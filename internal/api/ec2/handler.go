// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package ec2 emulates the EC2 Query API: instances, volumes, key pairs, AMIs
// and the VPC control plane (VPCs, subnets, route tables, gateways, elastic
// IPs, security groups, network ACLs and network interfaces).
//
// Every resource is one store record (JSON of the structs in types.go), in
// the request's namespace and region. Each VPC also gets the default security
// group, main route table and network ACL that AWS creates with it, and each
// namespace and region has a default VPC that is generated on the fly and
// only written to the store once something changes it.
package ec2

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	APIVersion = "2016-11-15"
	xmlns      = "http://ec2.amazonaws.com/doc/2016-11-15/"
)

type Handler struct {
	Store resource.Store

	// mu serialises EC2 requests: IP allocation, CIDR conflict checks and
	// the read-modify-write of rules need a consistent view.
	mu sync.Mutex
	// gone keeps terminated instances visible to DescribeInstances (AWS
	// shows them for about an hour) without leaving them in the store.
	gone map[string]goneInstance
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store, gone: map[string]goneInstance{}}
}

// call is one request in flight.
type call struct {
	h       *Handler
	r       *http.Request
	p       params
	ns      string // store scope, "namespace@region"
	region  string
	account string

	defs    map[string]map[string]object // generated default VPC, see defaults
	deleted map[string]bool              // tombstoned default resources
}

type action func(c *call) (any, error)

var actions map[string]action

func init() {
	actions = map[string]action{}
	for _, group := range []map[string]action{
		tagActions, metaActions, keyPairActions, instanceActions, volumeActions,
		vpcActions, subnetActions, routeTableActions, gatewayActions,
		securityGroupActions, naclActions, eniActions, endpointActions,
	} {
		for k, v := range group {
			actions[k] = v
		}
	}
}

// Dispatch handles EC2 Query API requests.
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	p := params(r.Form)
	name := p.get("Action")
	act, ok := actions[name]
	if !ok {
		writeError(w, &apiError{Code: "InvalidAction", Message: fmt.Sprintf("The action %s is not valid for this web service.", name)})
		return
	}
	id := util.IdentityFrom(r)
	c := &call{
		h:       h,
		r:       r,
		p:       p,
		ns:      util.NamespaceFromHeader(r),
		region:  id.Region,
		account: id.AccountID,
	}

	h.mu.Lock()
	body, err := act(c)
	h.mu.Unlock()

	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			ae = &apiError{Status: http.StatusInternalServerError, Code: "InternalError", Message: err.Error()}
		}
		writeError(w, ae)
		return
	}
	reply(w, name, body)
}

// reply writes body as <{action}Response> with a requestId first.
func reply(w http.ResponseWriter, action string, body any) {
	if body == nil {
		body = struct {
			Return bool `xml:"return"`
		}{true}
	}
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	start := xml.StartElement{
		Name: xml.Name{Local: action + "Response"},
		Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: xmlns}},
	}
	if err := enc.EncodeElement(body, start); err != nil {
		writeError(w, &apiError{Status: 500, Code: "InternalError", Message: err.Error()})
		return
	}
	out := buf.Bytes()
	i := bytes.IndexByte(out, '>') + 1
	awsresponses.WriteAWSHeaders(w)
	w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xml.Header))
	w.Write(out[:i])
	fmt.Fprintf(w, "<requestId>%s</requestId>", awsresponses.NextRequestID())
	w.Write(out[i:])
}

// apiError is an EC2 error response. Status defaults to 400, which is what
// EC2 uses for client errors, not-found included.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errorf(code, format string, args ...any) error {
	return &apiError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func notFound(code, id string) error {
	return errorf(code, "The %s '%s' does not exist", notFoundNoun(code), id)
}

func missing(name string) error {
	return errorf("MissingParameter", "The request must contain the parameter %s", name)
}

func invalidParam(name, value string) error {
	return errorf("InvalidParameterValue", "Value (%s) for parameter %s is invalid.", value, name)
}

// writeError writes EC2's error shape, which differs from the other Query
// services: <Response><Errors><Error>...</Error></Errors><RequestID/>.
func writeError(w http.ResponseWriter, e *apiError) {
	status := e.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	type errXML struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	type respXML struct {
		XMLName   xml.Name `xml:"Response"`
		Errors    []errXML `xml:"Errors>Error"`
		RequestID string   `xml:"RequestID"`
	}
	out, _ := xml.Marshal(respXML{Errors: []errXML{{e.Code, e.Message}}, RequestID: awsresponses.NextRequestID()})
	awsresponses.WriteAWSHeaders(w)
	w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
	w.WriteHeader(status)
	w.Write([]byte(xml.Header))
	w.Write(out)
}

func notFoundNoun(code string) string {
	switch code {
	case "InvalidVpcID.NotFound":
		return "vpc ID"
	case "InvalidSubnetID.NotFound":
		return "subnet ID"
	case "InvalidRouteTableID.NotFound":
		return "routeTable ID"
	case "InvalidInternetGatewayID.NotFound":
		return "internetGateway ID"
	case "NatGatewayNotFound":
		return "natGateway ID"
	case "InvalidAllocationID.NotFound":
		return "allocation ID"
	case "InvalidGroup.NotFound":
		return "security group"
	case "InvalidSecurityGroupRuleId.NotFound":
		return "security group rule ID"
	case "InvalidNetworkAclID.NotFound":
		return "network ACL ID"
	case "InvalidNetworkInterfaceID.NotFound":
		return "network interface ID"
	case "InvalidInstanceID.NotFound":
		return "instance ID"
	case "InvalidVolume.NotFound":
		return "volume"
	case "InvalidKeyPair.NotFound":
		return "key pair"
	case "InvalidAMIID.NotFound":
		return "image id"
	case "InvalidVpcEndpointId.NotFound":
		return "VPC endpoint"
	case "InvalidAssociationID.NotFound":
		return "association ID"
	}
	return "resource"
}
