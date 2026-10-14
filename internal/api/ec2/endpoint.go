// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"slices"
	"strings"
)

var endpointActions = map[string]action{
	"CreateVpcEndpoint":    createVpcEndpoint,
	"DeleteVpcEndpoints":   deleteVpcEndpoints,
	"DescribeVpcEndpoints": describeVpcEndpoints,
	"ModifyVpcEndpoint":    modifyVpcEndpoint,
}

const defaultEndpointPolicy = `{"Version":"2008-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"*","Resource":"*"}]}`

func (e *VpcEndpoint) objectID() string { return e.VpcEndpointId }
func (e *VpcEndpoint) tagList() *[]Tag  { return &e.Tags }
func (e *VpcEndpoint) filterValues() map[string][]string {
	return map[string][]string{
		"vpc-endpoint-id": {e.VpcEndpointId}, "vpc-id": {e.VpcId}, "service-name": {e.ServiceName},
		"vpc-endpoint-state": {e.State}, "vpc-endpoint-type": {e.VpcEndpointType}, "ip-address-type": {e.IpAddressType},
	}
}

// endpointServices are the AWS services an endpoint can reach, and whether
// each offers a gateway endpoint.
var endpointServices = map[string]bool{
	"s3": true, "dynamodb": true, "sqs": false, "sns": false, "ssm": false, "ssmmessages": false, "ec2": false,
	"ec2messages": false, "logs": false, "secretsmanager": false, "kms": false, "sts": false, "lambda": false,
	"execute-api": false, "ecr.api": false, "ecr.dkr": false, "monitoring": false, "events": false, "elasticache": false,
}

func (c *call) endpointService(name string) (svc string, gateway bool, ok bool) {
	svc, ok = strings.CutPrefix(name, "com.amazonaws."+c.region+".")
	if !ok {
		return "", false, false
	}
	gateway, ok = endpointServices[svc]
	return svc, gateway, ok
}

func createVpcEndpoint(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	name := c.p.get("ServiceName")
	svc, hasGateway, ok := c.endpointService(name)
	if !ok {
		return nil, errorf("InvalidServiceName", "The Vpc Endpoint Service '%s' does not exist", name)
	}
	typ := c.p.get("VpcEndpointType")
	if typ == "" {
		typ = "Gateway"
	}
	if typ == "Gateway" && !hasGateway {
		return nil, errorf("InvalidParameter", "The Vpc Endpoint Service '%s' does not support endpoint type Gateway", name)
	}
	if typ != "Gateway" && typ != "Interface" {
		return nil, errorf("UnsupportedOperation", "Endpoint type %s is not supported", typ)
	}
	tags, err := c.tagSpec("vpc-endpoint")
	if err != nil {
		return nil, err
	}
	policy := c.p.get("PolicyDocument")
	if policy == "" {
		policy = defaultEndpointPolicy
	}
	e := &VpcEndpoint{
		VpcEndpointId: newID("vpce"), VpcEndpointType: typ, VpcId: vpc.VpcId, ServiceName: name, State: "available",
		PolicyDocument: policy, IpAddressType: "ipv4", CreationTimestamp: now(), OwnerId: c.account, Tags: tags,
	}
	if typ == "Gateway" {
		if err := c.endpointRoutes(e, svc, c.p.list("RouteTableId"), nil); err != nil {
			return nil, err
		}
	} else {
		e.PrivateDnsEnabled = c.p.optBool("PrivateDnsEnabled") == nil || c.p.boolean("PrivateDnsEnabled")
		e.DnsOptions = &DnsOptions{DnsRecordIpType: "ipv4"}
		if g, err := c.resolveGroups(vpc.VpcId, c.p.list("SecurityGroupId"), nil); err != nil {
			return nil, err
		} else {
			e.Groups = g
		}
		if err := c.endpointSubnets(e, c.p.list("SubnetId"), nil); err != nil {
			return nil, err
		}
	}
	if err := c.put(tEndpoint, e); err != nil {
		return nil, err
	}
	return struct {
		E *VpcEndpoint `xml:"vpcEndpoint"`
	}{e}, nil
}

// endpointRoutes adds and removes a gateway endpoint's prefix-list routes.
func (c *call) endpointRoutes(e *VpcEndpoint, svc string, add, remove []string) error {
	pl := stableID("pl", c.region, svc)[:11]
	for _, id := range remove {
		rt, err := c.routeTable(id)
		if err != nil {
			return err
		}
		rt.Routes = slices.DeleteFunc(rt.Routes, func(r Route) bool { return r.GatewayId == e.VpcEndpointId })
		if err := c.put(tRTB, rt); err != nil {
			return err
		}
		e.RouteTableIds = slices.DeleteFunc(e.RouteTableIds, func(x string) bool { return x == id })
	}
	for _, id := range add {
		rt, err := c.routeTable(id)
		if err != nil {
			return err
		}
		if rt.VpcId != e.VpcId {
			return errorf("InvalidParameterValue", "Route table %s is not in VPC %s", id, e.VpcId)
		}
		rt.Routes = append(rt.Routes, Route{DestinationPrefixListId: pl, GatewayId: e.VpcEndpointId, State: "active", Origin: "CreateRoute"})
		if err := c.put(tRTB, rt); err != nil {
			return err
		}
		e.RouteTableIds = append(e.RouteTableIds, id)
	}
	return nil
}

// endpointSubnets gives an interface endpoint one interface per subnet.
func (c *call) endpointSubnets(e *VpcEndpoint, add, remove []string) error {
	for _, id := range remove {
		for _, eniID := range e.NetworkInterfaceIds {
			if eni, err := c.eni(eniID); err == nil && eni.SubnetId == id {
				if err := c.remove(tENI, eniID); err != nil {
					return err
				}
				e.NetworkInterfaceIds = slices.DeleteFunc(e.NetworkInterfaceIds, func(x string) bool { return x == eniID })
			}
		}
		e.SubnetIds = slices.DeleteFunc(e.SubnetIds, func(x string) bool { return x == id })
	}
	for _, id := range add {
		s, err := c.subnet(id)
		if err != nil {
			return err
		}
		eni, err := c.createEni(s, eniSpec{description: "VPC Endpoint Interface " + e.VpcEndpointId, ifType: "vpc_endpoint",
			requester: "amazon-vpce", groups: idsOf(e.Groups)})
		if err != nil {
			return err
		}
		e.SubnetIds = append(e.SubnetIds, id)
		e.NetworkInterfaceIds = append(e.NetworkInterfaceIds, eni.NetworkInterfaceId)
	}
	return nil
}

func describeVpcEndpoints(c *call) (any, error) {
	es, err := describe[VpcEndpoint](c, tEndpoint, c.p.list("VpcEndpointId"), "InvalidVpcEndpointId.NotFound")
	if err != nil {
		return nil, err
	}
	return struct {
		Es []*VpcEndpoint `xml:"vpcEndpointSet>item"`
	}{es}, nil
}

func deleteVpcEndpoints(c *call) (any, error) {
	type failure struct {
		ResourceId string `xml:"resourceId"`
		Error      struct {
			Code    string `xml:"code"`
			Message string `xml:"message"`
		} `xml:"error"`
	}
	var failed []failure
	for _, id := range c.p.list("VpcEndpointId") {
		e := new(VpcEndpoint)
		if ok, err := c.get(tEndpoint, id, e); err != nil {
			return nil, err
		} else if !ok {
			f := failure{ResourceId: id}
			f.Error.Code, f.Error.Message = "InvalidVpcEndpoint.NotFound", "The Vpc Endpoint Id '"+id+"' does not exist"
			failed = append(failed, f)
			continue
		}
		svc, _, _ := c.endpointService(e.ServiceName)
		if err := c.endpointRoutes(e, svc, nil, slices.Clone(e.RouteTableIds)); err != nil {
			return nil, err
		}
		for _, eniID := range e.NetworkInterfaceIds {
			if err := c.remove(tENI, eniID); err != nil {
				return nil, err
			}
		}
		if err := c.remove(tEndpoint, id); err != nil {
			return nil, err
		}
	}
	return struct {
		Failed []failure `xml:"unsuccessful>item"`
	}{failed}, nil
}

func modifyVpcEndpoint(c *call) (any, error) {
	e := new(VpcEndpoint)
	id := c.p.get("VpcEndpointId")
	if ok, err := c.get(tEndpoint, id, e); err != nil || !ok {
		if err == nil {
			err = notFound("InvalidVpcEndpointId.NotFound", id)
		}
		return nil, err
	}
	svc, _, _ := c.endpointService(e.ServiceName)
	if err := c.endpointRoutes(e, svc, c.p.list("AddRouteTableId"), c.p.list("RemoveRouteTableId")); err != nil {
		return nil, err
	}
	if add, rm := c.p.list("AddSecurityGroupId"), c.p.list("RemoveSecurityGroupId"); len(add)+len(rm) > 0 {
		ids := slices.DeleteFunc(idsOf(e.Groups), func(x string) bool { return slices.Contains(rm, x) })
		groups, err := c.resolveGroups(e.VpcId, append(ids, add...), nil)
		if err != nil {
			return nil, err
		}
		e.Groups = groups
	}
	if err := c.endpointSubnets(e, c.p.list("AddSubnetId"), c.p.list("RemoveSubnetId")); err != nil {
		return nil, err
	}
	if v := c.p.get("PolicyDocument"); v != "" {
		e.PolicyDocument = v
	}
	if c.p.boolean("ResetPolicy") {
		e.PolicyDocument = defaultEndpointPolicy
	}
	if v := c.p.optBool("PrivateDnsEnabled"); v != nil {
		e.PrivateDnsEnabled = *v
	}
	return nil, c.put(tEndpoint, e)
}

func describeVpcEndpointServices(c *call) (any, error) {
	type typ struct {
		ServiceType string `xml:"serviceType"`
	}
	type detail struct {
		ServiceName       string   `xml:"serviceName"`
		ServiceId         string   `xml:"serviceId"`
		ServiceType       []typ    `xml:"serviceType>item"`
		AvailabilityZones []string `xml:"availabilityZoneSet>item"`
		Owner             string   `xml:"owner"`
		BaseEndpoints     []string `xml:"baseEndpointDnsNameSet>item"`
		PolicySupported   bool     `xml:"vpcEndpointPolicySupported"`
		AcceptanceReq     bool     `xml:"acceptanceRequired"`
		ManagesEndpoints  bool     `xml:"managesVpcEndpoints"`
		IpTypes           []string `xml:"supportedIpAddressTypeSet>item"`
	}
	names := c.p.list("ServiceName")
	var svcs []string
	for s := range endpointServices {
		svcs = append(svcs, s)
	}
	slices.Sort(svcs)
	var outNames []string
	var details []detail
	for _, s := range svcs {
		name := "com.amazonaws." + c.region + "." + s
		if len(names) > 0 && !slices.Contains(names, name) {
			continue
		}
		types := []string{"Interface"}
		if endpointServices[s] {
			types = []string{"Gateway"}
			if s == "s3" {
				types = append(types, "Interface")
			}
		}
		for _, t := range types {
			ok, err := matchVals(c.p.filters(), map[string][]string{"service-name": {name}, "service-type": {t}, "owner": {"amazon"}})
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			outNames = append(outNames, name)
			details = append(details, detail{
				ServiceName: name, ServiceId: stableID("vpce-svc", c.region, s, t), ServiceType: []typ{{t}},
				AvailabilityZones: zoneNames(c.region), Owner: "amazon",
				BaseEndpoints:   []string{s + "." + c.region + ".amazonaws.com"},
				PolicySupported: true, IpTypes: []string{"ipv4"},
			})
		}
	}
	return struct {
		Names   []string `xml:"serviceNameSet>item"`
		Details []detail `xml:"serviceDetailSet>item"`
	}{outNames, details}, nil
}
