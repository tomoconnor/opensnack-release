// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	"opensnack/internal/resource"
)

// Reachability answers "can A reach B on port P?" from the configuration:
// security groups (stateful), network ACLs (stateless, so the return
// traffic to an ephemeral port is checked too) and route tables. It is
// served on the inspection API, for external checkers:
//
//	GET /_opensnack/v1/namespaces/{ns}/ec2/reachability
//	    ?region=us-east-1&source=i-...|eni-...|10.0.1.5|internet|203.0.113.7
//	    &destination=...&protocol=tcp&port=443
//
// It models one VPC at a time plus the internet, through internet and NAT
// gateways. Peering, transit gateways and VPC endpoints are out of scope
// and report unreachable.

// ReachStep is one check on the path.
type ReachStep struct {
	Check    string `json:"check"`
	Resource string `json:"resource,omitempty"`
	Allowed  bool   `json:"allowed"`
	Detail   string `json:"detail"`
}

type ReachResult struct {
	Source      string      `json:"source"`
	Destination string      `json:"destination"`
	Protocol    string      `json:"protocol"`
	Port        int         `json:"port"`
	Reachable   bool        `json:"reachable"`
	Reason      string      `json:"reason,omitempty"`
	Steps       []ReachStep `json:"steps"`
}

// ephemeral is the port range return traffic uses; ACLs must allow it.
const ephemeralFrom, ephemeralTo = 1024, 65535

// internetAddr stands in for "somewhere on the internet".
var internetAddr = netip.MustParseAddr("203.0.113.10")

type endpoint struct {
	label    string
	eni      *NetworkInterface // nil for the internet
	ip       netip.Addr        // the address traffic is seen from or sent to
	internet bool
}

// ReachabilityHandler serves the inspection route.
func (h *Handler) ReachabilityHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	region := q.Get("region")
	if region == "" {
		region = "us-east-1"
	}
	port, _ := strconv.Atoi(q.Get("port"))
	res, err := h.Reachability(r.PathValue("ns"), region, q.Get("source"), q.Get("destination"), q.Get("protocol"), port)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(res)
}

// Reachability evaluates one flow in a namespace and region.
func (h *Handler) Reachability(namespace, region, source, destination, protocol string, port int) (*ReachResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := &call{h: h, ns: resource.JoinScope(namespace, region), region: region}
	if protocol == "" {
		protocol = "tcp"
	}
	proto, err := normalizeProtocol(protocol)
	if err != nil {
		return nil, err
	}
	if (proto == "tcp" || proto == "udp") && (port < 0 || port > 65535) {
		return nil, fmt.Errorf("port %d out of range", port)
	}
	res := &ReachResult{Source: source, Destination: destination, Protocol: proto, Port: port}
	src, err := c.reachEndpoint(source, true)
	if err != nil {
		return nil, err
	}
	dst, err := c.reachEndpoint(destination, false)
	if err != nil {
		return nil, err
	}
	e := &evaluator{c: c, proto: proto, port: port, res: res}
	e.evaluate(src, dst)
	res.Reachable = res.Reason == ""
	return res, nil
}

// reachEndpoint resolves an instance, interface, address or "internet".
func (c *call) reachEndpoint(spec string, isSource bool) (endpoint, error) {
	switch {
	case spec == "":
		return endpoint{}, fmt.Errorf("source and destination are required")
	case spec == "internet":
		return endpoint{label: spec, ip: internetAddr, internet: true}, nil
	case strings.HasPrefix(spec, "i-"):
		inst := new(Instance)
		if ok, err := c.get(tInstance, spec, inst); err != nil || !ok {
			return endpoint{}, fmt.Errorf("instance %s not found", spec)
		}
		if inst.State != stateRunning {
			return endpoint{}, fmt.Errorf("instance %s is %s", spec, inst.State.Name)
		}
		if len(inst.Enis) == 0 {
			return endpoint{}, fmt.Errorf("instance %s has no network interface", spec)
		}
		spec = inst.Enis[0]
		fallthrough
	case strings.HasPrefix(spec, "eni-"):
		eni := new(NetworkInterface)
		if ok, err := c.get(tENI, spec, eni); err != nil || !ok {
			return endpoint{}, fmt.Errorf("network interface %s not found", spec)
		}
		return endpoint{label: spec, eni: eni, ip: netip.MustParseAddr(eni.PrivateIpAddress)}, nil
	}
	ip, err := netip.ParseAddr(spec)
	if err != nil || !ip.Is4() {
		return endpoint{}, fmt.Errorf("can't resolve %q: give an instance, interface, IPv4 address or \"internet\"", spec)
	}
	enis, _ := all[NetworkInterface](c, tENI)
	for _, e := range enis {
		for _, p := range e.PrivateIpAddresses {
			if p.PrivateIpAddress == spec {
				return endpoint{label: spec, eni: e, ip: ip}, nil
			}
		}
		if e.Association != nil && e.Association.PublicIp == spec {
			// A public address is reached from the internet.
			return endpoint{label: spec, eni: e, ip: netip.MustParseAddr(e.PrivateIpAddress)}, nil
		}
	}
	if ip.IsPrivate() {
		return endpoint{}, fmt.Errorf("no interface has the address %s", spec)
	}
	return endpoint{label: spec, ip: ip, internet: true}, nil
}

type evaluator struct {
	c     *call
	proto string
	port  int
	res   *ReachResult
}

func (e *evaluator) step(check, resource string, allowed bool, format string, args ...any) bool {
	s := ReachStep{Check: check, Resource: resource, Allowed: allowed, Detail: fmt.Sprintf(format, args...)}
	e.res.Steps = append(e.res.Steps, s)
	if !allowed && e.res.Reason == "" {
		e.res.Reason = check + " " + resource + ": " + s.Detail
	}
	return allowed
}

func (e *evaluator) evaluate(src, dst endpoint) {
	switch {
	case src.internet && dst.internet:
		e.step("path", "", false, "both ends are on the internet")
	case src.internet:
		e.fromInternet(src, dst)
	case dst.internet:
		e.toInternet(src, dst)
	case src.eni.VpcId != dst.eni.VpcId:
		e.step("route", src.eni.VpcId, false, "%s and %s are in different VPCs, and peering isn't modelled", src.eni.VpcId, dst.eni.VpcId)
	default:
		e.withinVpc(src, dst)
	}
}

func (e *evaluator) withinVpc(src, dst endpoint) {
	if !e.sg(src.eni, true, dst.ip, groupIDsOf(dst.eni)) {
		return
	}
	if src.eni.SubnetId != dst.eni.SubnetId {
		if !e.nacl(src.eni.SubnetId, true, dst.ip, e.port) {
			return
		}
		rt, r := e.c.routeFor(src.eni.SubnetId, dst.ip)
		if r == nil || r.GatewayId != "local" {
			e.step("route", rtID(rt), false, "no local route to %s", dst.ip)
			return
		}
		e.step("route", rtID(rt), true, "%s via local", r.DestinationCidrBlock)
		if !e.nacl(dst.eni.SubnetId, false, src.ip, e.port) {
			return
		}
	}
	if !e.sg(dst.eni, false, src.ip, groupIDsOf(src.eni)) {
		return
	}
	if src.eni.SubnetId != dst.eni.SubnetId {
		// Responses cross the ACLs again, to an ephemeral port.
		if !e.naclReturn(dst.eni.SubnetId, true, src.ip) || !e.naclReturn(src.eni.SubnetId, false, dst.ip) {
			return
		}
	}
}

func (e *evaluator) fromInternet(src, dst endpoint) {
	if dst.eni.Association == nil {
		e.step("public address", dst.eni.NetworkInterfaceId, false, "the destination has no public IP address")
		return
	}
	e.step("public address", dst.eni.NetworkInterfaceId, true, "%s maps to %s", dst.eni.Association.PublicIp, dst.ip)
	if !e.igwRoute(dst.eni, src.ip) {
		return
	}
	if !e.nacl(dst.eni.SubnetId, false, src.ip, e.port) {
		return
	}
	if !e.sg(dst.eni, false, src.ip, nil) {
		return
	}
	e.naclReturn(dst.eni.SubnetId, true, src.ip)
}

// igwRoute checks the subnet routes the internet address through an
// internet gateway attached to the VPC.
func (e *evaluator) igwRoute(eni *NetworkInterface, ip netip.Addr) bool {
	rt, r := e.c.routeFor(eni.SubnetId, ip)
	if r == nil {
		return e.step("route", rtID(rt), false, "no route to %s", ip)
	}
	if !strings.HasPrefix(r.GatewayId, "igw-") {
		return e.step("route", rtID(rt), false, "%s goes to %s, not an internet gateway", r.DestinationCidrBlock, routeTargetID(*r))
	}
	if !e.igwAttached(r.GatewayId, eni.VpcId) {
		return e.step("internet gateway", r.GatewayId, false, "not attached to %s", eni.VpcId)
	}
	return e.step("route", rtID(rt), true, "%s via %s", r.DestinationCidrBlock, r.GatewayId)
}

func (e *evaluator) igwAttached(id, vpcID string) bool {
	g := new(InternetGateway)
	ok, _ := e.c.get(tIGW, id, g)
	return ok && slices.ContainsFunc(g.Attachments, func(a IgwAttach) bool { return a.VpcId == vpcID })
}

func (e *evaluator) toInternet(src, dst endpoint) {
	if !e.sg(src.eni, true, dst.ip, nil) {
		return
	}
	if !e.nacl(src.eni.SubnetId, true, dst.ip, e.port) {
		return
	}
	rt, r := e.c.routeFor(src.eni.SubnetId, dst.ip)
	switch {
	case r == nil:
		e.step("route", rtID(rt), false, "no route to %s", dst.ip)
		return
	case strings.HasPrefix(r.GatewayId, "igw-"):
		if !e.igwAttached(r.GatewayId, src.eni.VpcId) {
			e.step("internet gateway", r.GatewayId, false, "not attached to %s", src.eni.VpcId)
			return
		}
		if src.eni.Association == nil {
			e.step("public address", src.eni.NetworkInterfaceId, false, "the source has no public IP address to use through %s", r.GatewayId)
			return
		}
		e.step("route", rtID(rt), true, "%s via %s", r.DestinationCidrBlock, r.GatewayId)
	case r.NatGatewayId != "":
		e.step("route", rtID(rt), true, "%s via %s", r.DestinationCidrBlock, r.NatGatewayId)
		if !e.throughNat(r.NatGatewayId, src, dst) {
			return
		}
	default:
		e.step("route", rtID(rt), false, "%s goes to %s, which doesn't lead to the internet", r.DestinationCidrBlock, routeTargetID(*r))
		return
	}
	e.naclReturn(src.eni.SubnetId, false, dst.ip)
}

// throughNat follows traffic into a NAT gateway and out of its subnet.
func (e *evaluator) throughNat(id string, src, dst endpoint) bool {
	n := new(NatGateway)
	if ok, _ := e.c.get(tNAT, id, n); !ok || n.State != "available" {
		return e.step("nat gateway", id, false, "not available")
	}
	if n.ConnectivityType != "public" || len(n.Addresses) == 0 || n.Addresses[0].PublicIp == "" {
		return e.step("nat gateway", id, false, "a private NAT gateway can't reach the internet")
	}
	if n.SubnetId != src.eni.SubnetId {
		if !e.nacl(n.SubnetId, false, src.ip, e.port) || !e.naclReturn(n.SubnetId, true, src.ip) {
			return false
		}
	}
	if !e.nacl(n.SubnetId, true, dst.ip, e.port) {
		return false
	}
	eni := &NetworkInterface{NetworkInterfaceId: n.Addresses[0].NetworkInterfaceId, SubnetId: n.SubnetId, VpcId: n.VpcId,
		Association: &EniAssociation{PublicIp: n.Addresses[0].PublicIp}}
	if !e.igwRoute(eni, dst.ip) {
		return false
	}
	return e.naclReturn(n.SubnetId, false, dst.ip)
}

func rtID(rt *RouteTable) string {
	if rt == nil {
		return ""
	}
	return rt.RouteTableId
}

func routeTargetID(r Route) string {
	return strings.Join(nonEmpty(r.GatewayId, r.NatGatewayId, r.InstanceId, r.NetworkInterfaceId, r.VpcPeeringConnectionId,
		r.TransitGatewayId, r.EgressOnlyInternetGatewayId), ",")
}

// routeFor finds the subnet's route table (explicit or main) and its
// longest-prefix route to ip, skipping blackholes.
func (c *call) routeFor(subnetID string, ip netip.Addr) (*RouteTable, *Route) {
	s := new(Subnet)
	if ok, _ := c.get(tSubnet, subnetID, s); !ok {
		return nil, nil
	}
	rts, _ := all[RouteTable](c, tRTB)
	var table *RouteTable
	for _, rt := range rts {
		if rt.VpcId != s.VpcId {
			continue
		}
		for _, a := range rt.Associations {
			if a.SubnetId == subnetID {
				table = rt
			}
			if a.Main && table == nil {
				table = rt
			}
		}
	}
	if table == nil {
		return nil, nil
	}
	live := c.liveTargets()
	var best *Route
	bestBits := -1
	for i, r := range table.Routes {
		if routeState(r, live) != "active" {
			continue
		}
		for _, p := range c.routeDestinations(r) {
			if p.Contains(ip) && p.Bits() > bestBits {
				best, bestBits = &table.Routes[i], p.Bits()
			}
		}
	}
	return table, best
}

func (c *call) routeDestinations(r Route) []netip.Prefix {
	if p, ok := parseCIDR(r.DestinationCidrBlock); ok {
		return []netip.Prefix{p}
	}
	if r.DestinationPrefixListId != "" {
		return c.prefixListCIDRs(r.DestinationPrefixListId)
	}
	return nil
}

func (c *call) prefixListCIDRs(id string) []netip.Prefix {
	for _, svc := range []string{"s3", "dynamodb"} {
		if stableID("pl", c.region, svc)[:11] != id {
			continue
		}
		cidrs := map[string][]string{"s3": {"52.216.0.0/15", "54.231.0.0/16"}, "dynamodb": {"52.94.0.0/22"}}[svc]
		var out []netip.Prefix
		for _, s := range cidrs {
			out = append(out, netip.MustParsePrefix(s))
		}
		return out
	}
	return nil
}

func groupIDsOf(e *NetworkInterface) []string { return idsOf(e.Groups) }

// sg checks the interface's security groups for a rule allowing the flow.
func (e *evaluator) sg(eni *NetworkInterface, egress bool, peer netip.Addr, peerGroups []string) bool {
	dir := "inbound"
	if egress {
		dir = "outbound"
	}
	check := "security group " + dir
	for _, g := range eni.Groups {
		sg := new(SecurityGroup)
		if ok, _ := e.c.get(tSG, g.GroupId, sg); !ok {
			continue
		}
		for _, r := range sg.Rules {
			if r.IsEgress != egress || !protoMatches(r.IpProtocol, e.proto) || !portMatches(r.IpProtocol, r.FromPort, r.ToPort, e.port) {
				continue
			}
			if e.peerMatches(r, peer, peerGroups) {
				return e.step(check, sg.GroupId, true, "rule %s allows %s", r.SecurityGroupRuleId, r)
			}
		}
	}
	return e.step(check, strings.Join(idsOf(eni.Groups), ","), false, "no rule allows %s %s %s port %d", e.proto, map[bool]string{true: "to", false: "from"}[egress], peer, e.port)
}

func (e *evaluator) peerMatches(r SgRule, peer netip.Addr, peerGroups []string) bool {
	switch {
	case r.CidrIpv4 != "":
		p, err := netip.ParsePrefix(r.CidrIpv4)
		return err == nil && p.Contains(peer)
	case r.ReferencedGroupInfo != nil:
		return slices.Contains(peerGroups, r.ReferencedGroupInfo.GroupId)
	case r.PrefixListId != "":
		return slices.ContainsFunc(e.c.prefixListCIDRs(r.PrefixListId), func(p netip.Prefix) bool { return p.Contains(peer) })
	}
	return false
}

func protoMatches(rule, flow string) bool {
	return rule == "-1" || rule == flow
}

func portMatches(proto string, from, to, port int) bool {
	switch proto {
	case "tcp", "udp":
		return port >= from && port <= to
	case "icmp", "icmpv6":
		return from == -1 || from == port
	}
	return true
}

// nacl checks the subnet's network ACL, first matching rule by number.
func (e *evaluator) nacl(subnetID string, egress bool, peer netip.Addr, port int) bool {
	dir := "inbound"
	if egress {
		dir = "outbound"
	}
	allowed, acl, detail := e.naclDecision(subnetID, egress, peer, e.proto, port)
	return e.step("network ACL "+dir, acl, allowed, "%s", detail)
}

// naclReturn checks the ACL lets the response through to an ephemeral
// port, which a stateless ACL needs a rule for.
func (e *evaluator) naclReturn(subnetID string, egress bool, peer netip.Addr) bool {
	if e.proto != "tcp" && e.proto != "udp" {
		return true
	}
	dir := "inbound"
	if egress {
		dir = "outbound"
	}
	// Any ephemeral port the ACL allows is enough for the client to pick.
	for _, p := range []int{ephemeralFrom, 32768, 49152, ephemeralTo} {
		if ok, acl, detail := e.naclDecision(subnetID, egress, peer, e.proto, p); ok {
			return e.step("network ACL "+dir+" (return)", acl, true, "%s", detail)
		}
	}
	_, acl, detail := e.naclDecision(subnetID, egress, peer, e.proto, ephemeralFrom)
	return e.step("network ACL "+dir+" (return)", acl, false, "responses to ephemeral ports are blocked: %s", detail)
}

func (e *evaluator) naclDecision(subnetID string, egress bool, peer netip.Addr, proto string, port int) (bool, string, string) {
	s := new(Subnet)
	if ok, _ := e.c.get(tSubnet, subnetID, s); !ok {
		return false, "", "subnet " + subnetID + " not found"
	}
	acl := new(NetworkAcl)
	if ok, _ := e.c.get(tNACL, s.NetworkAclId, acl); !ok {
		return false, s.NetworkAclId, "network ACL not found"
	}
	entries := slices.Clone(acl.Entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].RuleNumber < entries[j].RuleNumber })
	num, _ := naclProtocol(proto)
	for _, en := range entries {
		if en.Egress != egress || en.CidrBlock == "" {
			continue
		}
		p, err := netip.ParsePrefix(en.CidrBlock)
		if err != nil || !p.Contains(peer) || (en.Protocol != "-1" && en.Protocol != num) {
			continue
		}
		if en.PortRange != nil && (port < en.PortRange.From || port > en.PortRange.To) {
			continue
		}
		rule := "rule " + i2s(en.RuleNumber)
		if en.RuleNumber == naclDefaultRule {
			rule = "the default rule (*)"
		}
		return en.RuleAction == "allow", acl.NetworkAclId, fmt.Sprintf("%s %ss %s %s port %d", rule, en.RuleAction, proto, peer, port)
	}
	return false, acl.NetworkAclId, "no rule matches"
}
