// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"crypto/rand"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

var eniActions = map[string]action{
	"CreateNetworkInterface":          createNetworkInterface,
	"DeleteNetworkInterface":          deleteNetworkInterface,
	"DescribeNetworkInterfaces":       describeNetworkInterfaces,
	"ModifyNetworkInterfaceAttribute": modifyNetworkInterfaceAttribute,
	"AttachNetworkInterface":          attachNetworkInterface,
	"DetachNetworkInterface":          detachNetworkInterface,
	"AssignPrivateIpAddresses":        assignPrivateIpAddresses,
	"UnassignPrivateIpAddresses":      unassignPrivateIpAddresses,
}

func (e *NetworkInterface) objectID() string { return e.NetworkInterfaceId }
func (e *NetworkInterface) tagList() *[]Tag  { return &e.Tags }
func (e *NetworkInterface) filterValues() map[string][]string {
	m := map[string][]string{
		"network-interface-id": {e.NetworkInterfaceId}, "subnet-id": {e.SubnetId}, "vpc-id": {e.VpcId},
		"availability-zone": {e.AvailabilityZone}, "description": {e.Description}, "owner-id": {e.OwnerId},
		"private-ip-address": {e.PrivateIpAddress}, "private-dns-name": {e.PrivateDnsName}, "status": {e.Status},
		"interface-type": {e.InterfaceType}, "requester-managed": {b2s(e.RequesterManaged)},
		"requester-id": nonEmpty(e.RequesterId), "mac-address": {e.MacAddress},
		"source-dest-check": {b2s(e.SourceDestCheck)}, "source-desk-check": {b2s(e.SourceDestCheck)},
		"attachment.attachment-id": nil, "attachment.instance-id": nil, "attachment.instance-owner-id": nil,
		"attachment.device-index": nil, "attachment.status": nil, "attachment.delete-on-termination": nil,
		"association.public-ip": nil, "association.allocation-id": nil, "association.association-id": nil,
		"association.ip-owner-id": nil, "association.public-dns-name": nil,
	}
	for _, g := range e.Groups {
		m["group-id"] = append(m["group-id"], g.GroupId)
		m["group-name"] = append(m["group-name"], g.GroupName)
	}
	for _, p := range e.PrivateIpAddresses {
		m["addresses.private-ip-address"] = append(m["addresses.private-ip-address"], p.PrivateIpAddress)
		m["addresses.primary"] = append(m["addresses.primary"], b2s(p.Primary))
	}
	if a := e.Attachment; a != nil {
		m["attachment.attachment-id"] = []string{a.AttachmentId}
		m["attachment.instance-id"] = nonEmpty(a.InstanceId)
		m["attachment.instance-owner-id"] = []string{a.InstanceOwnerId}
		m["attachment.device-index"] = []string{i2s(a.DeviceIndex)}
		m["attachment.status"] = []string{a.Status}
		m["attachment.delete-on-termination"] = []string{b2s(a.DeleteOnTermination)}
	}
	if a := e.Association; a != nil {
		m["association.public-ip"] = []string{a.PublicIp}
		m["association.allocation-id"] = nonEmpty(a.AllocationId)
		m["association.association-id"] = nonEmpty(a.AssociationId)
		m["association.ip-owner-id"] = []string{a.IpOwnerId}
		m["association.public-dns-name"] = []string{a.PublicDnsName}
	}
	return m
}

func eniAssoc(a *Address, owner string) *EniAssociation {
	return &EniAssociation{
		AllocationId: a.AllocationId, AssociationId: a.AssociationId, IpOwnerId: owner,
		PublicIp: a.PublicIp, PublicDnsName: publicDNS(a.PublicIp),
	}
}

func (e *NetworkInterface) setAssociation(a *Address, owner string) {
	e.Association = eniAssoc(a, owner)
	for i := range e.PrivateIpAddresses {
		if e.PrivateIpAddresses[i].Primary {
			e.PrivateIpAddresses[i].Association = e.Association
		}
	}
}

func publicDNS(ip string) string {
	return "ec2-" + strings.ReplaceAll(ip, ".", "-") + ".compute-1.amazonaws.com"
}

func (c *call) privateDNS(ip string) string {
	name := "ip-" + strings.ReplaceAll(ip, ".", "-")
	if c.region == "us-east-1" {
		return name + ".ec2.internal"
	}
	return name + "." + c.region + ".compute.internal"
}

func (c *call) eni(id string) (*NetworkInterface, error) {
	return must[NetworkInterface](c, tENI, id, "InvalidNetworkInterfaceID.NotFound")
}

// usedIPs is every private address assigned in a subnet.
func (c *call) usedIPs(subnetID string) map[string]bool {
	used := map[string]bool{}
	enis, _ := all[NetworkInterface](c, tENI)
	for _, e := range enis {
		if e.SubnetId != subnetID {
			continue
		}
		for _, p := range e.PrivateIpAddresses {
			used[p.PrivateIpAddress] = true
		}
	}
	return used
}

// takeIP reserves ip (or the next free address when ip is "") in subnet s.
func (c *call) takeIP(s *Subnet, ip string, used map[string]bool) (string, error) {
	prefix := s.prefix()
	if ip == "" {
		a, err := allocateIP(prefix, used)
		if err != nil {
			return "", errorf("InsufficientFreeAddressesInSubnet", "There are not enough free addresses in subnet '%s' to satisfy the requested number of instances.", s.SubnetId)
		}
		used[a.String()] = true
		return a.String(), nil
	}
	a, err := netip.ParseAddr(ip)
	if err != nil || !prefix.Contains(a) {
		return "", errorf("InvalidParameterValue", "Address %s does not fall within the subnet's address range", ip)
	}
	if !usable(prefix, a) {
		return "", errorf("InvalidParameterValue", "Address %s is reserved by AWS in subnet %s", ip, s.SubnetId)
	}
	if used[ip] {
		return "", errorf("InvalidIPAddress.InUse", "Address %s is in use.", ip)
	}
	used[ip] = true
	return ip, nil
}

type eniSpec struct {
	ip          string
	secondary   []string
	nSecondary  int
	description string
	ifType      string
	requester   string
	groups      []string
	noGroups    bool
}

// createEni allocates addresses in s and stores a new interface.
func (c *call) createEni(s *Subnet, spec eniSpec) (*NetworkInterface, error) {
	used := c.usedIPs(s.SubnetId)
	primary, err := c.takeIP(s, spec.ip, used)
	if err != nil {
		return nil, err
	}
	e := &NetworkInterface{
		NetworkInterfaceId: newID("eni"), SubnetId: s.SubnetId, VpcId: s.VpcId,
		AvailabilityZone: s.AvailabilityZone, AvailabilityZoneId: s.AvailabilityZoneId,
		Description: spec.description, OwnerId: c.account, Status: "available", MacAddress: newMAC(),
		PrivateIpAddress: primary, PrivateDnsName: c.privateDNS(primary), SourceDestCheck: true,
		InterfaceType:      spec.ifType,
		PrivateIpAddresses: []EniPrivateIp{{PrivateIpAddress: primary, PrivateDnsName: c.privateDNS(primary), Primary: true}},
	}
	if e.InterfaceType == "" {
		e.InterfaceType = "interface"
	}
	if spec.requester != "" {
		e.RequesterId, e.RequesterManaged, e.SourceDestCheck = spec.requester, true, false
		e.Status = "in-use"
	}
	extra := spec.secondary
	for i := 0; i < spec.nSecondary; i++ {
		extra = append(extra, "")
	}
	for _, want := range extra {
		ip, err := c.takeIP(s, want, used)
		if err != nil {
			return nil, err
		}
		e.PrivateIpAddresses = append(e.PrivateIpAddresses, EniPrivateIp{PrivateIpAddress: ip, PrivateDnsName: c.privateDNS(ip)})
	}
	if !spec.noGroups {
		if e.Groups, err = c.resolveGroups(s.VpcId, spec.groups, nil); err != nil {
			return nil, err
		}
	}
	return e, c.put(tENI, e)
}

// resolveGroups turns security group IDs (or names, for the default VPC)
// into group identifiers, defaulting to the VPC's default group.
func (c *call) resolveGroups(vpcID string, ids, names []string) ([]GroupId, error) {
	sgs, err := all[SecurityGroup](c, tSG)
	if err != nil {
		return nil, err
	}
	var out []GroupId
	for _, id := range ids {
		i := slices.IndexFunc(sgs, func(g *SecurityGroup) bool { return g.GroupId == id })
		if i < 0 {
			return nil, errorf("InvalidGroup.NotFound", "The security group '%s' does not exist", id)
		}
		if sgs[i].VpcId != vpcID {
			return nil, errorf("InvalidParameter", "Security group %s and subnet belong to different networks.", id)
		}
		out = append(out, GroupId{sgs[i].GroupId, sgs[i].GroupName})
	}
	for _, n := range names {
		i := slices.IndexFunc(sgs, func(g *SecurityGroup) bool { return g.GroupName == n && g.VpcId == vpcID })
		if i < 0 {
			return nil, errorf("InvalidGroup.NotFound", "The security group '%s' does not exist in VPC '%s'", n, vpcID)
		}
		out = append(out, GroupId{sgs[i].GroupId, sgs[i].GroupName})
	}
	if len(out) == 0 {
		i := slices.IndexFunc(sgs, func(g *SecurityGroup) bool { return g.GroupName == "default" && g.VpcId == vpcID })
		if i >= 0 {
			out = append(out, GroupId{sgs[i].GroupId, sgs[i].GroupName})
		}
	}
	return out, nil
}

func newMAC() string {
	b := make([]byte, 5)
	rand.Read(b)
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4])
}

func createNetworkInterface(c *call) (any, error) {
	s, err := c.subnet(c.p.get("SubnetId"))
	if err != nil {
		return nil, err
	}
	tags, err := c.tagSpec("network-interface")
	if err != nil {
		return nil, err
	}
	spec := eniSpec{
		ip: c.p.get("PrivateIpAddress"), description: c.p.get("Description"), ifType: c.p.get("InterfaceType"),
		groups: c.p.list("SecurityGroupId"), nSecondary: c.p.integer("SecondaryPrivateIpAddressCount", 0),
	}
	for _, p := range c.p.sub("PrivateIpAddresses") {
		if p.boolean("Primary") {
			spec.ip = p.get("PrivateIpAddress")
		} else {
			spec.secondary = append(spec.secondary, p.get("PrivateIpAddress"))
		}
	}
	if spec.ifType != "" && spec.ifType != "interface" && spec.ifType != "efa" && spec.ifType != "trunk" {
		return nil, invalidParam("InterfaceType", spec.ifType)
	}
	// Validate groups before allocating anything.
	if _, err := c.resolveGroups(s.VpcId, spec.groups, nil); err != nil {
		return nil, err
	}
	e, err := c.createEni(s, spec)
	if err != nil {
		return nil, err
	}
	e.Tags = tags
	if err := c.put(tENI, e); err != nil {
		return nil, err
	}
	return struct {
		E *NetworkInterface `xml:"networkInterface"`
	}{e}, nil
}

func deleteNetworkInterface(c *call) (any, error) {
	e, err := c.eni(c.p.get("NetworkInterfaceId"))
	if err != nil {
		return nil, err
	}
	if e.attached() || e.RequesterManaged {
		return nil, errorf("InvalidNetworkInterface.InUse", "Interface: [%s] in use.", e.NetworkInterfaceId)
	}
	if e.Association != nil {
		if a, err := c.address(e.Association.AllocationId); err == nil {
			a.clearAssociation()
			c.put(tEIP, a)
		}
	}
	return nil, c.remove(tENI, e.NetworkInterfaceId)
}

func (e *NetworkInterface) attached() bool {
	return e.Attachment != nil && e.Attachment.Status != "detached"
}

// detachedVisible is how long a detached attachment stays visible, so
// waiters can see the "detached" state before it disappears.
const detachedVisible = 5 * time.Second

func describeNetworkInterfaces(c *call) (any, error) {
	enis, err := describe[NetworkInterface](c, tENI, c.p.list("NetworkInterfaceId"), "InvalidNetworkInterfaceID.NotFound")
	if err != nil {
		return nil, err
	}
	for _, e := range enis {
		if a := e.Attachment; a != nil && a.Status == "detached" {
			if t, err := time.Parse(time.RFC3339, a.AttachTime); err == nil && time.Since(t) > detachedVisible {
				e.Attachment = nil
			}
		}
	}
	return struct {
		Es []*NetworkInterface `xml:"networkInterfaceSet>item"`
	}{enis}, nil
}

func modifyNetworkInterfaceAttribute(c *call) (any, error) {
	e, err := c.eni(c.p.get("NetworkInterfaceId"))
	if err != nil {
		return nil, err
	}
	if c.p.has("Description.Value") {
		e.Description = c.p.get("Description.Value")
	}
	if v := c.p.optBool("SourceDestCheck.Value"); v != nil {
		e.SourceDestCheck = *v
	}
	if ids := c.p.list("SecurityGroupId"); len(ids) > 0 {
		if e.Groups, err = c.resolveGroups(e.VpcId, ids, nil); err != nil {
			return nil, err
		}
	}
	if v := c.p.optBool("Attachment.DeleteOnTermination"); v != nil {
		if e.Attachment == nil || e.Attachment.AttachmentId != c.p.get("Attachment.AttachmentId") {
			return nil, errorf("InvalidAttachmentID.NotFound", "The attachment ID '%s' does not exist", c.p.get("Attachment.AttachmentId"))
		}
		e.Attachment.DeleteOnTermination = *v
	}
	return nil, c.put(tENI, e)
}

func attachNetworkInterface(c *call) (any, error) {
	e, err := c.eni(c.p.get("NetworkInterfaceId"))
	if err != nil {
		return nil, err
	}
	inst, err := c.instance(c.p.get("InstanceId"))
	if err != nil {
		return nil, err
	}
	if e.attached() {
		return nil, errorf("InvalidNetworkInterface.InUse", "Interface: [%s] in use.", e.NetworkInterfaceId)
	}
	if inst.AvailabilityZone != e.AvailabilityZone {
		return nil, errorf("InvalidParameterCombination", "You may not attach a network interface to an instance if they are not in the same availability zone")
	}
	idx := c.p.integer("DeviceIndex", -1)
	if idx < 1 {
		return nil, invalidParam("DeviceIndex", c.p.get("DeviceIndex"))
	}
	for _, other := range inst.Enis {
		if o, err := c.eni(other); err == nil && o.Attachment != nil && o.Attachment.DeviceIndex == idx {
			return nil, errorf("InvalidParameterValue", "Instance '%s' already has an interface attached at device index '%d'.", inst.InstanceId, idx)
		}
	}
	e.Attachment = &EniAttachment{
		AttachmentId: newID("eni-attach"), InstanceId: inst.InstanceId, InstanceOwnerId: c.account,
		DeviceIndex: idx, NetworkCardIndex: c.p.integer("NetworkCardIndex", 0), Status: "attached", AttachTime: now(),
	}
	e.Status = "in-use"
	inst.Enis = append(inst.Enis, e.NetworkInterfaceId)
	if err := c.put(tENI, e); err != nil {
		return nil, err
	}
	if err := c.put(tInstance, inst); err != nil {
		return nil, err
	}
	return struct {
		AttachmentId     string `xml:"attachmentId"`
		NetworkCardIndex int    `xml:"networkCardIndex"`
	}{e.Attachment.AttachmentId, e.Attachment.NetworkCardIndex}, nil
}

func detachNetworkInterface(c *call) (any, error) {
	id := c.p.get("AttachmentId")
	enis, err := all[NetworkInterface](c, tENI)
	if err != nil {
		return nil, err
	}
	for _, e := range enis {
		if e.Attachment == nil || e.Attachment.AttachmentId != id || e.Attachment.Status == "detached" {
			continue
		}
		if e.Attachment.DeviceIndex == 0 {
			return nil, errorf("OperationNotPermitted", "The network interface at device index 0 cannot be detached.")
		}
		if inst, err := c.instance(e.Attachment.InstanceId); err == nil {
			inst.Enis = slices.DeleteFunc(inst.Enis, func(x string) bool { return x == e.NetworkInterfaceId })
			if err := c.put(tInstance, inst); err != nil {
				return nil, err
			}
		}
		e.Attachment.Status, e.Attachment.AttachTime = "detached", time.Now().UTC().Format(time.RFC3339)
		e.Status = "available"
		return nil, c.put(tENI, e)
	}
	return nil, errorf("InvalidAttachmentID.NotFound", "The attachment ID '%s' does not exist", id)
}

func assignPrivateIpAddresses(c *call) (any, error) {
	e, err := c.eni(c.p.get("NetworkInterfaceId"))
	if err != nil {
		return nil, err
	}
	s, err := c.subnet(e.SubnetId)
	if err != nil {
		return nil, err
	}
	want := c.p.list("PrivateIpAddress")
	for i := 0; i < c.p.integer("SecondaryPrivateIpAddressCount", 0); i++ {
		want = append(want, "")
	}
	used := c.usedIPs(s.SubnetId)
	type assigned struct {
		IP string `xml:"privateIpAddress"`
	}
	var out []assigned
	for _, w := range want {
		ip, err := c.takeIP(s, w, used)
		if err != nil {
			return nil, err
		}
		e.PrivateIpAddresses = append(e.PrivateIpAddresses, EniPrivateIp{PrivateIpAddress: ip, PrivateDnsName: c.privateDNS(ip)})
		out = append(out, assigned{ip})
	}
	if err := c.put(tENI, e); err != nil {
		return nil, err
	}
	return struct {
		ID       string     `xml:"networkInterfaceId"`
		Assigned []assigned `xml:"assignedPrivateIpAddressesSet>item"`
		Return   bool       `xml:"return"`
	}{e.NetworkInterfaceId, out, true}, nil
}

func unassignPrivateIpAddresses(c *call) (any, error) {
	e, err := c.eni(c.p.get("NetworkInterfaceId"))
	if err != nil {
		return nil, err
	}
	for _, ip := range c.p.list("PrivateIpAddress") {
		i := slices.IndexFunc(e.PrivateIpAddresses, func(p EniPrivateIp) bool { return p.PrivateIpAddress == ip })
		if i < 0 {
			return nil, errorf("InvalidParameterValue", "Some of the specified addresses are not assigned to interface %s", e.NetworkInterfaceId)
		}
		if e.PrivateIpAddresses[i].Primary {
			return nil, errorf("InvalidParameterValue", "The primary private IP address %s cannot be unassigned", ip)
		}
		e.PrivateIpAddresses = slices.Delete(e.PrivateIpAddresses, i, i+1)
	}
	return nil, c.put(tENI, e)
}
