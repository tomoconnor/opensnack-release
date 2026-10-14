// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"slices"
	"strconv"
	"strings"
)

var naclActions = map[string]action{
	"CreateNetworkAcl":             createNetworkAcl,
	"DeleteNetworkAcl":             deleteNetworkAcl,
	"DescribeNetworkAcls":          describeNetworkAcls,
	"CreateNetworkAclEntry":        func(c *call) (any, error) { return putNaclEntry(c, false) },
	"ReplaceNetworkAclEntry":       func(c *call) (any, error) { return putNaclEntry(c, true) },
	"DeleteNetworkAclEntry":        deleteNetworkAclEntry,
	"ReplaceNetworkAclAssociation": replaceNetworkAclAssociation,
}

func (a *NetworkAcl) objectID() string { return a.NetworkAclId }
func (a *NetworkAcl) tagList() *[]Tag  { return &a.Tags }
func (a *NetworkAcl) filterValues() map[string][]string {
	m := map[string][]string{
		"network-acl-id": {a.NetworkAclId}, "vpc-id": {a.VpcId}, "default": {b2s(a.IsDefault)}, "owner-id": {a.OwnerId},
	}
	for _, k := range []string{"association.association-id", "association.network-acl-id", "association.subnet-id",
		"entry.cidr", "entry.ipv6-cidr", "entry.egress", "entry.rule-number", "entry.rule-action", "entry.protocol",
		"entry.port-range.from", "entry.port-range.to", "entry.icmp.code", "entry.icmp.type"} {
		m[k] = nil
	}
	for _, as := range a.Associations {
		m["association.association-id"] = append(m["association.association-id"], as.NetworkAclAssociationId)
		m["association.network-acl-id"] = append(m["association.network-acl-id"], as.NetworkAclId)
		m["association.subnet-id"] = append(m["association.subnet-id"], as.SubnetId)
	}
	for _, e := range a.Entries {
		m["entry.cidr"] = append(m["entry.cidr"], nonEmpty(e.CidrBlock)...)
		m["entry.ipv6-cidr"] = append(m["entry.ipv6-cidr"], nonEmpty(e.Ipv6CidrBlock)...)
		m["entry.egress"] = append(m["entry.egress"], b2s(e.Egress))
		m["entry.rule-number"] = append(m["entry.rule-number"], i2s(e.RuleNumber))
		m["entry.rule-action"] = append(m["entry.rule-action"], e.RuleAction)
		m["entry.protocol"] = append(m["entry.protocol"], e.Protocol)
		if e.PortRange != nil {
			m["entry.port-range.from"] = append(m["entry.port-range.from"], i2s(e.PortRange.From))
			m["entry.port-range.to"] = append(m["entry.port-range.to"], i2s(e.PortRange.To))
		}
		if e.IcmpTypeCode != nil {
			m["entry.icmp.code"] = append(m["entry.icmp.code"], i2s(e.IcmpTypeCode.Code))
			m["entry.icmp.type"] = append(m["entry.icmp.type"], i2s(e.IcmpTypeCode.Type))
		}
	}
	return m
}

const naclDefaultRule = 32767

// defaultNaclEntries are the entries a new ACL starts with: the default
// ACL allows everything (rule 100), every ACL ends with a deny-all.
func defaultNaclEntries(allowAll bool) []NaclEntry {
	var out []NaclEntry
	for _, egress := range []bool{true, false} {
		if allowAll {
			out = append(out, NaclEntry{RuleNumber: 100, Protocol: "-1", RuleAction: "allow", Egress: egress, CidrBlock: "0.0.0.0/0"})
		}
		out = append(out, NaclEntry{RuleNumber: naclDefaultRule, Protocol: "-1", RuleAction: "deny", Egress: egress, CidrBlock: "0.0.0.0/0"})
	}
	return out
}

func (c *call) defaultNacl(vpcID string) (*NetworkAcl, error) {
	acls, err := all[NetworkAcl](c, tNACL)
	if err != nil {
		return nil, err
	}
	for _, a := range acls {
		if a.VpcId == vpcID && a.IsDefault {
			return a, nil
		}
	}
	return nil, errorf("InvalidNetworkAclID.NotFound", "The VPC %s has no default network ACL", vpcID)
}

// fillAssociations derives an ACL's associations from the subnets.
func fillAssociations(a *NetworkAcl, subnets []*Subnet) {
	a.Associations = nil
	for _, s := range subnets {
		if s.NetworkAclId == a.NetworkAclId {
			a.Associations = append(a.Associations, NaclAssoc{s.NaclAssociation, a.NetworkAclId, s.SubnetId})
		}
	}
}

func (c *call) nacl(id string) (*NetworkAcl, error) {
	return must[NetworkAcl](c, tNACL, id, "InvalidNetworkAclID.NotFound")
}

func createNetworkAcl(c *call) (any, error) {
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	tags, err := c.tagSpec("network-acl")
	if err != nil {
		return nil, err
	}
	a := &NetworkAcl{NetworkAclId: newID("acl"), VpcId: vpc.VpcId, OwnerId: c.account, Entries: defaultNaclEntries(false), Tags: tags}
	if err := c.put(tNACL, a); err != nil {
		return nil, err
	}
	return struct {
		A *NetworkAcl `xml:"networkAcl"`
	}{a}, nil
}

func deleteNetworkAcl(c *call) (any, error) {
	a, err := c.nacl(c.p.get("NetworkAclId"))
	if err != nil {
		return nil, err
	}
	if a.IsDefault {
		return nil, errorf("InvalidParameterValue", "cannot delete default network ACL %s", a.NetworkAclId)
	}
	subnets, _ := all[Subnet](c, tSubnet)
	for _, s := range subnets {
		if s.NetworkAclId == a.NetworkAclId {
			return nil, errorf("DependencyViolation", "The networkAcl '%s' has dependencies and cannot be deleted.", a.NetworkAclId)
		}
	}
	return nil, c.remove(tNACL, a.NetworkAclId)
}

func describeNetworkAcls(c *call) (any, error) {
	acls, err := all[NetworkAcl](c, tNACL)
	if err != nil {
		return nil, err
	}
	subnets, _ := all[Subnet](c, tSubnet)
	if ids := c.p.list("NetworkAclId"); len(ids) > 0 {
		var picked []*NetworkAcl
		for _, id := range ids {
			i := slices.IndexFunc(acls, func(a *NetworkAcl) bool { return a.NetworkAclId == id })
			if i < 0 {
				return nil, notFound("InvalidNetworkAclID.NotFound", id)
			}
			picked = append(picked, acls[i])
		}
		acls = picked
	}
	var out []*NetworkAcl
	for _, a := range acls {
		fillAssociations(a, subnets)
		ok, err := matches(c.p.filters(), a)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, a)
		}
	}
	return struct {
		As []*NetworkAcl `xml:"networkAclSet>item"`
	}{out}, nil
}

// naclProtocol maps a protocol to the number NACL entries report.
func naclProtocol(p string) (string, error) {
	switch strings.ToLower(p) {
	case "tcp":
		return "6", nil
	case "udp":
		return "17", nil
	case "icmp":
		return "1", nil
	case "icmpv6":
		return "58", nil
	case "all", "-1":
		return "-1", nil
	}
	if n, err := strconv.Atoi(p); err == nil && n >= 0 && n <= 255 {
		return p, nil
	}
	return "", errorf("InvalidParameterValue", "Invalid value '%s' for protocol", p)
}

func putNaclEntry(c *call, replace bool) (any, error) {
	a, err := c.nacl(c.p.get("NetworkAclId"))
	if err != nil {
		return nil, err
	}
	num := c.p.integer("RuleNumber", 0)
	if num < 1 || num > 32766 {
		return nil, invalidParam("RuleNumber", c.p.get("RuleNumber"))
	}
	proto, err := naclProtocol(c.p.get("Protocol"))
	if err != nil {
		return nil, err
	}
	action := c.p.get("RuleAction")
	if action != "allow" && action != "deny" {
		return nil, invalidParam("RuleAction", action)
	}
	e := NaclEntry{RuleNumber: num, Protocol: proto, RuleAction: action, Egress: c.p.boolean("Egress"),
		CidrBlock: c.p.get("CidrBlock"), Ipv6CidrBlock: c.p.get("Ipv6CidrBlock")}
	if len(nonEmpty(e.CidrBlock, e.Ipv6CidrBlock)) != 1 {
		return nil, errorf("InvalidParameterCombination", "Exactly one of CidrBlock and Ipv6CidrBlock is required")
	}
	if e.CidrBlock != "" {
		if _, ok := parseCIDR(e.CidrBlock); !ok {
			return nil, invalidParam("CidrBlock", e.CidrBlock)
		}
	}
	switch proto {
	case "6", "17":
		from, to := c.p.optInt("PortRange.From"), c.p.optInt("PortRange.To")
		if from == nil || to == nil {
			return nil, missing("PortRange")
		}
		e.PortRange = &PortRange{*from, *to}
	case "1", "58":
		e.IcmpTypeCode = &IcmpCode{Code: c.p.integer("Icmp.Code", -1), Type: c.p.integer("Icmp.Type", -1)}
	}
	i := slices.IndexFunc(a.Entries, func(x NaclEntry) bool { return x.RuleNumber == num && x.Egress == e.Egress })
	switch {
	case replace && i < 0:
		return nil, errorf("InvalidNetworkAclEntry.NotFound", "The network acl entry identified by %d does not exist.", num)
	case !replace && i >= 0:
		return nil, errorf("NetworkAclEntryAlreadyExists", "The network acl entry identified by %d already exists.", num)
	case replace:
		a.Entries[i] = e
	default:
		a.Entries = append(a.Entries, e)
	}
	return nil, c.put(tNACL, a)
}

func deleteNetworkAclEntry(c *call) (any, error) {
	a, err := c.nacl(c.p.get("NetworkAclId"))
	if err != nil {
		return nil, err
	}
	num, egress := c.p.integer("RuleNumber", 0), c.p.boolean("Egress")
	i := slices.IndexFunc(a.Entries, func(x NaclEntry) bool { return x.RuleNumber == num && x.Egress == egress })
	if i < 0 || num == naclDefaultRule {
		return nil, errorf("InvalidNetworkAclEntry.NotFound", "The network acl entry identified by %d does not exist.", num)
	}
	a.Entries = slices.Delete(a.Entries, i, i+1)
	return nil, c.put(tNACL, a)
}

func replaceNetworkAclAssociation(c *call) (any, error) {
	assocID := c.p.get("AssociationId")
	a, err := c.nacl(c.p.get("NetworkAclId"))
	if err != nil {
		return nil, err
	}
	subnets, err := all[Subnet](c, tSubnet)
	if err != nil {
		return nil, err
	}
	for _, s := range subnets {
		if s.NaclAssociation != assocID {
			continue
		}
		if s.VpcId != a.VpcId {
			return nil, errorf("InvalidParameterValue", "Network ACL %s and subnet %s belong to different networks", a.NetworkAclId, s.SubnetId)
		}
		s.NetworkAclId, s.NaclAssociation = a.NetworkAclId, newID("aclassoc")
		if err := c.put(tSubnet, s); err != nil {
			return nil, err
		}
		return struct {
			NewAssociationId string `xml:"newAssociationId"`
		}{s.NaclAssociation}, nil
	}
	return nil, errorf("InvalidAssociationID.NotFound", "The association ID '%s' does not exist", assocID)
}
