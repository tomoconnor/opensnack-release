// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"net/netip"
	"slices"
)

var vpcActions = map[string]action{
	"CreateVpc":                createVpc,
	"DeleteVpc":                deleteVpc,
	"DescribeVpcs":             describeVpcs,
	"ModifyVpcAttribute":       modifyVpcAttribute,
	"DescribeVpcAttribute":     describeVpcAttribute,
	"AssociateVpcCidrBlock":    associateVpcCidrBlock,
	"DisassociateVpcCidrBlock": disassociateVpcCidrBlock,
	"ModifyVpcTenancy":         modifyVpcTenancy,
	"CreateDefaultVpc":         createDefaultVpc,
}

func (v *Vpc) objectID() string { return v.VpcId }
func (v *Vpc) tagList() *[]Tag  { return &v.Tags }
func (v *Vpc) filterValues() map[string][]string {
	m := map[string][]string{
		"vpc-id": {v.VpcId}, "cidr": {v.CidrBlock}, "dhcp-options-id": {v.DhcpOptionsId},
		"is-default": {b2s(v.IsDefault)}, "isDefault": {b2s(v.IsDefault)}, "owner-id": {v.OwnerId}, "state": {v.State},
		"instance-tenancy": {v.InstanceTenancy},
		"ipv6-cidr-block-association.ipv6-cidr-block": nil, "ipv6-cidr-block-association.association-id": nil,
		"ipv6-cidr-block-association.state": nil, "ipv6-cidr-block-association.ipv6-pool": nil,
	}
	for _, a := range v.CidrBlockAssociationSet {
		m["cidr-block-association.cidr-block"] = append(m["cidr-block-association.cidr-block"], a.CidrBlock)
		m["cidr-block-association.association-id"] = append(m["cidr-block-association.association-id"], a.AssociationId)
		m["cidr-block-association.state"] = append(m["cidr-block-association.state"], a.CidrBlockState.State)
	}
	return m
}

// cidrs returns the VPC's associated IPv4 CIDR blocks.
func (v *Vpc) cidrs() []netip.Prefix {
	var out []netip.Prefix
	for _, a := range v.CidrBlockAssociationSet {
		if p, ok := parseCIDR(a.CidrBlock); ok && a.CidrBlockState.State == "associated" {
			out = append(out, p)
		}
	}
	return out
}

// checkVpcCIDR validates a VPC CIDR block: canonical, between /16 and /28.
func checkVpcCIDR(cidr string) (netip.Prefix, error) {
	p, ok := parseCIDR(cidr)
	if !ok {
		return p, errorf("InvalidParameterValue", "Value (%s) for parameter cidrBlock is invalid. This is not a valid CIDR block.", cidr)
	}
	if p.Bits() < 16 || p.Bits() > 28 {
		return p, errorf("InvalidVpc.Range", "The CIDR '%s' is invalid.", cidr)
	}
	return p, nil
}

// idGen makes IDs: random for user-created resources, derived from a seed
// for the generated default VPC.
type idGen func(prefix, name string) string

func randomIDs(prefix, _ string) string { return newID(prefix) }

// newVpc builds a VPC with the security group, main route table and network
// ACL AWS creates alongside it.
func (c *call) newVpc(cidr string, isDefault bool, ids idGen) (*Vpc, *SecurityGroup, *RouteTable, *NetworkAcl) {
	vpc := &Vpc{
		VpcId: ids("vpc", "vpc"), OwnerId: c.account, State: "available", CidrBlock: cidr,
		CidrBlockAssociationSet: []VpcCidrAssoc{{AssociationId: ids("vpc-cidr-assoc", "assoc"), CidrBlock: cidr, CidrBlockState: state{"associated"}}},
		DhcpOptionsId:           c.dhcpOptionsID(), InstanceTenancy: "default", IsDefault: isDefault,
		EnableDnsSupport: true, EnableDnsHostnames: isDefault,
	}
	sg := &SecurityGroup{
		GroupId: ids("sg", "sg"), GroupName: "default", Description: "default VPC security group",
		VpcId: vpc.VpcId, OwnerId: c.account,
	}
	sg.SecurityGroupArn = c.arn("security-group/" + sg.GroupId)
	sg.Rules = []SgRule{
		c.newRule(sg, ids("sgr", "sgr-in"), false, "-1", -1, -1, ruleSource{group: sg.GroupId}, ""),
		c.newRule(sg, ids("sgr", "sgr-out"), true, "-1", -1, -1, ruleSource{cidr4: "0.0.0.0/0"}, ""),
	}
	rtb := &RouteTable{
		RouteTableId: ids("rtb", "rtb"), VpcId: vpc.VpcId, OwnerId: c.account,
		Routes: []Route{localRoute(cidr)},
	}
	rtb.Associations = []RTAssoc{{RouteTableAssociationId: ids("rtbassoc", "rtbassoc"), RouteTableId: rtb.RouteTableId, Main: true, AssociationState: state{"associated"}}}
	acl := &NetworkAcl{
		NetworkAclId: ids("acl", "acl"), VpcId: vpc.VpcId, OwnerId: c.account, IsDefault: true,
		Entries: defaultNaclEntries(true),
	}
	return vpc, sg, rtb, acl
}

func localRoute(cidr string) Route {
	return Route{DestinationCidrBlock: cidr, GatewayId: "local", State: "active", Origin: "CreateRouteTable"}
}

func createVpc(c *call) (any, error) {
	cidr := c.p.get("CidrBlock")
	if cidr == "" {
		if c.p.has("Ipv4IpamPoolId") {
			return nil, errorf("UnsupportedOperation", "IPAM pools are not supported")
		}
		return nil, missing("CidrBlock")
	}
	if _, err := checkVpcCIDR(cidr); err != nil {
		return nil, err
	}
	tags, err := c.tagSpec("vpc")
	if err != nil {
		return nil, err
	}
	tenancy := c.p.get("InstanceTenancy")
	if tenancy == "" {
		tenancy = "default"
	}
	if tenancy != "default" && tenancy != "dedicated" {
		return nil, invalidParam("instanceTenancy", tenancy)
	}
	vpc, sg, rtb, acl := c.newVpc(cidr, false, randomIDs)
	vpc.InstanceTenancy = tenancy
	vpc.Tags = tags
	for _, step := range []struct {
		typ string
		o   object
	}{{tVpc, vpc}, {tSG, sg}, {tRTB, rtb}, {tNACL, acl}} {
		if err := c.put(step.typ, step.o); err != nil {
			return nil, err
		}
	}
	return struct {
		Vpc *Vpc `xml:"vpc"`
	}{vpc}, nil
}

func describeVpcs(c *call) (any, error) {
	vpcs, err := describe[Vpc](c, tVpc, c.p.list("VpcId"), "InvalidVpcID.NotFound")
	if err != nil {
		return nil, err
	}
	return struct {
		Vpcs []*Vpc `xml:"vpcSet>item"`
	}{vpcs}, nil
}

func (c *call) vpc(id string) (*Vpc, error) {
	return must[Vpc](c, tVpc, id, "InvalidVpcID.NotFound")
}

func deleteVpc(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	id := vpc.VpcId
	dep := func(what, depID string) error {
		return errorf("DependencyViolation", "The vpc '%s' has dependencies and cannot be deleted. (%s %s)", id, what, depID)
	}
	subnets, _ := all[Subnet](c, tSubnet)
	for _, s := range subnets {
		if s.VpcId == id {
			return nil, dep("subnet", s.SubnetId)
		}
	}
	igws, _ := all[InternetGateway](c, tIGW)
	for _, g := range igws {
		for _, a := range g.Attachments {
			if a.VpcId == id {
				return nil, dep("internet gateway", g.InternetGatewayId)
			}
		}
	}
	endpoints, _ := all[VpcEndpoint](c, tEndpoint)
	for _, e := range endpoints {
		if e.VpcId == id {
			return nil, dep("vpc endpoint", e.VpcEndpointId)
		}
	}
	var owned []struct {
		typ, id string
	}
	sgs, _ := all[SecurityGroup](c, tSG)
	for _, sg := range sgs {
		if sg.VpcId == id {
			if sg.GroupName != "default" {
				return nil, dep("security group", sg.GroupId)
			}
			owned = append(owned, struct{ typ, id string }{tSG, sg.GroupId})
		}
	}
	rtbs, _ := all[RouteTable](c, tRTB)
	for _, rt := range rtbs {
		if rt.VpcId == id {
			if !rt.isMain() {
				return nil, dep("route table", rt.RouteTableId)
			}
			owned = append(owned, struct{ typ, id string }{tRTB, rt.RouteTableId})
		}
	}
	acls, _ := all[NetworkAcl](c, tNACL)
	for _, a := range acls {
		if a.VpcId == id {
			if !a.IsDefault {
				return nil, dep("network ACL", a.NetworkAclId)
			}
			owned = append(owned, struct{ typ, id string }{tNACL, a.NetworkAclId})
		}
	}
	for _, o := range owned {
		if err := c.remove(o.typ, o.id); err != nil {
			return nil, err
		}
	}
	return nil, c.remove(tVpc, id)
}

func modifyVpcAttribute(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	set := 0
	if v := c.p.optBool("EnableDnsSupport.Value"); v != nil {
		vpc.EnableDnsSupport = *v
		set++
	}
	if v := c.p.optBool("EnableDnsHostnames.Value"); v != nil {
		vpc.EnableDnsHostnames = *v
		set++
	}
	if v := c.p.optBool("EnableNetworkAddressUsageMetrics.Value"); v != nil {
		vpc.EnableNetworkAddressUsageMetrics = *v
		set++
	}
	if set != 1 {
		return nil, errorf("InvalidParameterCombination", "Exactly one attribute must be specified")
	}
	return nil, c.put(tVpc, vpc)
}

func describeVpcAttribute(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	type val struct {
		Value bool `xml:"value"`
	}
	out := struct {
		VpcId                            string `xml:"vpcId"`
		EnableDnsSupport                 *val   `xml:"enableDnsSupport,omitempty"`
		EnableDnsHostnames               *val   `xml:"enableDnsHostnames,omitempty"`
		EnableNetworkAddressUsageMetrics *val   `xml:"enableNetworkAddressUsageMetrics,omitempty"`
	}{VpcId: vpc.VpcId}
	switch a := c.p.get("Attribute"); a {
	case "enableDnsSupport":
		out.EnableDnsSupport = &val{vpc.EnableDnsSupport}
	case "enableDnsHostnames":
		out.EnableDnsHostnames = &val{vpc.EnableDnsHostnames}
	case "enableNetworkAddressUsageMetrics":
		out.EnableNetworkAddressUsageMetrics = &val{vpc.EnableNetworkAddressUsageMetrics}
	default:
		return nil, invalidParam("attribute", a)
	}
	return out, nil
}

func modifyVpcTenancy(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	if t := c.p.get("InstanceTenancy"); t != "default" {
		return nil, invalidParam("InstanceTenancy", t)
	}
	vpc.InstanceTenancy = "default"
	return struct {
		Return bool `xml:"returnValue"`
	}{true}, c.put(tVpc, vpc)
}

const maxVpcCidrs = 5

func associateVpcCidrBlock(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	cidr := c.p.get("CidrBlock")
	if cidr == "" {
		return nil, errorf("UnsupportedOperation", "Only IPv4 CIDR blocks given explicitly are supported")
	}
	p, err := checkVpcCIDR(cidr)
	if err != nil {
		return nil, err
	}
	existing := vpc.cidrs()
	if len(existing) >= maxVpcCidrs {
		return nil, errorf("CidrLimitExceeded", "This network '%s' has met its maximum number of allowed CIDRs: %d", vpc.VpcId, maxVpcCidrs)
	}
	for _, e := range existing {
		if overlaps(e, p) {
			return nil, errorf("InvalidVpc.Range", "The CIDR '%s' overlaps with the existing CIDR '%s' of the VPC.", cidr, e)
		}
	}
	assoc := VpcCidrAssoc{AssociationId: newID("vpc-cidr-assoc"), CidrBlock: cidr, CidrBlockState: state{"associated"}}
	vpc.CidrBlockAssociationSet = append(vpc.CidrBlockAssociationSet, assoc)
	if err := c.put(tVpc, vpc); err != nil {
		return nil, err
	}
	if err := c.eachRouteTable(vpc.VpcId, func(rt *RouteTable) bool {
		rt.Routes = append(rt.Routes, localRoute(cidr))
		return true
	}); err != nil {
		return nil, err
	}
	return struct {
		VpcId string       `xml:"vpcId"`
		Assoc VpcCidrAssoc `xml:"cidrBlockAssociation"`
	}{vpc.VpcId, assoc}, nil
}

func disassociateVpcCidrBlock(c *call) (any, error) {
	id := c.p.get("AssociationId")
	vpcs, err := all[Vpc](c, tVpc)
	if err != nil {
		return nil, err
	}
	for _, vpc := range vpcs {
		i := slices.IndexFunc(vpc.CidrBlockAssociationSet, func(a VpcCidrAssoc) bool { return a.AssociationId == id })
		if i < 0 {
			continue
		}
		assoc := vpc.CidrBlockAssociationSet[i]
		if assoc.CidrBlock == vpc.CidrBlock {
			return nil, errorf("OperationNotPermitted", "The vpc CIDR block with association ID %s may not be disassociated. It is the primary IPv4 CIDR block of the VPC", id)
		}
		p, _ := parseCIDR(assoc.CidrBlock)
		subnets, _ := all[Subnet](c, tSubnet)
		for _, s := range subnets {
			if sp, ok := parseCIDR(s.CidrBlock); ok && s.VpcId == vpc.VpcId && overlaps(p, sp) {
				return nil, errorf("InvalidCidrBlock.InUse", "The vpc CIDR block %s is in use by subnet %s", assoc.CidrBlock, s.SubnetId)
			}
		}
		vpc.CidrBlockAssociationSet = slices.Delete(vpc.CidrBlockAssociationSet, i, i+1)
		if err := c.put(tVpc, vpc); err != nil {
			return nil, err
		}
		if err := c.eachRouteTable(vpc.VpcId, func(rt *RouteTable) bool {
			rt.Routes = slices.DeleteFunc(rt.Routes, func(r Route) bool {
				return r.GatewayId == "local" && r.DestinationCidrBlock == assoc.CidrBlock
			})
			return true
		}); err != nil {
			return nil, err
		}
		assoc.CidrBlockState.State = "disassociating"
		return struct {
			VpcId string       `xml:"vpcId"`
			Assoc VpcCidrAssoc `xml:"cidrBlockAssociation"`
		}{vpc.VpcId, assoc}, nil
	}
	return nil, errorf("InvalidVpcCidrBlockAssociationID.NotFound", "The vpc CIDR block association ID '%s' does not exist", id)
}

// eachRouteTable applies fn to the VPC's route tables, saving those for
// which it returns true.
func (c *call) eachRouteTable(vpcID string, fn func(*RouteTable) bool) error {
	rtbs, err := all[RouteTable](c, tRTB)
	if err != nil {
		return err
	}
	for _, rt := range rtbs {
		if rt.VpcId == vpcID && fn(rt) {
			if err := c.put(tRTB, rt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *call) arn(resource string) string {
	return "arn:aws:ec2:" + c.region + ":" + c.account + ":" + resource
}
