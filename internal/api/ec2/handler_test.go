// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"opensnack/internal/resource"
	"opensnack/internal/util"
)

type client struct {
	t  *testing.T
	h  *Handler
	ns string
}

func newClient(t *testing.T) *client {
	return &client{t: t, h: NewHandler(resource.NewMemStore()), ns: "tester"}
}

// call sends an EC2 Query request; kv alternates names and values.
func (c *client) call(action string, kv ...string) (int, string) {
	c.t.Helper()
	form := url.Values{"Action": {action}, "Version": {APIVersion}}
	for i := 0; i+1 < len(kv); i += 2 {
		form.Add(kv[i], kv[i+1])
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(util.WithIdentity(req.Context(), &util.Identity{Namespace: c.ns, AccountID: "123456789012", Region: "us-east-1"}))
	rec := httptest.NewRecorder()
	c.h.Dispatch(rec, req)
	return rec.Code, rec.Body.String()
}

// ok calls and fails the test on an error response.
func (c *client) ok(action string, kv ...string) string {
	c.t.Helper()
	code, body := c.call(action, kv...)
	if code != http.StatusOK {
		c.t.Fatalf("%s: %d %s", action, code, body)
	}
	return body
}

// fails calls and checks the EC2 error code.
func (c *client) fails(wantCode, action string, kv ...string) {
	c.t.Helper()
	code, body := c.call(action, kv...)
	var e struct {
		Code string `xml:"Errors>Error>Code"`
	}
	xml.Unmarshal([]byte(body), &e)
	if code != http.StatusBadRequest || e.Code != wantCode {
		c.t.Fatalf("%s: want %s, got %d %s", action, wantCode, code, body)
	}
}

func field(body, name string) string {
	m := regexp.MustCompile("<" + name + ">([^<]*)</" + name + ">").FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestParseCIDR(t *testing.T) {
	for s, want := range map[string]bool{
		"10.0.0.0/16": true, "10.0.0.1/16": false, "10.0.0.0/33": false, "nope": false, "::/0": false, "172.31.16.0/20": true,
	} {
		if _, ok := parseCIDR(s); ok != want {
			t.Errorf("parseCIDR(%q) = %v, want %v", s, ok, want)
		}
	}
}

func TestSubnetCIDRValidation(t *testing.T) {
	c := newClient(t)
	vpc := field(c.ok("CreateVpc", "CidrBlock", "10.0.0.0/16"), "vpcId")

	c.fails("InvalidVpc.Range", "CreateVpc", "CidrBlock", "10.0.0.0/8")
	c.fails("InvalidParameterValue", "CreateVpc", "CidrBlock", "10.0.0.1/16")

	// Outside the VPC, too small, too large.
	c.fails("InvalidSubnet.Range", "CreateSubnet", "VpcId", vpc, "CidrBlock", "10.1.0.0/24")
	c.fails("InvalidSubnet.Range", "CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/29")
	c.fails("InvalidSubnet.Range", "CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/15")
	c.fails("InvalidParameterValue", "CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/24", "AvailabilityZone", "us-east-1z")
	c.fails("InvalidParameterValue", "CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/24", "AvailabilityZone", "eu-west-1a")

	c.ok("CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/24", "AvailabilityZone", "us-east-1a")
	// Overlapping in either direction.
	c.fails("InvalidSubnet.Conflict", "CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.128/25")
	c.fails("InvalidSubnet.Conflict", "CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/23")
	c.ok("CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.1.0/24", "AvailabilityZoneId", "use1-az2")

	// A secondary CIDR makes room; overlapping secondaries are refused.
	c.fails("InvalidVpc.Range", "AssociateVpcCidrBlock", "VpcId", vpc, "CidrBlock", "10.0.128.0/17")
	c.ok("AssociateVpcCidrBlock", "VpcId", vpc, "CidrBlock", "10.1.0.0/16")
	c.ok("CreateSubnet", "VpcId", vpc, "CidrBlock", "10.1.0.0/24")

	// The same range is fine in another VPC.
	other := field(c.ok("CreateVpc", "CidrBlock", "10.0.0.0/16"), "vpcId")
	c.ok("CreateSubnet", "VpcId", other, "CidrBlock", "10.0.0.0/24")
}

func TestAllocateIP(t *testing.T) {
	p := netip.MustParsePrefix("10.0.1.0/28")
	used := map[string]bool{}
	var got []string
	for {
		a, err := allocateIP(p, used)
		if err != nil {
			break
		}
		used[a.String()] = true
		got = append(got, a.String())
	}
	// 16 addresses, less the first four and the broadcast address.
	if len(got) != 11 || got[0] != "10.0.1.4" || got[len(got)-1] != "10.0.1.14" {
		t.Fatalf("allocated %v", got)
	}
	for _, s := range []string{"10.0.1.0", "10.0.1.3", "10.0.1.15", "10.0.2.4"} {
		if usable(p, netip.MustParseAddr(s)) {
			t.Errorf("%s should not be usable", s)
		}
	}
}

func TestInterfaceAddresses(t *testing.T) {
	c := newClient(t)
	vpc := field(c.ok("CreateVpc", "CidrBlock", "10.0.0.0/16"), "vpcId")
	subnet := field(c.ok("CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/28"), "subnetId")

	first := c.ok("CreateNetworkInterface", "SubnetId", subnet)
	if ip := field(first, "privateIpAddress"); ip != "10.0.0.4" {
		t.Fatalf("first address %s, want 10.0.0.4", ip)
	}
	c.fails("InvalidParameterValue", "CreateNetworkInterface", "SubnetId", subnet, "PrivateIpAddress", "10.0.0.2")
	c.fails("InvalidParameterValue", "CreateNetworkInterface", "SubnetId", subnet, "PrivateIpAddress", "10.0.0.15")
	c.fails("InvalidParameterValue", "CreateNetworkInterface", "SubnetId", subnet, "PrivateIpAddress", "10.0.1.5")
	c.fails("InvalidIPAddress.InUse", "CreateNetworkInterface", "SubnetId", subnet, "PrivateIpAddress", "10.0.0.4")
	c.ok("CreateNetworkInterface", "SubnetId", subnet, "PrivateIpAddress", "10.0.0.14")
	if ip := field(c.ok("CreateNetworkInterface", "SubnetId", subnet), "privateIpAddress"); ip != "10.0.0.5" {
		t.Fatalf("next address %s, want 10.0.0.5", ip)
	}

	// 11 usable, 3 taken.
	if n := field(c.ok("DescribeSubnets", "SubnetId.1", subnet), "availableIpAddressCount"); n != "8" {
		t.Fatalf("available %s, want 8", n)
	}
	for i := 0; i < 8; i++ {
		c.ok("CreateNetworkInterface", "SubnetId", subnet)
	}
	c.fails("InsufficientFreeAddressesInSubnet", "CreateNetworkInterface", "SubnetId", subnet)
	c.fails("DependencyViolation", "DeleteSubnet", "SubnetId", subnet)

	// An instance takes its address from the subnet too.
	img := images("us-east-1")[1].ImageId
	c.fails("InsufficientFreeAddressesInSubnet", "RunInstances", "ImageId", img, "InstanceType", "t3.micro", "SubnetId", subnet, "MinCount", "1", "MaxCount", "1")
}

func TestDescribeTagsFilters(t *testing.T) {
	c := newClient(t)
	vpc := field(c.ok("CreateVpc", "CidrBlock", "10.0.0.0/16",
		"TagSpecification.1.ResourceType", "vpc",
		"TagSpecification.1.Tag.1.Key", "Name", "TagSpecification.1.Tag.1.Value", "main",
		"TagSpecification.1.Tag.2.Key", "Env", "TagSpecification.1.Tag.2.Value", "prod"), "vpcId")
	subnet := field(c.ok("CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/24"), "subnetId")
	c.ok("CreateTags", "ResourceId.1", subnet, "Tag.1.Key", "Name", "Tag.1.Value", "web", "Tag.2.Key", "Empty", "Tag.2.Value", "")

	count := func(kv ...string) int {
		t.Helper()
		return strings.Count(c.ok("DescribeTags", kv...), "<resourceId>")
	}
	if n := count(); n != 4 {
		t.Fatalf("all tags: %d", n)
	}
	if n := count("Filter.1.Name", "resource-id", "Filter.1.Value.1", vpc); n != 2 {
		t.Errorf("resource-id: %d", n)
	}
	if n := count("Filter.1.Name", "resource-type", "Filter.1.Value.1", "subnet"); n != 2 {
		t.Errorf("resource-type: %d", n)
	}
	if n := count("Filter.1.Name", "key", "Filter.1.Value.1", "Name"); n != 2 {
		t.Errorf("key: %d", n)
	}
	if n := count("Filter.1.Name", "key", "Filter.1.Value.1", "Name", "Filter.2.Name", "value", "Filter.2.Value.1", "w*"); n != 1 {
		t.Errorf("key and value wildcard: %d", n)
	}
	if n := count("Filter.1.Name", "value", "Filter.1.Value.1", "main", "Filter.1.Value.2", "prod"); n != 2 {
		t.Errorf("value any-of: %d", n)
	}
	c.fails("InvalidParameterValue", "DescribeTags", "Filter.1.Name", "bogus", "Filter.1.Value.1", "x")

	// DeleteTags with a value only removes a matching tag.
	c.ok("DeleteTags", "ResourceId.1", vpc, "Tag.1.Key", "Env", "Tag.1.Value", "staging")
	if n := count("Filter.1.Name", "resource-id", "Filter.1.Value.1", vpc); n != 2 {
		t.Errorf("after non-matching delete: %d", n)
	}
	c.ok("DeleteTags", "ResourceId.1", vpc, "Tag.1.Key", "Env")
	if n := count("Filter.1.Name", "resource-id", "Filter.1.Value.1", vpc); n != 1 {
		t.Errorf("after delete: %d", n)
	}

	// Describe* filters on tags.
	body := c.ok("DescribeSubnets", "Filter.1.Name", "tag:Name", "Filter.1.Value.1", "web")
	if !strings.Contains(body, subnet) {
		t.Errorf("tag:Name filter missed the subnet: %s", body)
	}
	body = c.ok("DescribeSubnets", "Filter.1.Name", "tag-key", "Filter.1.Value.1", "Nope")
	if strings.Contains(body, subnet) {
		t.Errorf("tag-key filter matched: %s", body)
	}
	c.fails("InvalidVpcID.NotFound", "CreateTags", "ResourceId.1", "vpc-0000", "Tag.1.Key", "a", "Tag.1.Value", "b")
	c.fails("InvalidParameterValue", "CreateTags", "ResourceId.1", vpc, "Tag.1.Key", "aws:reserved", "Tag.1.Value", "b")
}

func TestDefaultVPCLeavesNothingBehind(t *testing.T) {
	c := newClient(t)
	body := c.ok("DescribeVpcs", "Filter.1.Name", "isDefault", "Filter.1.Value.1", "true")
	if field(body, "cidrBlock") != "172.31.0.0/16" {
		t.Fatalf("default VPC: %s", body)
	}
	subnets := c.ok("DescribeSubnets", "Filter.1.Name", "default-for-az", "Filter.1.Value.1", "true")
	if n := strings.Count(subnets, "<subnetId>"); n != 6 {
		t.Fatalf("%d default subnets in us-east-1", n)
	}
	if !strings.Contains(subnets, "172.31.80.0/20") {
		t.Fatalf("default subnet CIDRs: %s", subnets)
	}
	insp := c.h.Store.(resource.Inspector)
	if n, _ := insp.Count(c.ns); len(n) != 0 {
		t.Fatalf("reading the default VPC stored %v", n)
	}

	// A security group with no VpcId lands in the default VPC; a launch
	// with no subnet lands in a default subnet.
	sg := field(c.ok("CreateSecurityGroup", "GroupName", "g", "GroupDescription", "d"), "groupId")
	img := images("us-east-1")[1].ImageId
	run := c.ok("RunInstances", "ImageId", img, "InstanceType", "t3.micro", "MinCount", "1", "MaxCount", "1", "SecurityGroupId.1", sg)
	if !strings.Contains(run, "172.31.") || field(run, "ipAddress") == "" {
		t.Fatalf("instance in default VPC: %s", run)
	}
	c.ok("TerminateInstances", "InstanceId.1", field(run, "instanceId"))
	c.ok("DeleteSecurityGroup", "GroupId", sg)
	if n, _ := insp.Count(c.ns); len(n) != 0 {
		t.Fatalf("left behind %v", n)
	}
	if !strings.Contains(c.ok("DescribeInstances", "InstanceId.1", field(run, "instanceId")), "terminated") {
		t.Fatal("terminated instance should stay visible")
	}
}

func TestSecurityGroupRules(t *testing.T) {
	c := newClient(t)
	vpc := field(c.ok("CreateVpc", "CidrBlock", "10.0.0.0/16"), "vpcId")
	sg := field(c.ok("CreateSecurityGroup", "GroupName", "web", "GroupDescription", "web", "VpcId", vpc), "groupId")
	perm := []string{"GroupId", sg, "IpPermissions.1.IpProtocol", "6", "IpPermissions.1.FromPort", "443", "IpPermissions.1.ToPort", "443",
		"IpPermissions.1.IpRanges.1.CidrIp", "10.0.0.0/8", "IpPermissions.1.IpRanges.2.CidrIp", "192.168.0.0/16"}
	body := c.ok("AuthorizeSecurityGroupIngress", perm...)
	if strings.Count(body, "<securityGroupRuleId>") != 2 || !strings.Contains(body, "<ipProtocol>tcp</ipProtocol>") {
		t.Fatalf("authorize: %s", body)
	}
	c.fails("InvalidPermission.Duplicate", "AuthorizeSecurityGroupIngress", perm...)
	desc := c.ok("DescribeSecurityGroups", "GroupId.1", sg)
	if strings.Count(desc, "<cidrIp>") != 3 || strings.Count(desc, "<fromPort>443</fromPort>") != 1 {
		t.Fatalf("grouped permissions: %s", desc)
	}
	c.ok("RevokeSecurityGroupIngress", perm...)
	c.fails("InvalidPermission.NotFound", "RevokeSecurityGroupIngress", perm...)
	c.fails("CannotDelete", "DeleteSecurityGroup", "GroupName", "default")
}

func TestNatGatewayWaitsForInternetGateway(t *testing.T) {
	c := newClient(t)
	vpc := field(c.ok("CreateVpc", "CidrBlock", "10.0.0.0/16"), "vpcId")
	subnet := field(c.ok("CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.0.0/24"), "subnetId")
	eip := field(c.ok("AllocateAddress", "Domain", "vpc"), "allocationId")
	c.fails("MissingParameter", "CreateNatGateway", "SubnetId", subnet)
	nat := field(c.ok("CreateNatGateway", "SubnetId", subnet, "AllocationId", eip), "natGatewayId")
	if s := field(c.ok("DescribeNatGateways", "NatGatewayId.1", nat), "state"); s != "pending" {
		t.Fatalf("without an internet gateway: %s", s)
	}
	c.fails("Resource.AlreadyAssociated", "CreateNatGateway", "SubnetId", subnet, "AllocationId", eip)
	igw := field(c.ok("CreateInternetGateway"), "internetGatewayId")
	c.ok("AttachInternetGateway", "InternetGatewayId", igw, "VpcId", vpc)
	body := c.ok("DescribeNatGateways", "NatGatewayId.1", nat)
	if field(body, "state") != "available" || field(body, "privateIp") != "10.0.0.4" {
		t.Fatalf("after attaching: %s", body)
	}
	c.fails("InvalidIPAddress.InUse", "ReleaseAddress", "AllocationId", eip)
	c.fails("DependencyViolation", "DetachInternetGateway", "InternetGatewayId", igw, "VpcId", vpc)
	c.ok("DeleteNatGateway", "NatGatewayId", nat)
	c.ok("DetachInternetGateway", "InternetGatewayId", igw, "VpcId", vpc)
	c.ok("ReleaseAddress", "AllocationId", eip)
	c.fails("NatGatewayNotFound", "DescribeNatGateways", "NatGatewayId.1", nat)
}

func TestReachability(t *testing.T) {
	c := newClient(t)
	vpc := field(c.ok("CreateVpc", "CidrBlock", "10.0.0.0/16"), "vpcId")
	public := field(c.ok("CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.1.0/24"), "subnetId")
	private := field(c.ok("CreateSubnet", "VpcId", vpc, "CidrBlock", "10.0.2.0/24"), "subnetId")
	igw := field(c.ok("CreateInternetGateway"), "internetGatewayId")
	c.ok("AttachInternetGateway", "InternetGatewayId", igw, "VpcId", vpc)
	rt := field(c.ok("CreateRouteTable", "VpcId", vpc), "routeTableId")
	c.ok("CreateRoute", "RouteTableId", rt, "DestinationCidrBlock", "0.0.0.0/0", "GatewayId", igw)
	c.ok("AssociateRouteTable", "RouteTableId", rt, "SubnetId", public)

	web := field(c.ok("CreateSecurityGroup", "GroupName", "web", "GroupDescription", "web", "VpcId", vpc), "groupId")
	c.ok("AuthorizeSecurityGroupIngress", "GroupId", web, "IpPermissions.1.IpProtocol", "tcp", "IpPermissions.1.FromPort", "443",
		"IpPermissions.1.ToPort", "443", "IpPermissions.1.IpRanges.1.CidrIp", "0.0.0.0/0")
	db := field(c.ok("CreateSecurityGroup", "GroupName", "db", "GroupDescription", "db", "VpcId", vpc), "groupId")
	c.ok("AuthorizeSecurityGroupIngress", "GroupId", db, "IpPermissions.1.IpProtocol", "tcp", "IpPermissions.1.FromPort", "5432",
		"IpPermissions.1.ToPort", "5432", "IpPermissions.1.Groups.1.GroupId", web)

	img := images("us-east-1")[1].ImageId
	webInst := field(c.ok("RunInstances", "ImageId", img, "InstanceType", "t3.micro", "MinCount", "1", "MaxCount", "1",
		"NetworkInterface.1.DeviceIndex", "0", "NetworkInterface.1.SubnetId", public,
		"NetworkInterface.1.AssociatePublicIpAddress", "true", "NetworkInterface.1.SecurityGroupId.1", web), "instanceId")
	dbInst := field(c.ok("RunInstances", "ImageId", img, "InstanceType", "t3.micro", "MinCount", "1", "MaxCount", "1",
		"SubnetId", private, "SecurityGroupId.1", db), "instanceId")

	check := func(src, dst string, port int, want bool) *ReachResult {
		t.Helper()
		res, err := c.h.Reachability(c.ns, "us-east-1", src, dst, "tcp", port)
		if err != nil {
			t.Fatal(err)
		}
		if res.Reachable != want {
			t.Fatalf("%s -> %s:%d reachable=%v, want %v (%s)", src, dst, port, res.Reachable, want, res.Reason)
		}
		return res
	}
	check("internet", webInst, 443, true)
	check("internet", webInst, 22, false)
	check(webInst, dbInst, 5432, true)
	check(dbInst, webInst, 443, true)
	check(webInst, dbInst, 22, false)
	if r := check("internet", dbInst, 5432, false); !strings.Contains(r.Reason, "public IP") {
		t.Errorf("reason: %s", r.Reason)
	}
	check(dbInst, "internet", 443, false)

	// A stateless ACL on the private subnet: allowing 5432 in isn't enough
	// until the responses to ephemeral ports can get out.
	acl := field(c.ok("CreateNetworkAcl", "VpcId", vpc), "networkAclId")
	c.ok("CreateNetworkAclEntry", "NetworkAclId", acl, "RuleNumber", "100", "Protocol", "6", "RuleAction", "allow",
		"Egress", "false", "CidrBlock", "10.0.1.0/24", "PortRange.From", "5432", "PortRange.To", "5432")
	privateAssoc := func() string {
		body := c.ok("DescribeNetworkAcls", "Filter.1.Name", "association.subnet-id", "Filter.1.Value.1", private)
		m := regexp.MustCompile(`<networkAclAssociationId>(aclassoc-[0-9a-f]+)</networkAclAssociationId>\s*<networkAclId>acl-[0-9a-f]+</networkAclId>\s*<subnetId>` + private).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no ACL association for the private subnet: %s", body)
		}
		return m[1]
	}
	c.ok("ReplaceNetworkAclAssociation", "AssociationId", privateAssoc(), "NetworkAclId", acl)
	if r := check(webInst, dbInst, 5432, false); !strings.Contains(r.Reason, "ephemeral") {
		t.Errorf("reason: %s", r.Reason)
	}
	c.ok("CreateNetworkAclEntry", "NetworkAclId", acl, "RuleNumber", "100", "Protocol", "6", "RuleAction", "allow",
		"Egress", "true", "CidrBlock", "10.0.1.0/24", "PortRange.From", "1024", "PortRange.To", "65535")
	check(webInst, dbInst, 5432, true)

	// The private subnet reaches out through a NAT gateway once it is back
	// on the default ACL.
	defaultACL := field(c.ok("DescribeNetworkAcls", "Filter.1.Name", "default", "Filter.1.Value.1", "true",
		"Filter.2.Name", "vpc-id", "Filter.2.Value.1", vpc), "networkAclId")
	c.ok("ReplaceNetworkAclAssociation", "AssociationId", privateAssoc(), "NetworkAclId", defaultACL)
	eip := field(c.ok("AllocateAddress", "Domain", "vpc"), "allocationId")
	nat := field(c.ok("CreateNatGateway", "SubnetId", public, "AllocationId", eip), "natGatewayId")
	main := field(c.ok("DescribeRouteTables", "Filter.1.Name", "association.main", "Filter.1.Value.1", "true",
		"Filter.2.Name", "vpc-id", "Filter.2.Value.1", vpc), "routeTableId")
	c.ok("CreateRoute", "RouteTableId", main, "DestinationCidrBlock", "0.0.0.0/0", "NatGatewayId", nat)
	check(dbInst, "internet", 443, true)
	check(dbInst, "8.8.8.8", 53, true)
}
