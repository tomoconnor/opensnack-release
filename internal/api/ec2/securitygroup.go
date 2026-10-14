// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

var securityGroupActions = map[string]action{
	"CreateSecurityGroup":                        createSecurityGroup,
	"DeleteSecurityGroup":                        deleteSecurityGroup,
	"DescribeSecurityGroups":                     describeSecurityGroups,
	"AuthorizeSecurityGroupIngress":              func(c *call) (any, error) { return authorize(c, false) },
	"AuthorizeSecurityGroupEgress":               func(c *call) (any, error) { return authorize(c, true) },
	"RevokeSecurityGroupIngress":                 func(c *call) (any, error) { return revoke(c, false) },
	"RevokeSecurityGroupEgress":                  func(c *call) (any, error) { return revoke(c, true) },
	"DescribeSecurityGroupRules":                 describeSecurityGroupRules,
	"ModifySecurityGroupRules":                   modifySecurityGroupRules,
	"UpdateSecurityGroupRuleDescriptionsIngress": func(c *call) (any, error) { return updateRuleDescriptions(c, false) },
	"UpdateSecurityGroupRuleDescriptionsEgress":  func(c *call) (any, error) { return updateRuleDescriptions(c, true) },
}

func (g *SecurityGroup) objectID() string { return g.GroupId }
func (g *SecurityGroup) tagList() *[]Tag  { return &g.Tags }
func (g *SecurityGroup) filterValues() map[string][]string {
	m := map[string][]string{
		"group-id": {g.GroupId}, "group-name": {g.GroupName}, "vpc-id": {g.VpcId}, "owner-id": {g.OwnerId},
		"description": {g.Description},
	}
	for _, prefix := range []string{"ip-permission.", "egress.ip-permission."} {
		for _, k := range []string{"cidr", "from-port", "to-port", "protocol", "group-id", "group-name", "ipv6-cidr", "prefix-list-id", "user-id"} {
			m[prefix+k] = nil
		}
	}
	for _, r := range g.Rules {
		prefix := "ip-permission."
		if r.IsEgress {
			prefix = "egress.ip-permission."
		}
		add := func(k, v string) {
			if v != "" {
				m[prefix+k] = append(m[prefix+k], v)
			}
		}
		add("cidr", r.CidrIpv4)
		add("ipv6-cidr", r.CidrIpv6)
		add("prefix-list-id", r.PrefixListId)
		add("protocol", r.IpProtocol)
		add("from-port", i2s(r.FromPort))
		add("to-port", i2s(r.ToPort))
		if r.ReferencedGroupInfo != nil {
			add("group-id", r.ReferencedGroupInfo.GroupId)
			add("user-id", r.ReferencedGroupInfo.UserId)
		}
	}
	return m
}

// ruleSource is the one peer of a rule.
type ruleSource struct {
	cidr4, cidr6, prefixList, group string
}

func (c *call) newRule(sg *SecurityGroup, id string, egress bool, proto string, from, to int, src ruleSource, desc string) SgRule {
	r := SgRule{
		SecurityGroupRuleId: id, GroupId: sg.GroupId, GroupOwnerId: sg.OwnerId, IsEgress: egress,
		IpProtocol: proto, FromPort: from, ToPort: to, CidrIpv4: src.cidr4, CidrIpv6: src.cidr6,
		PrefixListId: src.prefixList, Description: desc,
	}
	r.SecurityGroupRuleArn = c.arn("security-group-rule/" + id)
	if src.group != "" {
		r.ReferencedGroupInfo = &RefGroup{GroupId: src.group, UserId: c.account}
	}
	return r
}

// sameRule compares what identifies a rule: direction, protocol, ports and
// peer. Descriptions don't count.
func sameRule(a, b SgRule) bool {
	ga, gb := "", ""
	if a.ReferencedGroupInfo != nil {
		ga = a.ReferencedGroupInfo.GroupId
	}
	if b.ReferencedGroupInfo != nil {
		gb = b.ReferencedGroupInfo.GroupId
	}
	return a.IsEgress == b.IsEgress && a.IpProtocol == b.IpProtocol && a.FromPort == b.FromPort &&
		a.ToPort == b.ToPort && a.CidrIpv4 == b.CidrIpv4 && a.CidrIpv6 == b.CidrIpv6 &&
		a.PrefixListId == b.PrefixListId && ga == gb
}

func (r SgRule) String() string {
	peer := r.CidrIpv4 + r.CidrIpv6 + r.PrefixListId
	if r.ReferencedGroupInfo != nil {
		peer = r.ReferencedGroupInfo.GroupId
	}
	proto := strings.ToUpper(r.IpProtocol)
	if r.IpProtocol == "-1" {
		proto = "ALL"
	}
	return fmt.Sprintf("peer: %s, %s, from port: %d, to port: %d, ALLOW", peer, proto, r.FromPort, r.ToPort)
}

// normalizeProtocol maps protocol names and numbers the way EC2 reports
// them: tcp, udp, icmp, icmpv6 by name, -1 for all, other numbers as is.
func normalizeProtocol(p string) (string, error) {
	switch strings.ToLower(p) {
	case "tcp", "6":
		return "tcp", nil
	case "udp", "17":
		return "udp", nil
	case "icmp", "1":
		return "icmp", nil
	case "icmpv6", "58":
		return "icmpv6", nil
	case "-1", "all":
		return "-1", nil
	}
	if n, err := strconv.Atoi(p); err == nil && n >= 0 && n <= 255 {
		return p, nil
	}
	return "", errorf("InvalidParameterValue", "Invalid value '%s' for IP protocol. Unknown protocol.", p)
}

// ports validates a rule's port range for its protocol.
func ports(proto string, from, to *int) (int, int, error) {
	switch proto {
	case "tcp", "udp":
		if from == nil || to == nil {
			return 0, 0, errorf("InvalidParameterValue", "Invalid value 'Must specify both from and to ports with TCP/UDP.' for portRange")
		}
		if *from < 0 || *to > 65535 || *from > *to {
			return 0, 0, errorf("InvalidParameterValue", "Invalid TCP/UDP port range %d-%d", *from, *to)
		}
		return *from, *to, nil
	case "icmp", "icmpv6":
		f, t := -1, -1
		if from != nil {
			f = *from
		}
		if to != nil {
			t = *to
		}
		return f, t, nil
	}
	return -1, -1, nil
}

// parsePermissions expands IpPermissions.N into one rule per peer.
func (c *call) parsePermissions(sg *SecurityGroup, egress bool) ([]SgRule, error) {
	var out []SgRule
	perms := c.p.sub("IpPermissions")
	if len(perms) == 0 && c.p.get("IpProtocol") != "" {
		// The legacy flat form.
		perms = []params{params(c.p)}
	}
	for _, perm := range perms {
		proto, err := normalizeProtocol(perm.get("IpProtocol"))
		if err != nil {
			return nil, err
		}
		from, to, err := ports(proto, perm.optInt("FromPort"), perm.optInt("ToPort"))
		if err != nil {
			return nil, err
		}
		add := func(src ruleSource, desc string) {
			out = append(out, c.newRule(sg, newID("sgr"), egress, proto, from, to, src, desc))
		}
		for _, r := range perm.sub("IpRanges") {
			cidr := r.get("CidrIp")
			if p, err := netip.ParsePrefix(cidr); err != nil || !p.Addr().Is4() {
				return nil, errorf("InvalidParameterValue", "CIDR block %s is malformed", cidr)
			}
			add(ruleSource{cidr4: cidr}, r.get("Description"))
		}
		if cidr := perm.get("CidrIp"); cidr != "" {
			add(ruleSource{cidr4: cidr}, "")
		}
		for _, r := range perm.sub("Ipv6Ranges") {
			cidr := r.get("CidrIpv6")
			if p, err := netip.ParsePrefix(cidr); err != nil || !p.Addr().Is6() {
				return nil, errorf("InvalidParameterValue", "CIDR block %s is malformed", cidr)
			}
			add(ruleSource{cidr6: cidr}, r.get("Description"))
		}
		for _, r := range perm.sub("PrefixListIds") {
			add(ruleSource{prefixList: r.get("PrefixListId")}, r.get("Description"))
		}
		for _, g := range perm.sub("Groups") {
			id := g.get("GroupId")
			if id == "" && g.get("GroupName") != "" {
				ref, err := c.securityGroupByName(g.get("GroupName"), sg.VpcId)
				if err != nil {
					return nil, err
				}
				id = ref.GroupId
			}
			if id != sg.GroupId {
				if _, err := c.securityGroup(id); err != nil {
					return nil, err
				}
			}
			add(ruleSource{group: id}, g.get("Description"))
		}
	}
	return out, nil
}

func (c *call) securityGroup(id string) (*SecurityGroup, error) {
	return must[SecurityGroup](c, tSG, id, "InvalidGroup.NotFound")
}

func (c *call) securityGroupByName(name, vpcID string) (*SecurityGroup, error) {
	sgs, err := all[SecurityGroup](c, tSG)
	if err != nil {
		return nil, err
	}
	if vpcID == "" {
		vpcID = c.defaultVpcID()
	}
	for _, g := range sgs {
		if g.GroupName == name && g.VpcId == vpcID {
			return g, nil
		}
	}
	return nil, errorf("InvalidGroup.NotFound", "The security group '%s' does not exist in VPC '%s'", name, vpcID)
}

// groupParam finds the group named by GroupId or GroupName.
func (c *call) groupParam() (*SecurityGroup, error) {
	if id := c.p.get("GroupId"); id != "" {
		return c.securityGroup(id)
	}
	if name := c.p.get("GroupName"); name != "" {
		return c.securityGroupByName(name, "")
	}
	return nil, missing("GroupId")
}

func createSecurityGroup(c *call) (any, error) {
	name, desc := c.p.get("GroupName"), c.p.get("GroupDescription")
	if name == "" {
		return nil, missing("GroupName")
	}
	if desc == "" {
		return nil, missing("GroupDescription")
	}
	if name == "default" {
		return nil, errorf("InvalidParameterValue", "Cannot use reserved security group name: default")
	}
	if strings.HasPrefix(strings.ToLower(name), "sg-") {
		return nil, errorf("InvalidParameterValue", "Group names may not be in the format sg-*")
	}
	if len(name) > 255 || len(desc) > 255 {
		return nil, errorf("InvalidParameterValue", "Group name and description are limited to 255 characters")
	}
	vpcID := c.p.get("VpcId")
	if vpcID == "" {
		vpcID = c.defaultVpcID()
	}
	if _, err := c.vpc(vpcID); err != nil {
		return nil, err
	}
	if _, err := c.securityGroupByName(name, vpcID); err == nil {
		return nil, errorf("InvalidGroup.Duplicate", "The security group '%s' already exists for VPC '%s'", name, vpcID)
	}
	tags, err := c.tagSpec("security-group")
	if err != nil {
		return nil, err
	}
	sg := &SecurityGroup{GroupId: newID("sg"), GroupName: name, Description: desc, VpcId: vpcID, OwnerId: c.account, Tags: tags}
	sg.SecurityGroupArn = c.arn("security-group/" + sg.GroupId)
	sg.Rules = []SgRule{c.newRule(sg, newID("sgr"), true, "-1", -1, -1, ruleSource{cidr4: "0.0.0.0/0"}, "")}
	if err := c.put(tSG, sg); err != nil {
		return nil, err
	}
	return struct {
		GroupId string `xml:"groupId"`
		Tags    []Tag  `xml:"tagSet>item,omitempty"`
		Arn     string `xml:"securityGroupArn"`
	}{sg.GroupId, sg.Tags, sg.SecurityGroupArn}, nil
}

func deleteSecurityGroup(c *call) (any, error) {
	sg, err := c.groupParam()
	if err != nil {
		return nil, err
	}
	if sg.GroupName == "default" {
		return nil, errorf("CannotDelete", "the specified group: \"%s\" name: \"default\" cannot be deleted by a user", sg.GroupId)
	}
	enis, _ := all[NetworkInterface](c, tENI)
	for _, e := range enis {
		if slices.ContainsFunc(e.Groups, func(g GroupId) bool { return g.GroupId == sg.GroupId }) {
			return nil, errorf("DependencyViolation", "resource %s has a dependent object", sg.GroupId)
		}
	}
	sgs, _ := all[SecurityGroup](c, tSG)
	for _, other := range sgs {
		if other.GroupId == sg.GroupId {
			continue
		}
		for _, r := range other.Rules {
			if r.ReferencedGroupInfo != nil && r.ReferencedGroupInfo.GroupId == sg.GroupId {
				return nil, errorf("DependencyViolation", "resource %s has a dependent object", sg.GroupId)
			}
		}
	}
	if err := c.remove(tSG, sg.GroupId); err != nil {
		return nil, err
	}
	return struct {
		Return  bool   `xml:"return"`
		GroupId string `xml:"groupId"`
	}{true, sg.GroupId}, nil
}

// permissions groups rules into IpPermissions the way DescribeSecurityGroups
// reports them: one entry per protocol and port range.
func permissions(rules []SgRule, egress bool) []IpPermission {
	var out []IpPermission
	for _, r := range rules {
		if r.IsEgress != egress {
			continue
		}
		i := slices.IndexFunc(out, func(p IpPermission) bool {
			return p.IpProtocol == r.IpProtocol && intOr(p.FromPort, -1) == r.FromPort && intOr(p.ToPort, -1) == r.ToPort
		})
		if i < 0 {
			p := IpPermission{IpProtocol: r.IpProtocol}
			if r.IpProtocol == "tcp" || r.IpProtocol == "udp" || r.IpProtocol == "icmp" || r.IpProtocol == "icmpv6" {
				from, to := r.FromPort, r.ToPort
				p.FromPort, p.ToPort = &from, &to
			}
			out = append(out, p)
			i = len(out) - 1
		}
		p := &out[i]
		switch {
		case r.CidrIpv4 != "":
			p.IpRanges = append(p.IpRanges, IpRange{r.CidrIpv4, r.Description})
		case r.CidrIpv6 != "":
			p.Ipv6Ranges = append(p.Ipv6Ranges, Ipv6Range{r.CidrIpv6, r.Description})
		case r.PrefixListId != "":
			p.PrefixListIds = append(p.PrefixListIds, PrefixListId{r.PrefixListId, r.Description})
		case r.ReferencedGroupInfo != nil:
			p.Groups = append(p.Groups, UserIdGroupPair{r.ReferencedGroupInfo.GroupId, r.ReferencedGroupInfo.UserId, r.Description})
		}
	}
	return out
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func describeSecurityGroups(c *call) (any, error) {
	sgs, err := describe[SecurityGroup](c, tSG, c.p.list("GroupId"), "InvalidGroup.NotFound")
	if err != nil {
		return nil, err
	}
	if names := c.p.list("GroupName"); len(names) > 0 {
		var picked []*SecurityGroup
		for _, n := range names {
			i := slices.IndexFunc(sgs, func(g *SecurityGroup) bool { return g.GroupName == n })
			if i < 0 {
				return nil, errorf("InvalidGroup.NotFound", "The security group '%s' does not exist", n)
			}
			for _, g := range sgs {
				if g.GroupName == n {
					picked = append(picked, g)
				}
			}
		}
		sgs = picked
	}
	for _, g := range sgs {
		g.IpPermissions = permissions(g.Rules, false)
		g.IpPermissionsEgress = permissions(g.Rules, true)
	}
	return struct {
		Groups []*SecurityGroup `xml:"securityGroupInfo>item"`
	}{sgs}, nil
}

func authorize(c *call, egress bool) (any, error) {
	sg, err := c.groupParam()
	if err != nil {
		return nil, err
	}
	rules, err := c.parsePermissions(sg, egress)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, missing("IpPermissions")
	}
	tags, err := c.tagSpec("security-group-rule")
	if err != nil {
		return nil, err
	}
	for i := range rules {
		for _, existing := range sg.Rules {
			if sameRule(existing, rules[i]) {
				return nil, errorf("InvalidPermission.Duplicate", "the specified rule \"%s\" already exists", rules[i])
			}
		}
		for _, earlier := range rules[:i] {
			if sameRule(earlier, rules[i]) {
				return nil, errorf("InvalidPermission.Duplicate", "the specified rule \"%s\" already exists", rules[i])
			}
		}
		rules[i].Tags = tags
	}
	if n := countRules(sg.Rules, egress) + len(rules); n > maxRulesPerGroup {
		return nil, errorf("RulesPerSecurityGroupLimitExceeded", "The maximum number of rules per security group has been reached.")
	}
	sg.Rules = append(sg.Rules, rules...)
	if err := c.put(tSG, sg); err != nil {
		return nil, err
	}
	return struct {
		Return bool     `xml:"return"`
		Rules  []SgRule `xml:"securityGroupRuleSet>item"`
	}{true, rules}, nil
}

const maxRulesPerGroup = 60

func countRules(rules []SgRule, egress bool) int {
	n := 0
	for _, r := range rules {
		if r.IsEgress == egress {
			n++
		}
	}
	return n
}

func revoke(c *call, egress bool) (any, error) {
	sg, err := c.groupParam()
	if err != nil {
		return nil, err
	}
	if ids := c.p.list("SecurityGroupRuleId"); len(ids) > 0 {
		for _, id := range ids {
			i := slices.IndexFunc(sg.Rules, func(r SgRule) bool { return r.SecurityGroupRuleId == id && r.IsEgress == egress })
			if i < 0 {
				return nil, errorf("InvalidSecurityGroupRuleId.NotFound", "The security group rule ID '%s' does not exist", id)
			}
			sg.Rules = slices.Delete(sg.Rules, i, i+1)
		}
		return nil, c.put(tSG, sg)
	}
	rules, err := c.parsePermissions(sg, egress)
	if err != nil {
		return nil, err
	}
	var unknown []SgRule
	removed := 0
	for _, want := range rules {
		i := slices.IndexFunc(sg.Rules, func(r SgRule) bool { return sameRule(r, want) })
		if i < 0 {
			unknown = append(unknown, want)
			continue
		}
		sg.Rules = slices.Delete(sg.Rules, i, i+1)
		removed++
	}
	if removed == 0 {
		return nil, errorf("InvalidPermission.NotFound", "The specified rule does not exist in this security group.")
	}
	if err := c.put(tSG, sg); err != nil {
		return nil, err
	}
	return struct {
		Return  bool           `xml:"return"`
		Unknown []IpPermission `xml:"unknownIpPermissionSet>item,omitempty"`
	}{true, permissions(unknown, egress)}, nil
}

// findRule locates a rule by ID across all groups.
func (c *call) findRule(id string) (*SecurityGroup, int, error) {
	sgs, err := all[SecurityGroup](c, tSG)
	if err != nil {
		return nil, 0, err
	}
	for _, g := range sgs {
		if i := slices.IndexFunc(g.Rules, func(r SgRule) bool { return r.SecurityGroupRuleId == id }); i >= 0 {
			return g, i, nil
		}
	}
	return nil, 0, errorf("InvalidSecurityGroupRuleId.NotFound", "The security group rule ID '%s' does not exist", id)
}

func (r *SgRule) objectID() string { return r.SecurityGroupRuleId }
func (r *SgRule) tagList() *[]Tag  { return &r.Tags }
func (r *SgRule) filterValues() map[string][]string {
	return map[string][]string{"group-id": {r.GroupId}, "security-group-rule-id": {r.SecurityGroupRuleId}}
}

func describeSecurityGroupRules(c *call) (any, error) {
	sgs, err := all[SecurityGroup](c, tSG)
	if err != nil {
		return nil, err
	}
	var rules []*SgRule
	for _, g := range sgs {
		for i := range g.Rules {
			rules = append(rules, &g.Rules[i])
		}
	}
	if ids := c.p.list("SecurityGroupRuleId"); len(ids) > 0 {
		var picked []*SgRule
		for _, id := range ids {
			i := slices.IndexFunc(rules, func(r *SgRule) bool { return r.SecurityGroupRuleId == id })
			if i < 0 {
				return nil, errorf("InvalidSecurityGroupRuleId.NotFound", "The security group rule ID '%s' does not exist", id)
			}
			picked = append(picked, rules[i])
		}
		rules = picked
	}
	var out []*SgRule
	for _, r := range rules {
		ok, err := matches(c.p.filters(), r)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r)
		}
	}
	return struct {
		Rules []*SgRule `xml:"securityGroupRuleSet>item"`
	}{out}, nil
}

func modifySecurityGroupRules(c *call) (any, error) {
	sg, err := c.securityGroup(c.p.get("GroupId"))
	if err != nil {
		return nil, err
	}
	for _, m := range c.p.sub("SecurityGroupRule") {
		id := m.get("SecurityGroupRuleId")
		i := slices.IndexFunc(sg.Rules, func(r SgRule) bool { return r.SecurityGroupRuleId == id })
		if i < 0 {
			return nil, errorf("InvalidSecurityGroupRuleId.NotFound", "The security group rule ID '%s' does not exist", id)
		}
		req := params{}
		for k, v := range m {
			if rest, ok := strings.CutPrefix(k, "SecurityGroupRule."); ok {
				req[rest] = v
			}
		}
		proto, err := normalizeProtocol(req.get("IpProtocol"))
		if err != nil {
			return nil, err
		}
		from, to, err := ports(proto, req.optInt("FromPort"), req.optInt("ToPort"))
		if err != nil {
			return nil, err
		}
		src := ruleSource{cidr4: req.get("CidrIpv4"), cidr6: req.get("CidrIpv6"), prefixList: req.get("PrefixListId"), group: req.get("ReferencedGroupId")}
		if len(nonEmpty(src.cidr4, src.cidr6, src.prefixList, src.group)) != 1 {
			return nil, errorf("InvalidParameterCombination", "Exactly one of CidrIpv4, CidrIpv6, PrefixListId and ReferencedGroupId is required")
		}
		old := sg.Rules[i]
		r := c.newRule(sg, id, old.IsEgress, proto, from, to, src, req.get("Description"))
		r.Tags = old.Tags
		for j, other := range sg.Rules {
			if j != i && sameRule(other, r) {
				return nil, errorf("InvalidPermission.Duplicate", "the specified rule \"%s\" already exists", r)
			}
		}
		sg.Rules[i] = r
	}
	return nil, c.put(tSG, sg)
}

func updateRuleDescriptions(c *call, egress bool) (any, error) {
	sg, err := c.groupParam()
	if err != nil {
		return nil, err
	}
	for _, d := range c.p.sub("SecurityGroupRuleDescription") {
		i := slices.IndexFunc(sg.Rules, func(r SgRule) bool { return r.SecurityGroupRuleId == d.get("SecurityGroupRuleId") })
		if i < 0 {
			return nil, errorf("InvalidSecurityGroupRuleId.NotFound", "The security group rule ID '%s' does not exist", d.get("SecurityGroupRuleId"))
		}
		sg.Rules[i].Description = d.get("Description")
	}
	rules, err := c.parsePermissions(sg, egress)
	if err != nil {
		return nil, err
	}
	for _, want := range rules {
		i := slices.IndexFunc(sg.Rules, func(r SgRule) bool { return sameRule(r, want) })
		if i < 0 {
			return nil, errorf("InvalidPermission.NotFound", "The specified rule does not exist in this security group.")
		}
		sg.Rules[i].Description = want.Description
	}
	return nil, c.put(tSG, sg)
}
