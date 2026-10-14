// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"slices"
	"strings"
)

var routeTableActions = map[string]action{
	"CreateRouteTable":             createRouteTable,
	"DeleteRouteTable":             deleteRouteTable,
	"DescribeRouteTables":          describeRouteTables,
	"CreateRoute":                  createRoute,
	"ReplaceRoute":                 replaceRoute,
	"DeleteRoute":                  deleteRoute,
	"AssociateRouteTable":          associateRouteTable,
	"DisassociateRouteTable":       disassociateRouteTable,
	"ReplaceRouteTableAssociation": replaceRouteTableAssociation,
}

func (rt *RouteTable) objectID() string { return rt.RouteTableId }
func (rt *RouteTable) tagList() *[]Tag  { return &rt.Tags }
func (rt *RouteTable) filterValues() map[string][]string {
	m := map[string][]string{"route-table-id": {rt.RouteTableId}, "vpc-id": {rt.VpcId}, "owner-id": {rt.OwnerId}}
	add := func(k string, v string) {
		m[k] = append(m[k], nonEmpty(v)...)
	}
	for _, a := range rt.Associations {
		add("association.route-table-association-id", a.RouteTableAssociationId)
		add("association.route-table-id", a.RouteTableId)
		add("association.subnet-id", a.SubnetId)
		add("association.gateway-id", a.GatewayId)
		add("association.main", b2s(a.Main))
		add("association.association-state", a.AssociationState.State)
	}
	for _, r := range rt.Routes {
		add("route.destination-cidr-block", r.DestinationCidrBlock)
		add("route.destination-ipv6-cidr-block", r.DestinationIpv6CidrBlock)
		add("route.destination-prefix-list-id", r.DestinationPrefixListId)
		add("route.gateway-id", r.GatewayId)
		add("route.nat-gateway-id", r.NatGatewayId)
		add("route.instance-id", r.InstanceId)
		add("route.network-interface-id", r.NetworkInterfaceId)
		add("route.vpc-peering-connection-id", r.VpcPeeringConnectionId)
		add("route.transit-gateway-id", r.TransitGatewayId)
		add("route.egress-only-internet-gateway-id", r.EgressOnlyInternetGatewayId)
		add("route.origin", r.Origin)
		add("route.state", r.State)
	}
	// A table without associations or routes still accepts these filters.
	for _, k := range []string{"association.route-table-association-id", "association.route-table-id", "association.subnet-id",
		"association.gateway-id", "association.main", "association.association-state"} {
		add(k, "")
	}
	return m
}

func (rt *RouteTable) isMain() bool {
	return slices.ContainsFunc(rt.Associations, func(a RTAssoc) bool { return a.Main })
}

func (c *call) routeTable(id string) (*RouteTable, error) {
	return must[RouteTable](c, tRTB, id, "InvalidRouteTableID.NotFound")
}

func createRouteTable(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	tags, err := c.tagSpec("route-table")
	if err != nil {
		return nil, err
	}
	rt := &RouteTable{RouteTableId: newID("rtb"), VpcId: vpc.VpcId, OwnerId: c.account, Tags: tags}
	for _, a := range vpc.CidrBlockAssociationSet {
		rt.Routes = append(rt.Routes, localRoute(a.CidrBlock))
	}
	if err := c.put(tRTB, rt); err != nil {
		return nil, err
	}
	return struct {
		RT *RouteTable `xml:"routeTable"`
	}{rt}, nil
}

func deleteRouteTable(c *call) (any, error) {
	rt, err := c.routeTable(c.p.get("RouteTableId"))
	if err != nil {
		return nil, err
	}
	if len(rt.Associations) > 0 {
		return nil, errorf("DependencyViolation", "The routeTable '%s' has dependencies and cannot be deleted.", rt.RouteTableId)
	}
	return nil, c.remove(tRTB, rt.RouteTableId)
}

func describeRouteTables(c *call) (any, error) {
	rts, err := describe[RouteTable](c, tRTB, c.p.list("RouteTableId"), "InvalidRouteTableID.NotFound")
	if err != nil {
		return nil, err
	}
	live := c.liveTargets()
	for _, rt := range rts {
		for i := range rt.Routes {
			rt.Routes[i].State = routeState(rt.Routes[i], live)
		}
	}
	return struct {
		RTs []*RouteTable `xml:"routeTableSet>item"`
	}{rts}, nil
}

// liveTargets is the set of gateway, NAT and interface IDs that exist, so
// routes to anything else show as blackholes, as in AWS.
func (c *call) liveTargets() map[string]bool {
	live := map[string]bool{"local": true}
	igws, _ := all[InternetGateway](c, tIGW)
	for _, g := range igws {
		for _, a := range g.Attachments {
			live[g.InternetGatewayId+"/"+a.VpcId] = true
		}
		live[g.InternetGatewayId] = len(g.Attachments) > 0
	}
	nats, _ := all[NatGateway](c, tNAT)
	for _, n := range nats {
		live[n.NatGatewayId] = n.State == "available"
	}
	enis, _ := all[NetworkInterface](c, tENI)
	for _, e := range enis {
		live[e.NetworkInterfaceId] = true
	}
	eps, _ := all[VpcEndpoint](c, tEndpoint)
	for _, e := range eps {
		live[e.VpcEndpointId] = true
	}
	return live
}

func routeState(r Route, live map[string]bool) string {
	for _, t := range []string{r.GatewayId, r.NatGatewayId, r.NetworkInterfaceId} {
		if t != "" && !live[t] && !strings.HasPrefix(t, "vgw-") {
			return "blackhole"
		}
	}
	return "active"
}

// routeDest identifies a route by its destination parameter.
func routeDest(p params) (field, value string, err error) {
	switch {
	case p.get("DestinationCidrBlock") != "":
		v := p.get("DestinationCidrBlock")
		if _, ok := parseCIDR(v); !ok {
			return "", "", errorf("InvalidParameterValue", "Value (%s) for parameter destinationCidrBlock is invalid. This is not a valid CIDR block.", v)
		}
		return "cidr", v, nil
	case p.get("DestinationIpv6CidrBlock") != "":
		return "ipv6", p.get("DestinationIpv6CidrBlock"), nil
	case p.get("DestinationPrefixListId") != "":
		return "pl", p.get("DestinationPrefixListId"), nil
	}
	return "", "", missing("DestinationCidrBlock")
}

func routeMatches(r Route, field, value string) bool {
	switch field {
	case "cidr":
		return r.DestinationCidrBlock == value
	case "ipv6":
		return r.DestinationIpv6CidrBlock == value
	}
	return r.DestinationPrefixListId == value
}

// routeTarget builds a route from the request's destination and target and
// checks the target exists in the route table's VPC.
func (c *call) routeTarget(rt *RouteTable) (Route, error) {
	field, value, err := routeDest(c.p)
	if err != nil {
		return Route{}, err
	}
	r := Route{State: "active", Origin: "CreateRoute"}
	switch field {
	case "cidr":
		r.DestinationCidrBlock = value
	case "ipv6":
		r.DestinationIpv6CidrBlock = value
	default:
		r.DestinationPrefixListId = value
	}
	p := c.p
	r.GatewayId = p.get("GatewayId")
	r.NatGatewayId = p.get("NatGatewayId")
	r.InstanceId = p.get("InstanceId")
	r.NetworkInterfaceId = p.get("NetworkInterfaceId")
	r.VpcPeeringConnectionId = p.get("VpcPeeringConnectionId")
	r.TransitGatewayId = p.get("TransitGatewayId")
	r.EgressOnlyInternetGatewayId = p.get("EgressOnlyInternetGatewayId")
	r.LocalGatewayId = p.get("LocalGatewayId")
	r.CarrierGatewayId = p.get("CarrierGatewayId")
	r.CoreNetworkArn = p.get("CoreNetworkArn")
	if v := p.get("VpcEndpointId"); v != "" {
		r.GatewayId = v
	}
	targets := nonEmpty(r.GatewayId, r.NatGatewayId, r.InstanceId, r.NetworkInterfaceId, r.VpcPeeringConnectionId,
		r.TransitGatewayId, r.EgressOnlyInternetGatewayId, r.LocalGatewayId, r.CarrierGatewayId, r.CoreNetworkArn)
	if len(targets) != 1 {
		return r, errorf("InvalidParameterCombination", "The parameter combination is not valid. Specify exactly one route target.")
	}
	switch {
	case strings.HasPrefix(r.GatewayId, "igw-"):
		g, err := must[InternetGateway](c, tIGW, r.GatewayId, "InvalidInternetGatewayID.NotFound")
		if err != nil {
			return r, err
		}
		if !slices.ContainsFunc(g.Attachments, func(a IgwAttach) bool { return a.VpcId == rt.VpcId }) {
			return r, errorf("InvalidParameterValue", "route table %s and network gateway %s belong to different networks", rt.RouteTableId, r.GatewayId)
		}
	case r.GatewayId == "local":
		return r, errorf("InvalidParameterValue", "Cannot create or replace a route with the local gateway")
	case r.NatGatewayId != "":
		if _, err := must[NatGateway](c, tNAT, r.NatGatewayId, "InvalidNatGatewayID.NotFound"); err != nil {
			return r, err
		}
	case r.InstanceId != "":
		inst, err := c.instance(r.InstanceId)
		if err != nil {
			return r, err
		}
		if len(inst.Enis) > 0 {
			r.NetworkInterfaceId = inst.Enis[0]
		}
		r.InstanceOwnerId = c.account
	case r.NetworkInterfaceId != "":
		eni, err := c.eni(r.NetworkInterfaceId)
		if err != nil {
			return r, err
		}
		if eni.Attachment != nil && eni.Attachment.InstanceId != "" {
			r.InstanceId, r.InstanceOwnerId = eni.Attachment.InstanceId, c.account
		}
	}
	return r, nil
}

func createRoute(c *call) (any, error) {
	rt, err := c.routeTable(c.p.get("RouteTableId"))
	if err != nil {
		return nil, err
	}
	r, err := c.routeTarget(rt)
	if err != nil {
		return nil, err
	}
	field, value, _ := routeDest(c.p)
	if slices.ContainsFunc(rt.Routes, func(x Route) bool { return routeMatches(x, field, value) }) {
		return nil, errorf("RouteAlreadyExists", "The route identified by %s already exists.", value)
	}
	rt.Routes = append(rt.Routes, r)
	return nil, c.put(tRTB, rt)
}

func replaceRoute(c *call) (any, error) {
	rt, err := c.routeTable(c.p.get("RouteTableId"))
	if err != nil {
		return nil, err
	}
	field, value, err := routeDest(c.p)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(rt.Routes, func(x Route) bool { return routeMatches(x, field, value) })
	if i < 0 {
		return nil, errorf("InvalidParameterValue", "There is no route defined for '%s' in the route table. Use CreateRoute instead.", value)
	}
	if rt.Routes[i].GatewayId == "local" && c.p.get("LocalTarget") == "true" {
		return nil, nil
	}
	r, err := c.routeTarget(rt)
	if err != nil {
		return nil, err
	}
	r.Origin = rt.Routes[i].Origin
	rt.Routes[i] = r
	return nil, c.put(tRTB, rt)
}

func deleteRoute(c *call) (any, error) {
	rt, err := c.routeTable(c.p.get("RouteTableId"))
	if err != nil {
		return nil, err
	}
	field, value, err := routeDest(c.p)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(rt.Routes, func(x Route) bool { return routeMatches(x, field, value) })
	if i < 0 {
		return nil, errorf("InvalidRoute.NotFound", "no route with destination %s in route table %s", value, rt.RouteTableId)
	}
	if rt.Routes[i].GatewayId == "local" {
		return nil, errorf("InvalidParameterValue", "cannot remove local route %s in route table %s", value, rt.RouteTableId)
	}
	rt.Routes = slices.Delete(rt.Routes, i, i+1)
	return nil, c.put(tRTB, rt)
}

type assocResult struct {
	AssociationId string `xml:"associationId"`
	State         state  `xml:"associationState"`
}

func associateRouteTable(c *call) (any, error) {
	rt, err := c.routeTable(c.p.get("RouteTableId"))
	if err != nil {
		return nil, err
	}
	a := RTAssoc{RouteTableAssociationId: newID("rtbassoc"), RouteTableId: rt.RouteTableId, AssociationState: state{"associated"}}
	switch {
	case c.p.get("SubnetId") != "":
		s, err := c.subnet(c.p.get("SubnetId"))
		if err != nil {
			return nil, err
		}
		if s.VpcId != rt.VpcId {
			return nil, errorf("InvalidParameterValue", "Route table %s and subnet %s belong to different networks", rt.RouteTableId, s.SubnetId)
		}
		rts, _ := all[RouteTable](c, tRTB)
		for _, other := range rts {
			for _, x := range other.Associations {
				if x.SubnetId == s.SubnetId {
					return nil, errorf("Resource.AlreadyAssociated", "the specified association for route table %s conflicts with an existing association", rt.RouteTableId)
				}
			}
		}
		a.SubnetId = s.SubnetId
	case c.p.get("GatewayId") != "":
		a.GatewayId = c.p.get("GatewayId")
	default:
		return nil, missing("SubnetId")
	}
	rt.Associations = append(rt.Associations, a)
	if err := c.put(tRTB, rt); err != nil {
		return nil, err
	}
	return assocResult{a.RouteTableAssociationId, a.AssociationState}, nil
}

// findAssociation locates a route table association by ID.
func (c *call) findAssociation(id string) (*RouteTable, int, error) {
	rts, err := all[RouteTable](c, tRTB)
	if err != nil {
		return nil, 0, err
	}
	for _, rt := range rts {
		if i := slices.IndexFunc(rt.Associations, func(a RTAssoc) bool { return a.RouteTableAssociationId == id }); i >= 0 {
			return rt, i, nil
		}
	}
	return nil, 0, errorf("InvalidAssociationID.NotFound", "The association ID '%s' does not exist", id)
}

func disassociateRouteTable(c *call) (any, error) {
	rt, i, err := c.findAssociation(c.p.get("AssociationId"))
	if err != nil {
		return nil, err
	}
	if rt.Associations[i].Main {
		return nil, errorf("InvalidParameterValue", "cannot disassociate the main route table association %s", rt.Associations[i].RouteTableAssociationId)
	}
	rt.Associations = slices.Delete(rt.Associations, i, i+1)
	return nil, c.put(tRTB, rt)
}

func replaceRouteTableAssociation(c *call) (any, error) {
	old, i, err := c.findAssociation(c.p.get("AssociationId"))
	if err != nil {
		return nil, err
	}
	target, err := c.routeTable(c.p.get("RouteTableId"))
	if err != nil {
		return nil, err
	}
	if target.VpcId != old.VpcId {
		return nil, errorf("InvalidParameterValue", "Route table %s and association %s belong to different networks", target.RouteTableId, c.p.get("AssociationId"))
	}
	a := old.Associations[i]
	a.RouteTableAssociationId = newID("rtbassoc")
	a.RouteTableId = target.RouteTableId
	old.Associations = slices.Delete(old.Associations, i, i+1)
	if err := c.put(tRTB, old); err != nil {
		return nil, err
	}
	if target.RouteTableId == old.RouteTableId {
		target = old
	}
	target.Associations = append(target.Associations, a)
	if err := c.put(tRTB, target); err != nil {
		return nil, err
	}
	return struct {
		NewAssociationId string `xml:"newAssociationId"`
		State            state  `xml:"associationState"`
	}{a.RouteTableAssociationId, a.AssociationState}, nil
}
