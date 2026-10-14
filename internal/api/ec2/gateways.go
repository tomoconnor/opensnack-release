// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"crypto/rand"
	"fmt"
	"slices"
	"time"
)

// Internet gateways, NAT gateways and elastic IPs.

var gatewayActions = map[string]action{
	"CreateInternetGateway":      createInternetGateway,
	"DeleteInternetGateway":      deleteInternetGateway,
	"DescribeInternetGateways":   describeInternetGateways,
	"AttachInternetGateway":      attachInternetGateway,
	"DetachInternetGateway":      detachInternetGateway,
	"CreateNatGateway":           createNatGateway,
	"DeleteNatGateway":           deleteNatGateway,
	"DescribeNatGateways":        describeNatGateways,
	"AllocateAddress":            allocateAddress,
	"ReleaseAddress":             releaseAddress,
	"DescribeAddresses":          describeAddresses,
	"AssociateAddress":           associateAddress,
	"DisassociateAddress":        disassociateAddress,
	"DescribeAddressesAttribute": describeAddressesAttribute,
}

func (g *InternetGateway) objectID() string { return g.InternetGatewayId }
func (g *InternetGateway) tagList() *[]Tag  { return &g.Tags }
func (g *InternetGateway) filterValues() map[string][]string {
	m := map[string][]string{"internet-gateway-id": {g.InternetGatewayId}, "owner-id": {g.OwnerId},
		"attachment.vpc-id": nil, "attachment.state": nil}
	for _, a := range g.Attachments {
		m["attachment.vpc-id"] = append(m["attachment.vpc-id"], a.VpcId)
		m["attachment.state"] = append(m["attachment.state"], a.State)
	}
	return m
}

func (c *call) igw(id string) (*InternetGateway, error) {
	return must[InternetGateway](c, tIGW, id, "InvalidInternetGatewayID.NotFound")
}

func createInternetGateway(c *call) (any, error) {
	tags, err := c.tagSpec("internet-gateway")
	if err != nil {
		return nil, err
	}
	g := &InternetGateway{InternetGatewayId: newID("igw"), OwnerId: c.account, Tags: tags}
	if err := c.put(tIGW, g); err != nil {
		return nil, err
	}
	return struct {
		G *InternetGateway `xml:"internetGateway"`
	}{g}, nil
}

func deleteInternetGateway(c *call) (any, error) {
	g, err := c.igw(c.p.get("InternetGatewayId"))
	if err != nil {
		return nil, err
	}
	if len(g.Attachments) > 0 {
		return nil, errorf("DependencyViolation", "The internetGateway '%s' has dependencies and cannot be deleted.", g.InternetGatewayId)
	}
	return nil, c.remove(tIGW, g.InternetGatewayId)
}

func describeInternetGateways(c *call) (any, error) {
	gs, err := describe[InternetGateway](c, tIGW, c.p.list("InternetGatewayId"), "InvalidInternetGatewayID.NotFound")
	if err != nil {
		return nil, err
	}
	return struct {
		Gs []*InternetGateway `xml:"internetGatewaySet>item"`
	}{gs}, nil
}

func attachInternetGateway(c *call) (any, error) {
	g, err := c.igw(c.p.get("InternetGatewayId"))
	if err != nil {
		return nil, err
	}
	vpc, err := c.vpc(c.p.get("VpcId"))
	if err != nil {
		return nil, err
	}
	if len(g.Attachments) > 0 {
		return nil, errorf("Resource.AlreadyAssociated", "resource %s is already attached to network %s", g.InternetGatewayId, g.Attachments[0].VpcId)
	}
	igws, _ := all[InternetGateway](c, tIGW)
	for _, other := range igws {
		if slices.ContainsFunc(other.Attachments, func(a IgwAttach) bool { return a.VpcId == vpc.VpcId }) {
			return nil, errorf("Resource.AlreadyAssociated", "Network %s already has an internet gateway attached", vpc.VpcId)
		}
	}
	g.Attachments = []IgwAttach{{VpcId: vpc.VpcId, State: "available"}}
	return nil, c.put(tIGW, g)
}

func detachInternetGateway(c *call) (any, error) {
	g, err := c.igw(c.p.get("InternetGatewayId"))
	if err != nil {
		return nil, err
	}
	vpcID := c.p.get("VpcId")
	if !slices.ContainsFunc(g.Attachments, func(a IgwAttach) bool { return a.VpcId == vpcID }) {
		return nil, errorf("Gateway.NotAttached", "resource %s is not attached to network %s", g.InternetGatewayId, vpcID)
	}
	// AWS refuses while public addresses in the VPC still route through it.
	enis, _ := all[NetworkInterface](c, tENI)
	for _, e := range enis {
		if e.VpcId == vpcID && e.Association != nil {
			return nil, errorf("DependencyViolation", "Network %s has some mapped public address(es). Please unmap those public address(es) before detaching the gateway.", vpcID)
		}
	}
	g.Attachments = nil
	return nil, c.put(tIGW, g)
}

// NAT gateways.

func (n *NatGateway) objectID() string { return n.NatGatewayId }
func (n *NatGateway) tagList() *[]Tag  { return &n.Tags }
func (n *NatGateway) filterValues() map[string][]string {
	return map[string][]string{"nat-gateway-id": {n.NatGatewayId}, "subnet-id": {n.SubnetId}, "vpc-id": {n.VpcId}, "state": {n.State}}
}

func createNatGateway(c *call) (any, error) {
	s, err := c.subnet(c.p.get("SubnetId"))
	if err != nil {
		return nil, err
	}
	connectivity := c.p.get("ConnectivityType")
	if connectivity == "" {
		connectivity = "public"
	}
	if mode := c.p.get("AvailabilityMode"); mode != "" && mode != "zonal" {
		return nil, errorf("UnsupportedOperation", "Regional NAT gateways are not supported")
	}
	tags, err := c.tagSpec("natgateway")
	if err != nil {
		return nil, err
	}
	var eip *Address
	if connectivity == "public" {
		allocID := c.p.get("AllocationId")
		if allocID == "" {
			return nil, missing("AllocationId")
		}
		if eip, err = c.address(allocID); err != nil {
			return nil, err
		}
		if eip.AssociationId != "" {
			return nil, errorf("Resource.AlreadyAssociated", "Elastic IP address [%s] is already associated", allocID)
		}
	} else if c.p.get("AllocationId") != "" {
		return nil, errorf("InvalidParameterCombination", "AllocationId is not supported for private NAT gateways")
	}
	n := &NatGateway{
		NatGatewayId: newID("nat"), SubnetId: s.SubnetId, VpcId: s.VpcId, State: "available",
		ConnectivityType: connectivity, AvailabilityMode: "zonal", CreateTime: now(), Tags: tags,
	}
	// A public NAT gateway needs the VPC's internet gateway. It waits in
	// "pending" for one to be attached (Terraform often creates both at
	// once) and fails if none turns up, as in AWS.
	if connectivity == "public" && !c.vpcHasIGW(s.VpcId) {
		n.State = "pending"
	}
	eni, err := c.createEni(s, eniSpec{
		ip: c.p.get("PrivateIpAddress"), description: "Interface for NAT Gateway " + n.NatGatewayId,
		ifType: "nat_gateway", requester: "managed-nat", noGroups: true,
	})
	if err != nil {
		return nil, err
	}
	addr := NatAddr{NetworkInterfaceId: eni.NetworkInterfaceId, PrivateIp: eni.PrivateIpAddress, IsPrimary: true, Status: "succeeded"}
	if eip != nil {
		assocID := newID("eipassoc")
		addr.AllocationId, addr.PublicIp, addr.AssociationId = eip.AllocationId, eip.PublicIp, assocID
		eip.AssociationId, eip.NetworkInterfaceId, eip.NetworkInterfaceOwnerId, eip.PrivateIpAddress = assocID, eni.NetworkInterfaceId, c.account, eni.PrivateIpAddress
		eni.setAssociation(eip, c.account)
		if err := c.put(tEIP, eip); err != nil {
			return nil, err
		}
		if err := c.put(tENI, eni); err != nil {
			return nil, err
		}
	}
	n.Addresses = []NatAddr{addr}
	if err := c.put(tNAT, n); err != nil {
		return nil, err
	}
	return struct {
		N *NatGateway `xml:"natGateway"`
	}{n}, nil
}

func (c *call) vpcHasIGW(vpcID string) bool {
	igws, _ := all[InternetGateway](c, tIGW)
	return slices.ContainsFunc(igws, func(g *InternetGateway) bool {
		return slices.ContainsFunc(g.Attachments, func(a IgwAttach) bool { return a.VpcId == vpcID })
	})
}

func deleteNatGateway(c *call) (any, error) {
	n, err := must[NatGateway](c, tNAT, c.p.get("NatGatewayId"), "NatGatewayNotFound")
	if err != nil {
		return nil, err
	}
	if err := c.releaseNatAddresses(n); err != nil {
		return nil, err
	}
	if err := c.remove(tNAT, n.NatGatewayId); err != nil {
		return nil, err
	}
	return struct {
		ID string `xml:"natGatewayId"`
	}{n.NatGatewayId}, nil
}

// natPendingTimeout is how long a public NAT gateway waits for an
// internet gateway before failing.
const natPendingTimeout = 45 * time.Second

// settleNat moves a pending NAT gateway on: available once the VPC has an
// internet gateway, failed (giving its address back) after the timeout.
func (c *call) settleNat(n *NatGateway) error {
	if n.State != "pending" {
		return nil
	}
	if c.vpcHasIGW(n.VpcId) {
		n.State = "available"
		return c.put(tNAT, n)
	}
	created, _ := time.Parse("2006-01-02T15:04:05.000Z", n.CreateTime)
	if time.Since(created) < natPendingTimeout {
		return nil
	}
	n.State, n.FailureCode = "failed", "Gateway.NotAttached"
	n.FailureMessage = fmt.Sprintf("Network %s has no Internet gateway attached", n.VpcId)
	if err := c.releaseNatAddresses(n); err != nil {
		return err
	}
	return c.put(tNAT, n)
}

// releaseNatAddresses frees a NAT gateway's interface and elastic IP.
func (c *call) releaseNatAddresses(n *NatGateway) error {
	for _, a := range n.Addresses {
		if a.AllocationId != "" {
			if eip, err := c.address(a.AllocationId); err == nil {
				eip.clearAssociation()
				if err := c.put(tEIP, eip); err != nil {
					return err
				}
			}
		}
		if a.NetworkInterfaceId != "" {
			if err := c.remove(tENI, a.NetworkInterfaceId); err != nil {
				return err
			}
		}
	}
	n.Addresses = nil
	return nil
}

func describeNatGateways(c *call) (any, error) {
	pending, err := all[NatGateway](c, tNAT)
	if err != nil {
		return nil, err
	}
	for _, n := range pending {
		if err := c.settleNat(n); err != nil {
			return nil, err
		}
	}
	ns, err := describe[NatGateway](c, tNAT, c.p.list("NatGatewayId"), "NatGatewayNotFound")
	if err != nil {
		return nil, err
	}
	return struct {
		Ns []*NatGateway `xml:"natGatewaySet>item"`
	}{ns}, nil
}

// Elastic IPs.

func (a *Address) objectID() string { return a.AllocationId }
func (a *Address) tagList() *[]Tag  { return &a.Tags }
func (a *Address) filterValues() map[string][]string {
	return map[string][]string{
		"allocation-id": {a.AllocationId}, "association-id": nonEmpty(a.AssociationId), "domain": {a.Domain},
		"instance-id": nonEmpty(a.InstanceId), "network-interface-id": nonEmpty(a.NetworkInterfaceId),
		"network-interface-owner-id": nonEmpty(a.NetworkInterfaceOwnerId), "private-ip-address": nonEmpty(a.PrivateIpAddress),
		"public-ip": {a.PublicIp}, "network-border-group": {a.NetworkBorderGroup}, "public-ipv4-pool": {a.PublicIpv4Pool},
	}
}

func (a *Address) clearAssociation() {
	a.AssociationId, a.InstanceId, a.NetworkInterfaceId, a.NetworkInterfaceOwnerId, a.PrivateIpAddress = "", "", "", "", ""
}

func (c *call) address(id string) (*Address, error) {
	return must[Address](c, tEIP, id, "InvalidAllocationID.NotFound")
}

// publicIP picks an unused address from 54.0.0.0/8 for this namespace.
func (c *call) publicIP() (string, error) {
	addrs, err := all[Address](c, tEIP)
	if err != nil {
		return "", err
	}
	for {
		b := make([]byte, 3)
		rand.Read(b)
		ip := fmt.Sprintf("54.%d.%d.%d", b[0], b[1], 1+b[2]%254)
		if !slices.ContainsFunc(addrs, func(a *Address) bool { return a.PublicIp == ip }) {
			return ip, nil
		}
	}
}

func allocateAddress(c *call) (any, error) {
	if d := c.p.get("Domain"); d != "" && d != "vpc" {
		return nil, invalidParam("Domain", d)
	}
	if c.p.get("Address") != "" || c.p.get("CustomerOwnedIpv4Pool") != "" || c.p.get("IpamPoolId") != "" {
		return nil, errorf("UnsupportedOperation", "Only Amazon-provided addresses are supported")
	}
	tags, err := c.tagSpec("elastic-ip")
	if err != nil {
		return nil, err
	}
	ip, err := c.publicIP()
	if err != nil {
		return nil, err
	}
	nbg := c.p.get("NetworkBorderGroup")
	if nbg == "" {
		nbg = c.region
	}
	a := &Address{
		AllocationId: newID("eipalloc"), PublicIp: ip, Domain: "vpc", PublicIpv4Pool: "amazon",
		NetworkBorderGroup: nbg, Tags: tags,
	}
	if err := c.put(tEIP, a); err != nil {
		return nil, err
	}
	return struct {
		PublicIp           string `xml:"publicIp"`
		AllocationId       string `xml:"allocationId"`
		Domain             string `xml:"domain"`
		PublicIpv4Pool     string `xml:"publicIpv4Pool"`
		NetworkBorderGroup string `xml:"networkBorderGroup"`
	}{a.PublicIp, a.AllocationId, a.Domain, a.PublicIpv4Pool, a.NetworkBorderGroup}, nil
}

func (c *call) addressByParams() (*Address, error) {
	if id := c.p.get("AllocationId"); id != "" {
		return c.address(id)
	}
	ip := c.p.get("PublicIp")
	if ip == "" {
		return nil, missing("AllocationId")
	}
	addrs, err := all[Address](c, tEIP)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if a.PublicIp == ip {
			return a, nil
		}
	}
	return nil, errorf("InvalidAddress.NotFound", "Address '%s' not found.", ip)
}

func releaseAddress(c *call) (any, error) {
	a, err := c.addressByParams()
	if err != nil {
		return nil, err
	}
	if a.AssociationId != "" {
		return nil, errorf("InvalidIPAddress.InUse", "Address %s is in use.", a.PublicIp)
	}
	return nil, c.remove(tEIP, a.AllocationId)
}

func describeAddresses(c *call) (any, error) {
	addrs, err := describe[Address](c, tEIP, c.p.list("AllocationId"), "InvalidAllocationID.NotFound")
	if err != nil {
		return nil, err
	}
	if ips := c.p.list("PublicIp"); len(ips) > 0 {
		var picked []*Address
		for _, ip := range ips {
			i := slices.IndexFunc(addrs, func(a *Address) bool { return a.PublicIp == ip })
			if i < 0 {
				return nil, errorf("InvalidAddress.NotFound", "Address '%s' not found.", ip)
			}
			picked = append(picked, addrs[i])
		}
		addrs = picked
	}
	return struct {
		As []*Address `xml:"addressesSet>item"`
	}{addrs}, nil
}

func describeAddressesAttribute(c *call) (any, error) {
	type attr struct {
		PublicIp     string `xml:"publicIp"`
		AllocationId string `xml:"allocationId"`
	}
	var out []attr
	for _, id := range c.p.list("AllocationId") {
		a, err := c.address(id)
		if err != nil {
			return nil, err
		}
		out = append(out, attr{a.PublicIp, a.AllocationId})
	}
	return struct {
		Attrs []attr `xml:"addressSet>item"`
	}{out}, nil
}

func associateAddress(c *call) (any, error) {
	a, err := c.addressByParams()
	if err != nil {
		return nil, err
	}
	var eni *NetworkInterface
	switch {
	case c.p.get("NetworkInterfaceId") != "":
		if eni, err = c.eni(c.p.get("NetworkInterfaceId")); err != nil {
			return nil, err
		}
	case c.p.get("InstanceId") != "":
		inst, err := c.instance(c.p.get("InstanceId"))
		if err != nil {
			return nil, err
		}
		if len(inst.Enis) == 0 {
			return nil, errorf("InvalidInstanceID", "The instance %s has no network interface", inst.InstanceId)
		}
		if eni, err = c.eni(inst.Enis[0]); err != nil {
			return nil, err
		}
	default:
		return nil, missing("InstanceId")
	}
	if !c.vpcHasIGW(eni.VpcId) {
		return nil, errorf("Gateway.NotAttached", "Network %s is not attached to any internet gateway", eni.VpcId)
	}
	privateIP := c.p.get("PrivateIpAddress")
	if privateIP == "" {
		privateIP = eni.PrivateIpAddress
	} else if !slices.ContainsFunc(eni.PrivateIpAddresses, func(p EniPrivateIp) bool { return p.PrivateIpAddress == privateIP }) {
		return nil, errorf("InvalidParameterValue", "The private IP address %s is not on network interface %s", privateIP, eni.NetworkInterfaceId)
	}
	if a.AssociationId != "" && !c.p.boolean("AllowReassociation") {
		return nil, errorf("Resource.AlreadyAssociated", "resource %s is already associated with associate-id %s", a.AllocationId, a.AssociationId)
	}
	if a.AssociationId != "" {
		if err := c.unmapAddress(a); err != nil {
			return nil, err
		}
	}
	a.AssociationId = newID("eipassoc")
	a.NetworkInterfaceId, a.NetworkInterfaceOwnerId, a.PrivateIpAddress = eni.NetworkInterfaceId, c.account, privateIP
	if eni.Attachment != nil {
		a.InstanceId = eni.Attachment.InstanceId
	}
	if privateIP == eni.PrivateIpAddress {
		eni.setAssociation(a, c.account)
	}
	for i := range eni.PrivateIpAddresses {
		if eni.PrivateIpAddresses[i].PrivateIpAddress == privateIP {
			eni.PrivateIpAddresses[i].Association = eniAssoc(a, c.account)
		}
	}
	if err := c.put(tENI, eni); err != nil {
		return nil, err
	}
	if err := c.put(tEIP, a); err != nil {
		return nil, err
	}
	return struct {
		AssociationId string `xml:"associationId"`
	}{a.AssociationId}, nil
}

// unmapAddress removes an address's association from its interface.
func (c *call) unmapAddress(a *Address) error {
	if eni, err := c.eni(a.NetworkInterfaceId); err == nil {
		if eni.Association != nil && eni.Association.AllocationId == a.AllocationId {
			eni.Association = nil
		}
		for i := range eni.PrivateIpAddresses {
			if as := eni.PrivateIpAddresses[i].Association; as != nil && as.AllocationId == a.AllocationId {
				eni.PrivateIpAddresses[i].Association = nil
			}
		}
		if err := c.put(tENI, eni); err != nil {
			return err
		}
	}
	a.clearAssociation()
	return nil
}

func disassociateAddress(c *call) (any, error) {
	id := c.p.get("AssociationId")
	addrs, err := all[Address](c, tEIP)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if (id != "" && a.AssociationId == id) || (id == "" && a.PublicIp == c.p.get("PublicIp") && a.AssociationId != "") {
			if eni, err := c.eni(a.NetworkInterfaceId); err == nil && eni.InterfaceType == "nat_gateway" {
				return nil, errorf("InvalidParameterValue", "You cannot disassociate an Elastic IP address that is associated with a NAT gateway")
			}
			if err := c.unmapAddress(a); err != nil {
				return nil, err
			}
			return nil, c.put(tEIP, a)
		}
	}
	return nil, errorf("InvalidAssociationID.NotFound", "The association ID '%s' does not exist", id)
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
