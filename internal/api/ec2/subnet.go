// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"net/netip"
	"slices"
	"strings"
)

var subnetActions = map[string]action{
	"CreateSubnet":          createSubnet,
	"DeleteSubnet":          deleteSubnet,
	"DescribeSubnets":       describeSubnets,
	"ModifySubnetAttribute": modifySubnetAttribute,
}

func (s *Subnet) objectID() string { return s.SubnetId }
func (s *Subnet) tagList() *[]Tag  { return &s.Tags }
func (s *Subnet) filterValues() map[string][]string {
	return map[string][]string{
		"subnet-id": {s.SubnetId}, "vpc-id": {s.VpcId}, "cidr-block": {s.CidrBlock}, "cidr": {s.CidrBlock},
		"cidrBlock": {s.CidrBlock}, "availability-zone": {s.AvailabilityZone}, "availabilityZone": {s.AvailabilityZone},
		"availability-zone-id": {s.AvailabilityZoneId}, "availabilityZoneId": {s.AvailabilityZoneId},
		"default-for-az": {b2s(s.DefaultForAz)}, "defaultForAz": {b2s(s.DefaultForAz)}, "state": {s.State},
		"owner-id": {s.OwnerId}, "subnet-arn": {s.SubnetArn}, "map-public-ip-on-launch": {b2s(s.MapPublicIpOnLaunch)},
		"available-ip-address-count": {i2s(s.AvailableIpAddressCount)}, "ipv6-native": {b2s(s.Ipv6Native)},
		"enable-dns64": {b2s(s.EnableDns64)}, "map-customer-owned-ip-on-launch": {b2s(s.MapCustomerOwnedIpOnLaunch)},
		"assign-ipv6-address-on-creation":             {b2s(s.AssignIpv6AddressOnCreation)},
		"ipv6-cidr-block-association.ipv6-cidr-block": nil, "ipv6-cidr-block-association.association-id": nil,
		"ipv6-cidr-block-association.state": nil, "outpost-arn": nil,
		"private-dns-name-options-on-launch.hostname-type": {s.PrivateDnsNameOptionsOnLaunch.HostnameType},
	}
}

func (s *Subnet) prefix() netip.Prefix {
	p, _ := parseCIDR(s.CidrBlock)
	return p
}

func (c *call) subnet(id string) (*Subnet, error) {
	return must[Subnet](c, tSubnet, id, "InvalidSubnetID.NotFound")
}

// checkSubnetCIDR applies EC2's rules for a new subnet in vpc: a canonical
// block between /16 and /28, inside one of the VPC's CIDRs, overlapping no
// other subnet of the VPC.
func (c *call) checkSubnetCIDR(vpc *Vpc, cidr string) error {
	p, ok := parseCIDR(cidr)
	if !ok {
		return errorf("InvalidParameterValue", "Value (%s) for parameter cidrBlock is invalid. This is not a valid CIDR block.", cidr)
	}
	if p.Bits() < 16 || p.Bits() > 28 {
		return errorf("InvalidSubnet.Range", "The CIDR '%s' is invalid.", cidr)
	}
	if !slices.ContainsFunc(vpc.cidrs(), func(v netip.Prefix) bool { return contains(v, p) }) {
		return errorf("InvalidSubnet.Range", "The CIDR '%s' is invalid.", cidr)
	}
	subnets, err := all[Subnet](c, tSubnet)
	if err != nil {
		return err
	}
	for _, s := range subnets {
		if s.VpcId == vpc.VpcId && overlaps(s.prefix(), p) {
			return errorf("InvalidSubnet.Conflict", "The CIDR '%s' conflicts with another subnet", cidr)
		}
	}
	return nil
}

func (c *call) pickZone(name, id string) (azInfo, error) {
	if name == "" && id == "" {
		return zones(c.region)[0], nil
	}
	v := name
	if v == "" {
		v = id
	}
	z, ok := zoneByNameOrID(c.region, v)
	if !ok || (name != "" && z.Name != name) || (id != "" && z.ID != id) {
		param := "availabilityZone"
		if name == "" {
			param = "availabilityZoneId"
		}
		return z, errorf("InvalidParameterValue", "Value (%s) for parameter %s is invalid. Subnets can currently only be created in the following availability zones: %s.",
			v, param, strings.Join(zoneNames(c.region), ", "))
	}
	return z, nil
}

func createSubnet(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	cidr := c.p.get("CidrBlock")
	if cidr == "" {
		if c.p.has("Ipv6CidrBlock") || c.p.boolean("Ipv6Native") {
			return nil, errorf("UnsupportedOperation", "IPv6 subnets are not supported")
		}
		return nil, missing("CidrBlock")
	}
	z, err := c.pickZone(c.p.get("AvailabilityZone"), c.p.get("AvailabilityZoneId"))
	if err != nil {
		return nil, err
	}
	if err := c.checkSubnetCIDR(vpc, cidr); err != nil {
		return nil, err
	}
	tags, err := c.tagSpec("subnet")
	if err != nil {
		return nil, err
	}
	acl, err := c.defaultNacl(vpc.VpcId)
	if err != nil {
		return nil, err
	}
	s := &Subnet{
		SubnetId: newID("subnet"), VpcId: vpc.VpcId, OwnerId: c.account, State: "available", CidrBlock: cidr,
		AvailabilityZone: z.Name, AvailabilityZoneId: z.ID, Tags: tags,
		PrivateDnsNameOptionsOnLaunch: PrivateDnsOpts{HostnameType: "ip-name"},
		NetworkAclId:                  acl.NetworkAclId, NaclAssociation: newID("aclassoc"),
	}
	s.SubnetArn = c.arn("subnet/" + s.SubnetId)
	if err := c.put(tSubnet, s); err != nil {
		return nil, err
	}
	c.fillSubnet(s)
	return struct {
		Subnet *Subnet `xml:"subnet"`
	}{s}, nil
}

// fillSubnet sets the derived fields.
func (c *call) fillSubnet(s *Subnet) {
	used := c.usedIPs(s.SubnetId)
	s.AvailableIpAddressCount = subnetSize(s.prefix()) - reservedInSubnet - len(used)
}

func describeSubnets(c *call) (any, error) {
	// available-ip-address-count is derived, so fill before filtering.
	items, err := all[Subnet](c, tSubnet)
	if err != nil {
		return nil, err
	}
	ids := c.p.list("SubnetId")
	if len(ids) > 0 {
		var picked []*Subnet
		for _, id := range ids {
			i := slices.IndexFunc(items, func(s *Subnet) bool { return s.SubnetId == id })
			if i < 0 {
				return nil, notFound("InvalidSubnetID.NotFound", id)
			}
			picked = append(picked, items[i])
		}
		items = picked
	}
	var out []*Subnet
	for _, s := range items {
		c.fillSubnet(s)
		ok, err := matches(c.p.filters(), s)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, s)
		}
	}
	return struct {
		Subnets []*Subnet `xml:"subnetSet>item"`
	}{out}, nil
}

func deleteSubnet(c *call) (any, error) {
	s, err := c.subnet(c.p.get("SubnetId"))
	if err != nil {
		return nil, err
	}
	enis, err := all[NetworkInterface](c, tENI)
	if err != nil {
		return nil, err
	}
	for _, e := range enis {
		if e.SubnetId == s.SubnetId {
			return nil, errorf("DependencyViolation", "The subnet '%s' has dependencies and cannot be deleted.", s.SubnetId)
		}
	}
	// Explicit route table associations go with the subnet.
	if err := c.eachRouteTable(s.VpcId, func(rt *RouteTable) bool {
		n := len(rt.Associations)
		rt.Associations = slices.DeleteFunc(rt.Associations, func(a RTAssoc) bool { return a.SubnetId == s.SubnetId })
		return len(rt.Associations) != n
	}); err != nil {
		return nil, err
	}
	return nil, c.remove(tSubnet, s.SubnetId)
}

func modifySubnetAttribute(c *call) (any, error) {
	s, err := c.subnet(c.p.get("SubnetId"))
	if err != nil {
		return nil, err
	}
	setBool := func(param string, dst *bool) {
		if v := c.p.optBool(param + ".Value"); v != nil {
			*dst = *v
		}
	}
	setBool("MapPublicIpOnLaunch", &s.MapPublicIpOnLaunch)
	setBool("MapCustomerOwnedIpOnLaunch", &s.MapCustomerOwnedIpOnLaunch)
	setBool("EnableDns64", &s.EnableDns64)
	setBool("EnableResourceNameDnsARecordOnLaunch", &s.PrivateDnsNameOptionsOnLaunch.EnableResourceNameDnsARecord)
	setBool("EnableResourceNameDnsAAAARecordOnLaunch", &s.PrivateDnsNameOptionsOnLaunch.EnableResourceNameDnsAAAARecord)
	if v := c.p.optBool("AssignIpv6AddressOnCreation.Value"); v != nil {
		if *v {
			return nil, errorf("InvalidParameterValue", "The subnet %s has no IPv6 CIDR block", s.SubnetId)
		}
		s.AssignIpv6AddressOnCreation = false
	}
	if v := c.p.get("PrivateDnsHostnameTypeOnLaunch"); v != "" {
		if v != "ip-name" && v != "resource-name" {
			return nil, invalidParam("PrivateDnsHostnameTypeOnLaunch", v)
		}
		s.PrivateDnsNameOptionsOnLaunch.HostnameType = v
	}
	if v := c.p.optInt("EnableLniAtDeviceIndex"); v != nil {
		s.EnableLniAtDeviceIndex = v
	}
	return nil, c.put(tSubnet, s)
}
