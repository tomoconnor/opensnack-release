// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package route53 emulates the Route 53 REST-XML API: hosted zones, record
// sets, reusable delegation sets, health checks and tags. Route 53 is
// global, so everything is stored under util.GlobalNamespace.
package route53

import (
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	APIVersion = "2013-04-01"
	service    = "route53"

	typeZone          = "hostedzone"
	typeRecord        = "rrset"
	typeHealthCheck   = "healthcheck"
	typeDelegationSet = "delegationset"
)

type Handler struct {
	Store resource.Store

	// mu serialises writes so a change batch is validated and applied
	// against a consistent view of the zone.
	mu      sync.Mutex
	changes sync.Map // change ID -> submitted time
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// randomID returns prefix plus n characters in the alphabet Route 53 uses for
// zone, change and delegation set IDs.
func randomID(prefix string, n int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return prefix + string(b)
}

// Dispatch routes /2013-04-01/... requests. A legacy /route53 prefix in front
// of the version is tolerated.
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	i := strings.Index(path, "/"+APIVersion+"/")
	if i < 0 {
		writeError(w, http.StatusNotFound, "UnknownOperationException", "Unknown Route 53 operation: "+r.Method+" "+r.URL.Path)
		return
	}
	var seg []string
	for _, s := range strings.Split(strings.Trim(path[i+len(APIVersion)+2:], "/"), "/") {
		u, err := url.PathUnescape(s)
		if err != nil {
			u = s
		}
		seg = append(seg, u)
	}
	m := r.Method
	route := func(method string, parts ...string) bool {
		if m != method || len(parts) != len(seg) {
			return false
		}
		for i, p := range parts {
			if p != "*" && p != seg[i] {
				return false
			}
		}
		return true
	}

	switch {
	case route("POST", "hostedzone"):
		h.CreateHostedZone(w, r)
	case route("GET", "hostedzone"):
		h.ListHostedZones(w, r)
	case route("GET", "hostedzonesbyname"):
		h.ListHostedZonesByName(w, r)
	case route("GET", "hostedzonecount"):
		h.GetHostedZoneCount(w, r)
	case route("GET", "hostedzone", "*"):
		h.GetHostedZone(w, r, seg[1])
	case route("POST", "hostedzone", "*"):
		h.UpdateHostedZoneComment(w, r, seg[1])
	case route("DELETE", "hostedzone", "*"):
		h.DeleteHostedZone(w, r, seg[1])
	case route("POST", "hostedzone", "*", "rrset"):
		h.ChangeResourceRecordSets(w, r, seg[1])
	case route("GET", "hostedzone", "*", "rrset"):
		h.ListResourceRecordSets(w, r, seg[1])
	case route("POST", "hostedzone", "*", "associatevpc"):
		h.AssociateVPC(w, r, seg[1])
	case route("POST", "hostedzone", "*", "disassociatevpc"):
		h.DisassociateVPC(w, r, seg[1])
	case route("GET", "hostedzone", "*", "dnssec"):
		h.GetDNSSEC(w, r, seg[1])
	case route("GET", "change", "*"):
		h.GetChange(w, r, seg[1])
	case route("POST", "delegationset"):
		h.CreateReusableDelegationSet(w, r)
	case route("GET", "delegationset"):
		h.ListReusableDelegationSets(w, r)
	case route("GET", "delegationset", "*"):
		h.GetReusableDelegationSet(w, r, seg[1])
	case route("DELETE", "delegationset", "*"):
		h.DeleteReusableDelegationSet(w, r, seg[1])
	case route("GET", "tags", "*", "*"):
		h.ListTagsForResource(w, r, seg[1], seg[2])
	case route("POST", "tags", "*", "*"):
		h.ChangeTagsForResource(w, r, seg[1], seg[2])
	case route("POST", "tags", "*"):
		h.ListTagsForResources(w, r, seg[1])
	case route("POST", "healthcheck"):
		h.CreateHealthCheck(w, r)
	case route("GET", "healthcheck"):
		h.ListHealthChecks(w, r)
	case route("GET", "healthcheckcount"):
		h.GetHealthCheckCount(w, r)
	case route("GET", "healthcheck", "*"):
		h.GetHealthCheck(w, r, seg[1])
	case route("POST", "healthcheck", "*"):
		h.UpdateHealthCheck(w, r, seg[1])
	case route("DELETE", "healthcheck", "*"):
		h.DeleteHealthCheck(w, r, seg[1])
	default:
		writeError(w, http.StatusNotFound, "UnknownOperationException", "Unknown Route 53 operation: "+r.Method+" "+r.URL.Path)
	}
}

// Responses and errors.

func writeXML(w http.ResponseWriter, status int, v any) {
	out, err := xml.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	awsresponses.WriteAWSHeaders(w)
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	resp := errorResponse{Xmlns: xmlns, RequestID: awsresponses.NextRequestID()}
	resp.Error.Type = "Sender"
	if status >= 500 {
		resp.Error.Type = "Receiver"
	}
	resp.Error.Code = code
	resp.Error.Message = message
	writeXML(w, status, resp)
}

func writeInvalidInput(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, "InvalidInput", message)
}

// writeInvalidChangeBatch uses the plain error shape for one message and the
// InvalidChangeBatch document for several, as Route 53 does.
func writeInvalidChangeBatch(w http.ResponseWriter, messages []string) {
	if len(messages) == 1 {
		writeError(w, http.StatusBadRequest, "InvalidChangeBatch", messages[0])
		return
	}
	writeXML(w, http.StatusBadRequest, invalidChangeBatchResponse{
		Xmlns: xmlns, Messages: messages, RequestID: awsresponses.NextRequestID(),
	})
}

func decodeBody(r *http.Request, v any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return xml.Unmarshal(body, v)
}

func (h *Handler) newChange(comment string) ChangeInfo {
	id := randomID("C", 20)
	now := time.Now().UTC()
	h.changes.Store(id, now)
	return ChangeInfo{ID: "/change/" + id, Status: "PENDING", SubmittedAt: now.Format(time.RFC3339), Comment: comment}
}

// GetChange reports every change as INSYNC: changes apply immediately.
func (h *Handler) GetChange(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.TrimPrefix(id, "/change/")
	submitted := time.Now().UTC()
	if v, ok := h.changes.Load(id); ok {
		submitted = v.(time.Time)
	} else if len(id) < 2 || id[0] != 'C' {
		writeError(w, http.StatusNotFound, "NoSuchChange", "A change with the specified change ID does not exist: "+id)
		return
	}
	writeXML(w, http.StatusOK, GetChangeResponse{Xmlns: xmlns, ChangeInfo: ChangeInfo{
		ID: "/change/" + id, Status: "INSYNC", SubmittedAt: submitted.Format(time.RFC3339),
	}})
}

// Store helpers.

func (h *Handler) load(ns, typ, id string, v any) bool {
	res, err := h.Store.Get(id, service, typ, ns)
	if err != nil || res == nil {
		return false
	}
	return json.Unmarshal(res.Attributes, v) == nil
}

func (h *Handler) save(ns, typ, id string, v any, create bool) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	res := &resource.Resource{ID: id, Namespace: ns, Service: service, Type: typ, Attributes: buf}
	if create {
		return h.Store.Create(res)
	}
	return h.Store.Update(res)
}

// Hosted zones.

type zone struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	CallerReference string            `json:"caller_reference"`
	Comment         string            `json:"comment,omitempty"`
	Private         bool              `json:"private"`
	VPCs            []VPC             `json:"vpcs,omitempty"`
	DelegationSetID string            `json:"delegation_set_id,omitempty"`
	NameServers     []string          `json:"name_servers"`
	Tags            map[string]string `json:"tags,omitempty"`
}

func cleanZoneID(id string) string {
	return strings.TrimPrefix(strings.TrimPrefix(id, "/hostedzone/"), "hostedzone/")
}

func (h *Handler) getZone(ns, id string) (*zone, bool) {
	var z zone
	if !h.load(ns, typeZone, cleanZoneID(id), &z) {
		return nil, false
	}
	return &z, true
}

func writeNoSuchZone(w http.ResponseWriter, id string) {
	writeError(w, http.StatusNotFound, "NoSuchHostedZone", "No hosted zone found with ID: "+cleanZoneID(id))
}

func (h *Handler) zoneRecords(ns, zoneID string) []ResourceRecordSet {
	all, _ := h.Store.List(service, typeRecord, ns)
	var out []ResourceRecordSet
	for _, res := range all {
		if !strings.HasPrefix(res.ID, zoneID+"/") {
			continue
		}
		var rr ResourceRecordSet
		if json.Unmarshal(res.Attributes, &rr) == nil {
			out = append(out, rr)
		}
	}
	sortRecords(out)
	return out
}

func sortRecords(rrs []ResourceRecordSet) {
	sort.SliceStable(rrs, func(i, j int) bool {
		a, b := rrs[i], rrs[j]
		return compareRecordKeys(a.Name, a.Type, a.SetIdentifier, b.Name, b.Type, b.SetIdentifier) < 0
	})
}

func (h *Handler) hostedZoneOut(ns string, z *zone) HostedZone {
	return HostedZone{
		ID:                     "/hostedzone/" + z.ID,
		Name:                   z.Name,
		CallerReference:        z.CallerReference,
		Config:                 HostedZoneConfig{Comment: z.Comment, PrivateZone: z.Private},
		ResourceRecordSetCount: len(h.zoneRecords(ns, z.ID)),
	}
}

// publicNameServers makes four name servers in the style of awsdns.
func publicNameServers() []string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	n := func(i int) int { return int(b[i])<<8 | int(b[i+1]) }
	return []string{
		"ns-" + strconv.Itoa(n(0)%512) + ".awsdns-" + pad2(int(b[2])%64) + ".com.",
		"ns-" + strconv.Itoa(512+n(2)%512) + ".awsdns-" + pad2(int(b[4])%64) + ".net.",
		"ns-" + strconv.Itoa(1024+n(4)%512) + ".awsdns-" + pad2(int(b[6])%64) + ".org.",
		"ns-" + strconv.Itoa(1536+n(6)%512) + ".awsdns-" + pad2(int(b[1])%64) + ".co.uk.",
	}
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

var privateNameServers = []string{"ns-0.awsdns-00.com.", "ns-512.awsdns-00.net.", "ns-1024.awsdns-00.org.", "ns-1536.awsdns-00.co.uk."}

func (h *Handler) CreateHostedZone(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	var req CreateHostedZoneRequest
	if err := decodeBody(r, &req); err != nil {
		writeInvalidInput(w, "Could not parse CreateHostedZoneRequest: "+err.Error())
		return
	}
	if req.Name == "" || req.CallerReference == "" {
		writeInvalidInput(w, "Name and CallerReference are required")
		return
	}
	name := normalizeName(req.Name)
	if name == "." || !validHostname(name) {
		writeError(w, http.StatusBadRequest, "InvalidDomainName", "The domain name is invalid: "+req.Name)
		return
	}
	private := req.VPC != nil && req.VPC.VPCId != ""
	if req.HostedZoneConfig != nil && req.HostedZoneConfig.PrivateZone && !private {
		writeInvalidInput(w, "When you're creating a private hosted zone (when you specify true for PrivateZone), you must also specify values for VPCId and VPCRegion.")
		return
	}
	if private && req.DelegationSetID != "" {
		writeError(w, http.StatusBadRequest, "InvalidInput", "A private hosted zone cannot use a reusable delegation set.")
		return
	}
	if private && req.VPC.VPCRegion == "" {
		writeInvalidInput(w, "VPCRegion is required")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	zones, _ := h.Store.List(service, typeZone, ns)
	for _, res := range zones {
		var z zone
		if json.Unmarshal(res.Attributes, &z) == nil && z.CallerReference == req.CallerReference {
			writeError(w, http.StatusConflict, "HostedZoneAlreadyExists", "A hosted zone has already been created with the specified caller reference.")
			return
		}
	}

	z := zone{ID: randomID("Z", 20), Name: name, CallerReference: req.CallerReference, Private: private}
	if req.HostedZoneConfig != nil {
		z.Comment = req.HostedZoneConfig.Comment
	}
	switch {
	case private:
		z.VPCs = []VPC{*req.VPC}
		z.NameServers = privateNameServers
	case req.DelegationSetID != "":
		var ds delegationSet
		id := cleanDelegationSetID(req.DelegationSetID)
		if !h.load(ns, typeDelegationSet, id, &ds) {
			writeError(w, http.StatusBadRequest, "NoSuchDelegationSet", "A reusable delegation set with the specified ID does not exist: "+id)
			return
		}
		z.DelegationSetID = id
		z.NameServers = ds.NameServers
	default:
		z.NameServers = publicNameServers()
	}

	if err := h.save(ns, typeZone, z.ID, z, true); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	ttlNS, ttlSOA := int64(172800), int64(900)
	defaults := []ResourceRecordSet{
		{Name: name, Type: "NS", TTL: &ttlNS, ResourceRecords: values(z.NameServers...)},
		{Name: name, Type: "SOA", TTL: &ttlSOA, ResourceRecords: values(z.NameServers[0] + " awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400")},
	}
	for _, rr := range defaults {
		if err := h.save(ns, typeRecord, recordKey(z.ID, rr), rr, true); err != nil {
			writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
			return
		}
	}

	out := CreateHostedZoneResponse{
		Xmlns:         xmlns,
		HostedZone:    h.hostedZoneOut(ns, &z),
		ChangeInfo:    h.newChange(""),
		DelegationSet: DelegationSet{NameServers: z.NameServers},
	}
	if z.DelegationSetID != "" {
		out.DelegationSet.ID = "/delegationset/" + z.DelegationSetID
	}
	if private {
		out.VPC = &z.VPCs[0]
	}
	w.Header().Set("Location", util.BaseURL(r)+"/"+APIVersion+"/hostedzone/"+z.ID)
	writeXML(w, http.StatusCreated, out)
}

func values(vs ...string) []ResourceRecord {
	out := make([]ResourceRecord, len(vs))
	for i, v := range vs {
		out[i] = ResourceRecord{Value: v}
	}
	return out
}

func (h *Handler) GetHostedZone(w http.ResponseWriter, r *http.Request, id string) {
	ns := util.GlobalNamespace(r)
	z, ok := h.getZone(ns, id)
	if !ok {
		writeNoSuchZone(w, id)
		return
	}
	out := GetHostedZoneResponse{Xmlns: xmlns, HostedZone: h.hostedZoneOut(ns, z)}
	if z.Private {
		out.VPCs = z.VPCs
	} else {
		out.DelegationSet = &DelegationSet{NameServers: z.NameServers}
		if z.DelegationSetID != "" {
			out.DelegationSet.ID = "/delegationset/" + z.DelegationSetID
		}
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) UpdateHostedZoneComment(w http.ResponseWriter, r *http.Request, id string) {
	ns := util.GlobalNamespace(r)
	var req UpdateHostedZoneCommentRequest
	if err := decodeBody(r, &req); err != nil {
		writeInvalidInput(w, "Could not parse UpdateHostedZoneCommentRequest: "+err.Error())
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	z, ok := h.getZone(ns, id)
	if !ok {
		writeNoSuchZone(w, id)
		return
	}
	z.Comment = ""
	if req.Comment != nil {
		z.Comment = *req.Comment
	}
	if err := h.save(ns, typeZone, z.ID, z, false); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	writeXML(w, http.StatusOK, UpdateHostedZoneCommentResponse{Xmlns: xmlns, HostedZone: h.hostedZoneOut(ns, z)})
}

func (h *Handler) DeleteHostedZone(w http.ResponseWriter, r *http.Request, id string) {
	ns := util.GlobalNamespace(r)
	h.mu.Lock()
	defer h.mu.Unlock()
	z, ok := h.getZone(ns, id)
	if !ok {
		writeNoSuchZone(w, id)
		return
	}
	records := h.zoneRecords(ns, z.ID)
	for _, rr := range records {
		if !isApexDefault(rr, z.Name) {
			writeError(w, http.StatusBadRequest, "HostedZoneNotEmpty", "The specified hosted zone contains non-required resource record sets and so cannot be deleted.")
			return
		}
	}
	for _, rr := range records {
		_ = h.Store.Delete(recordKey(z.ID, rr), service, typeRecord, ns)
	}
	if err := h.Store.Delete(z.ID, service, typeZone, ns); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	writeXML(w, http.StatusOK, ChangeInfoResponse{XMLName: xml.Name{Local: "DeleteHostedZoneResponse"}, Xmlns: xmlns, ChangeInfo: h.newChange("")})
}

// isApexDefault reports whether rr is one of the NS and SOA record sets that
// every zone has at its apex and that can't be deleted.
func isApexDefault(rr ResourceRecordSet, zoneName string) bool {
	return rr.Name == zoneName && (rr.Type == "NS" || rr.Type == "SOA") && rr.SetIdentifier == ""
}

func (h *Handler) listZones(ns string) []zone {
	all, _ := h.Store.List(service, typeZone, ns)
	zones := make([]zone, 0, len(all))
	for _, res := range all {
		var z zone
		if json.Unmarshal(res.Attributes, &z) == nil {
			zones = append(zones, z)
		}
	}
	return zones
}

func maxItems(r *http.Request, def int) (int, bool) {
	v := r.URL.Query().Get("maxitems")
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	if n == 0 || n > def {
		n = def
	}
	return n, true
}

func (h *Handler) ListHostedZones(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	q := r.URL.Query()
	limit, ok := maxItems(r, 100)
	if !ok {
		writeInvalidInput(w, "maxitems must be a positive integer")
		return
	}
	zones := h.listZones(ns)
	sort.Slice(zones, func(i, j int) bool { return zones[i].ID < zones[j].ID })
	if ds := cleanDelegationSetID(q.Get("delegationsetid")); ds != "" {
		var keep []zone
		for _, z := range zones {
			if z.DelegationSetID == ds {
				keep = append(keep, z)
			}
		}
		zones = keep
	}
	if t := q.Get("hostedzonetype"); t == "PrivateHostedZone" {
		var keep []zone
		for _, z := range zones {
			if z.Private {
				keep = append(keep, z)
			}
		}
		zones = keep
	}
	marker := q.Get("marker")
	start := 0
	if marker != "" {
		start = len(zones)
		for i, z := range zones {
			if z.ID >= marker {
				start = i
				break
			}
		}
	}
	out := ListHostedZonesResponse{Xmlns: xmlns, Marker: marker, MaxItems: limit, HostedZones: []HostedZone{}}
	for i := start; i < len(zones); i++ {
		if len(out.HostedZones) == limit {
			out.IsTruncated = true
			out.NextMarker = zones[i].ID
			break
		}
		out.HostedZones = append(out.HostedZones, h.hostedZoneOut(ns, &zones[i]))
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) ListHostedZonesByName(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	q := r.URL.Query()
	limit, ok := maxItems(r, 100)
	if !ok {
		writeInvalidInput(w, "maxitems must be a positive integer")
		return
	}
	dnsName, startID := q.Get("dnsname"), cleanZoneID(q.Get("hostedzoneid"))
	if startID != "" && dnsName == "" {
		writeInvalidInput(w, "The DNSName must be specified when HostedZoneId is specified.")
		return
	}
	zones := h.listZones(ns)
	sort.Slice(zones, func(i, j int) bool {
		if c := compareNames(zones[i].Name, zones[j].Name); c != 0 {
			return c < 0
		}
		return zones[i].ID < zones[j].ID
	})
	out := ListHostedZonesByNameResponse{Xmlns: xmlns, MaxItems: limit, HostedZones: []HostedZone{}, HostedZoneID: startID}
	start := 0
	if dnsName != "" {
		out.DNSName = normalizeName(dnsName)
		start = len(zones)
		for i, z := range zones {
			c := compareNames(z.Name, out.DNSName)
			if c > 0 || c == 0 && z.ID >= startID {
				start = i
				break
			}
		}
	}
	for i := start; i < len(zones); i++ {
		if len(out.HostedZones) == limit {
			out.IsTruncated = true
			out.NextDNSName, out.NextHostedZoneID = zones[i].Name, zones[i].ID
			break
		}
		out.HostedZones = append(out.HostedZones, h.hostedZoneOut(ns, &zones[i]))
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) GetHostedZoneCount(w http.ResponseWriter, r *http.Request) {
	writeXML(w, http.StatusOK, GetHostedZoneCountResponse{Xmlns: xmlns, HostedZoneCount: len(h.listZones(util.GlobalNamespace(r)))})
}

func (h *Handler) AssociateVPC(w http.ResponseWriter, r *http.Request, id string) {
	h.changeVPC(w, r, id, true)
}

func (h *Handler) DisassociateVPC(w http.ResponseWriter, r *http.Request, id string) {
	h.changeVPC(w, r, id, false)
}

func (h *Handler) changeVPC(w http.ResponseWriter, r *http.Request, id string, associate bool) {
	ns := util.GlobalNamespace(r)
	var req VPCAssociationRequest
	if err := decodeBody(r, &req); err != nil || req.VPC.VPCId == "" {
		writeInvalidInput(w, "VPC is required")
		return
	}
	if req.VPC.VPCRegion == "" {
		req.VPC.VPCRegion = util.Region(r)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	z, ok := h.getZone(ns, id)
	if !ok {
		writeNoSuchZone(w, id)
		return
	}
	if !z.Private {
		writeError(w, http.StatusBadRequest, "PublicZoneVPCAssociation", "You're trying to associate a VPC with a public hosted zone. Amazon Route 53 doesn't support associating a VPC with a public hosted zone.")
		return
	}
	idx := -1
	for i, v := range z.VPCs {
		if v.VPCId == req.VPC.VPCId && v.VPCRegion == req.VPC.VPCRegion {
			idx = i
		}
	}
	name := "AssociateVPCWithHostedZoneResponse"
	if associate {
		if idx >= 0 {
			writeError(w, http.StatusConflict, "ConflictingDomainExists", "The VPC "+req.VPC.VPCId+" is already associated with the hosted zone.")
			return
		}
		z.VPCs = append(z.VPCs, req.VPC)
	} else {
		name = "DisassociateVPCFromHostedZoneResponse"
		if idx < 0 {
			writeError(w, http.StatusNotFound, "VPCAssociationNotFound", "The specified VPC "+req.VPC.VPCId+" is not associated with the hosted zone.")
			return
		}
		if len(z.VPCs) == 1 {
			writeError(w, http.StatusBadRequest, "LastVPCAssociation", "The VPC that you're trying to disassociate from the private hosted zone is the last VPC that is associated with the hosted zone. Amazon Route 53 doesn't support disassociating the last VPC from a hosted zone.")
			return
		}
		z.VPCs = append(z.VPCs[:idx], z.VPCs[idx+1:]...)
	}
	if err := h.save(ns, typeZone, z.ID, z, false); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	writeXML(w, http.StatusOK, ChangeInfoResponse{XMLName: xml.Name{Local: name}, Xmlns: xmlns, ChangeInfo: h.newChange(req.Comment)})
}

// GetDNSSEC reports that no zone is signed; the provider's force_destroy
// path asks before deleting a zone.
func (h *Handler) GetDNSSEC(w http.ResponseWriter, r *http.Request, id string) {
	z, ok := h.getZone(util.GlobalNamespace(r), id)
	if !ok {
		writeNoSuchZone(w, id)
		return
	}
	if z.Private {
		writeError(w, http.StatusBadRequest, "InvalidArgument", "Operation is unsupported for private hosted zones.")
		return
	}
	out := GetDNSSECResponse{Xmlns: xmlns}
	out.Status.ServeSignature = "NOT_SIGNING"
	writeXML(w, http.StatusOK, out)
}

// Reusable delegation sets.

type delegationSet struct {
	ID              string   `json:"id"`
	CallerReference string   `json:"caller_reference"`
	NameServers     []string `json:"name_servers"`
}

func cleanDelegationSetID(id string) string {
	return strings.TrimPrefix(id, "/delegationset/")
}

func (d delegationSet) out() DelegationSet {
	return DelegationSet{ID: "/delegationset/" + d.ID, CallerReference: d.CallerReference, NameServers: d.NameServers}
}

func (h *Handler) CreateReusableDelegationSet(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	var req CreateReusableDelegationSetRequest
	if err := decodeBody(r, &req); err != nil || req.CallerReference == "" {
		writeInvalidInput(w, "CallerReference is required")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	all, _ := h.Store.List(service, typeDelegationSet, ns)
	for _, res := range all {
		var d delegationSet
		if json.Unmarshal(res.Attributes, &d) == nil && d.CallerReference == req.CallerReference {
			writeError(w, http.StatusConflict, "DelegationSetAlreadyCreated", "A delegation set with the same owner and caller reference combination has already been created.")
			return
		}
	}
	d := delegationSet{ID: randomID("N", 20), CallerReference: req.CallerReference}
	var z *zone
	if req.HostedZoneID != "" {
		var ok bool
		if z, ok = h.getZone(ns, req.HostedZoneID); !ok {
			writeNoSuchZone(w, req.HostedZoneID)
			return
		}
		if z.Private {
			writeError(w, http.StatusBadRequest, "HostedZoneNotFound", "The specified hosted zone is a private hosted zone.")
			return
		}
		if z.DelegationSetID != "" {
			writeError(w, http.StatusConflict, "DelegationSetAlreadyReusable", "The specified delegation set has already been marked as reusable.")
			return
		}
		d.NameServers = z.NameServers
	} else {
		d.NameServers = publicNameServers()
	}
	if err := h.save(ns, typeDelegationSet, d.ID, d, true); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	if z != nil {
		z.DelegationSetID = d.ID
		_ = h.save(ns, typeZone, z.ID, z, false)
	}
	w.Header().Set("Location", util.BaseURL(r)+"/"+APIVersion+"/delegationset/"+d.ID)
	writeXML(w, http.StatusCreated, DelegationSetResponse{XMLName: xml.Name{Local: "CreateReusableDelegationSetResponse"}, Xmlns: xmlns, DelegationSet: d.out()})
}

func (h *Handler) GetReusableDelegationSet(w http.ResponseWriter, r *http.Request, id string) {
	var d delegationSet
	id = cleanDelegationSetID(id)
	if !h.load(util.GlobalNamespace(r), typeDelegationSet, id, &d) {
		writeError(w, http.StatusNotFound, "NoSuchDelegationSet", "A reusable delegation set with the specified ID does not exist: "+id)
		return
	}
	writeXML(w, http.StatusOK, DelegationSetResponse{XMLName: xml.Name{Local: "GetReusableDelegationSetResponse"}, Xmlns: xmlns, DelegationSet: d.out()})
}

func (h *Handler) ListReusableDelegationSets(w http.ResponseWriter, r *http.Request) {
	all, _ := h.Store.List(service, typeDelegationSet, util.GlobalNamespace(r))
	out := ListReusableDelegationSetsResponse{Xmlns: xmlns, MaxItems: 100, DelegationSets: []DelegationSet{}}
	for _, res := range all {
		var d delegationSet
		if json.Unmarshal(res.Attributes, &d) == nil {
			out.DelegationSets = append(out.DelegationSets, d.out())
		}
	}
	sort.Slice(out.DelegationSets, func(i, j int) bool { return out.DelegationSets[i].ID < out.DelegationSets[j].ID })
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) DeleteReusableDelegationSet(w http.ResponseWriter, r *http.Request, id string) {
	ns := util.GlobalNamespace(r)
	id = cleanDelegationSetID(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	var d delegationSet
	if !h.load(ns, typeDelegationSet, id, &d) {
		writeError(w, http.StatusNotFound, "NoSuchDelegationSet", "A reusable delegation set with the specified ID does not exist: "+id)
		return
	}
	for _, z := range h.listZones(ns) {
		if z.DelegationSetID == id {
			writeError(w, http.StatusBadRequest, "DelegationSetInUse", "The specified delegation contains associated hosted zones which must be deleted before the reusable delegation set can be deleted.")
			return
		}
	}
	if err := h.Store.Delete(id, service, typeDelegationSet, ns); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	writeXML(w, http.StatusOK, EmptyResponse{XMLName: xml.Name{Local: "DeleteReusableDelegationSetResponse"}, Xmlns: xmlns})
}

// Tags live on the zone or health check they belong to.

func (h *Handler) loadTagged(ns, resourceType, id string) (any, *map[string]string, string, bool) {
	switch resourceType {
	case "hostedzone":
		var z zone
		if !h.load(ns, typeZone, cleanZoneID(id), &z) {
			return nil, nil, typeZone, false
		}
		return &z, &z.Tags, typeZone, true
	case "healthcheck":
		var hc healthCheck
		if !h.load(ns, typeHealthCheck, id, &hc) {
			return nil, nil, typeHealthCheck, false
		}
		return &hc, &hc.Tags, typeHealthCheck, true
	}
	return nil, nil, "", false
}

func writeNoSuchTagged(w http.ResponseWriter, resourceType, id string) {
	switch resourceType {
	case "hostedzone":
		writeNoSuchZone(w, id)
	case "healthcheck":
		writeError(w, http.StatusNotFound, "NoSuchHealthCheck", "No health check exists with the specified ID "+id)
	default:
		writeInvalidInput(w, "Invalid resource type: "+resourceType)
	}
}

func tagList(m map[string]string) []Tag {
	tags := make([]Tag, 0, len(m))
	for k, v := range m {
		tags = append(tags, Tag{Key: k, Value: v})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Key < tags[j].Key })
	return tags
}

func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request, resourceType, id string) {
	_, tags, _, ok := h.loadTagged(util.GlobalNamespace(r), resourceType, id)
	if !ok {
		writeNoSuchTagged(w, resourceType, id)
		return
	}
	writeXML(w, http.StatusOK, ListTagsForResourceResponse{Xmlns: xmlns, ResourceTagSet: ResourceTagSet{
		ResourceType: resourceType, ResourceID: cleanZoneID(id), Tags: tagList(*tags),
	}})
}

func (h *Handler) ListTagsForResources(w http.ResponseWriter, r *http.Request, resourceType string) {
	ns := util.GlobalNamespace(r)
	var req ListTagsForResourcesRequest
	if err := decodeBody(r, &req); err != nil || len(req.ResourceIDs) == 0 {
		writeInvalidInput(w, "ResourceIds is required")
		return
	}
	if len(req.ResourceIDs) > 10 {
		writeInvalidInput(w, "At most 10 resource IDs can be specified")
		return
	}
	out := ListTagsForResourcesResponse{Xmlns: xmlns}
	for _, id := range req.ResourceIDs {
		_, tags, _, ok := h.loadTagged(ns, resourceType, id)
		if !ok {
			writeNoSuchTagged(w, resourceType, id)
			return
		}
		out.ResourceTagSets = append(out.ResourceTagSets, ResourceTagSet{ResourceType: resourceType, ResourceID: cleanZoneID(id), Tags: tagList(*tags)})
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) ChangeTagsForResource(w http.ResponseWriter, r *http.Request, resourceType, id string) {
	ns := util.GlobalNamespace(r)
	var req ChangeTagsForResourceRequest
	if err := decodeBody(r, &req); err != nil {
		writeInvalidInput(w, "Could not parse ChangeTagsForResourceRequest: "+err.Error())
		return
	}
	if len(req.AddTags) > 10 || len(req.RemoveTagKeys) > 10 {
		writeInvalidInput(w, "At most 10 tags can be added or removed in one request")
		return
	}
	for _, t := range req.AddTags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			writeInvalidInput(w, "Invalid tag: "+t.Key)
			return
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	obj, tags, typ, ok := h.loadTagged(ns, resourceType, id)
	if !ok {
		writeNoSuchTagged(w, resourceType, id)
		return
	}
	if *tags == nil {
		*tags = map[string]string{}
	}
	for _, k := range req.RemoveTagKeys {
		delete(*tags, k)
	}
	for _, t := range req.AddTags {
		(*tags)[t.Key] = t.Value
	}
	if len(*tags) > 50 {
		writeInvalidInput(w, "A resource can have at most 50 tags")
		return
	}
	if err := h.save(ns, typ, cleanZoneID(id), obj, false); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	writeXML(w, http.StatusOK, EmptyResponse{XMLName: xml.Name{Local: "ChangeTagsForResourceResponse"}, Xmlns: xmlns})
}
