// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import "fmt"

// The default VPC is generated, not stored: every namespace and region has
// one, built the same way each time from stable IDs, so reading it leaves
// nothing behind. Changing one of its resources (tags, rules, a launched
// instance's ENI is separate) writes that resource to the store, after which
// the stored copy wins. Deleting one stores a tombstone.

const defaultVpcCIDR = "172.31.0.0/16"

func (c *call) stableIDs(prefix, name string) string {
	return stableID(prefix, c.ns, "default-vpc", name)
}

func (c *call) defaultVpcID() string { return c.stableIDs("vpc", "vpc") }

// defaults returns the generated default VPC resources by store type and ID.
func (c *call) defaults() map[string]map[string]object {
	if c.defs != nil {
		return c.defs
	}
	c.defs = map[string]map[string]object{}
	add := func(typ string, o object) {
		if c.defs[typ] == nil {
			c.defs[typ] = map[string]object{}
		}
		c.defs[typ][o.objectID()] = o
	}

	vpc, sg, rtb, acl := c.newVpc(defaultVpcCIDR, true, c.stableIDs)
	igw := &InternetGateway{
		InternetGatewayId: c.stableIDs("igw", "igw"), OwnerId: c.account,
		Attachments: []IgwAttach{{VpcId: vpc.VpcId, State: "available"}},
	}
	rtb.Routes = append(rtb.Routes, Route{DestinationCidrBlock: "0.0.0.0/0", GatewayId: igw.InternetGatewayId, State: "active", Origin: "CreateRoute"})
	add(tVpc, vpc)
	add(tSG, sg)
	add(tRTB, rtb)
	add(tNACL, acl)
	add(tIGW, igw)
	for i, z := range zones(c.region) {
		s := &Subnet{
			SubnetId: c.stableIDs("subnet", z.Name), VpcId: vpc.VpcId, OwnerId: c.account, State: "available",
			CidrBlock: fmt.Sprintf("172.31.%d.0/20", i*16), AvailabilityZone: z.Name, AvailabilityZoneId: z.ID,
			DefaultForAz: true, MapPublicIpOnLaunch: true,
			PrivateDnsNameOptionsOnLaunch: PrivateDnsOpts{HostnameType: "ip-name"},
			NetworkAclId:                  acl.NetworkAclId, NaclAssociation: c.stableIDs("aclassoc", z.Name),
		}
		s.SubnetArn = c.arn("subnet/" + s.SubnetId)
		add(tSubnet, s)
	}
	return c.defs
}

func createDefaultVpc(c *call) (any, error) {
	var vpc Vpc
	ok, err := c.get(tVpc, c.defaultVpcID(), &vpc)
	if err != nil {
		return nil, err
	}
	if ok {
		return nil, errorf("DefaultVpcAlreadyExists", "A Default VPC already exists for this account in this region.")
	}
	// Lift the tombstones so the generated resources reappear.
	for _, byID := range c.defaults() {
		for id := range byID {
			c.h.Store.Delete(id, service, tDeleted, c.ns)
		}
	}
	c.deleted = nil
	if _, err := c.get(tVpc, c.defaultVpcID(), &vpc); err != nil {
		return nil, err
	}
	return struct {
		Vpc *Vpc `xml:"vpc"`
	}{&vpc}, nil
}
