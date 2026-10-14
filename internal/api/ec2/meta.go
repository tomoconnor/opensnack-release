// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"slices"
	"strings"
)

// Regions, availability zones, AMIs and instance types: fixed catalogues.

type regionInfo struct {
	name  string
	code  string // zone ID prefix, e.g. use1
	zones string // zone letters
	optIn bool   // opt-in region
}

var regions = []regionInfo{
	{"us-east-1", "use1", "abcdef", false},
	{"us-east-2", "use2", "abc", false},
	{"us-west-1", "usw1", "ac", false},
	{"us-west-2", "usw2", "abcd", false},
	{"ca-central-1", "cac1", "abd", false},
	{"eu-west-1", "euw1", "abc", false},
	{"eu-west-2", "euw2", "abc", false},
	{"eu-west-3", "euw3", "abc", false},
	{"eu-central-1", "euc1", "abc", false},
	{"eu-north-1", "eun1", "abc", false},
	{"eu-south-1", "eus1", "abc", true},
	{"ap-south-1", "aps1", "abc", false},
	{"ap-northeast-1", "apne1", "acd", false},
	{"ap-northeast-2", "apne2", "abcd", false},
	{"ap-northeast-3", "apne3", "abc", false},
	{"ap-southeast-1", "apse1", "abc", false},
	{"ap-southeast-2", "apse2", "abc", false},
	{"ap-east-1", "ape1", "abc", true},
	{"sa-east-1", "sae1", "abc", false},
	{"me-south-1", "mes1", "abc", true},
	{"af-south-1", "afs1", "abc", true},
}

func regionByName(name string) regionInfo {
	for _, r := range regions {
		if r.name == name {
			return r
		}
	}
	// Unknown regions still work, with three zones.
	return regionInfo{name: name, code: strings.ReplaceAll(name, "-", ""), zones: "abc"}
}

type azInfo struct {
	Name, ID string
}

// zones lists a region's availability zones. Zone IDs follow the letter
// order (use1-az1 is us-east-1a); real accounts shuffle them, but a fixed
// mapping keeps things predictable.
func zones(region string) []azInfo {
	r := regionByName(region)
	var out []azInfo
	for i, l := range r.zones {
		out = append(out, azInfo{Name: region + string(l), ID: r.code + "-az" + i2s(i+1)})
	}
	return out
}

// zoneByNameOrID finds a zone in the region by name or zone ID.
func zoneByNameOrID(region, nameOrID string) (azInfo, bool) {
	for _, z := range zones(region) {
		if z.Name == nameOrID || z.ID == nameOrID {
			return z, true
		}
	}
	return azInfo{}, false
}

func zoneNames(region string) []string {
	var out []string
	for _, z := range zones(region) {
		out = append(out, z.Name)
	}
	return out
}

type AvailabilityZone struct {
	ZoneName           string   `xml:"zoneName"`
	ZoneId             string   `xml:"zoneId"`
	ZoneState          string   `xml:"zoneState"`
	RegionName         string   `xml:"regionName"`
	GroupName          string   `xml:"groupName"`
	GroupLongName      string   `xml:"groupLongName"`
	NetworkBorderGroup string   `xml:"networkBorderGroup"`
	OptInStatus        string   `xml:"optInStatus"`
	ZoneType           string   `xml:"zoneType"`
	Messages           []string `xml:"messageSet>item"`
}

type Region struct {
	RegionName     string `xml:"regionName"`
	RegionEndpoint string `xml:"regionEndpoint"`
	OptInStatus    string `xml:"optInStatus"`
}

var metaActions = map[string]action{
	"DescribeAvailabilityZones":            describeAvailabilityZones,
	"DescribeRegions":                      describeRegions,
	"DescribeImages":                       describeImages,
	"DescribeInstanceTypes":                describeInstanceTypes,
	"DescribeInstanceTypeOfferings":        describeInstanceTypeOfferings,
	"DescribeAccountAttributes":            describeAccountAttributes,
	"GetEbsEncryptionByDefault":            getEbsEncryptionByDefault,
	"GetInstanceMetadataDefaults":          getInstanceMetadataDefaults,
	"DescribeDhcpOptions":                  describeDhcpOptions,
	"DescribePrefixLists":                  describePrefixLists,
	"DescribeVpcEndpointServices":          describeVpcEndpointServices,
	"DescribeSecurityGroupVpcAssociations": func(c *call) (any, error) { return struct{}{}, nil },
}

func describeAvailabilityZones(c *call) (any, error) {
	names := c.p.list("ZoneName")
	ids := c.p.list("ZoneId")
	fs := c.p.filters()
	var out []AvailabilityZone
	for _, z := range zones(c.region) {
		if len(names) > 0 && !slices.Contains(names, z.Name) {
			continue
		}
		if len(ids) > 0 && !slices.Contains(ids, z.ID) {
			continue
		}
		az := AvailabilityZone{
			ZoneName: z.Name, ZoneId: z.ID, ZoneState: "available", RegionName: c.region,
			GroupName: c.region + "-zg-1", GroupLongName: regionLongName(c.region), NetworkBorderGroup: c.region,
			OptInStatus: "opt-in-not-required", ZoneType: "availability-zone",
		}
		vals := map[string][]string{
			"zone-name": {z.Name}, "zone-id": {z.ID}, "state": {"available"}, "region-name": {c.region},
			"group-name": {az.GroupName}, "opt-in-status": {az.OptInStatus}, "zone-type": {az.ZoneType},
			"network-border-group": {c.region}, "message": nil, "group-long-name": {az.GroupLongName},
		}
		ok, err := matchVals(fs, vals)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, az)
		}
	}
	return struct {
		Zones []AvailabilityZone `xml:"availabilityZoneInfo>item"`
	}{out}, nil
}

func regionLongName(region string) string {
	names := map[string]string{
		"us-east-1": "US East (N. Virginia)", "us-east-2": "US East (Ohio)",
		"us-west-1": "US West (N. California)", "us-west-2": "US West (Oregon)",
		"eu-west-1": "Europe (Ireland)", "eu-west-2": "Europe (London)", "eu-west-3": "Europe (Paris)",
		"eu-central-1": "Europe (Frankfurt)", "eu-north-1": "Europe (Stockholm)",
	}
	if n, ok := names[region]; ok {
		return n
	}
	return region
}

func describeRegions(c *call) (any, error) {
	names := c.p.list("RegionName")
	allRegions := c.p.boolean("AllRegions")
	fs := c.p.filters()
	var out []Region
	for _, r := range regions {
		status := "opt-in-not-required"
		if r.optIn {
			if !allRegions && len(names) == 0 {
				continue
			}
			status = "not-opted-in"
		}
		if len(names) > 0 && !slices.Contains(names, r.name) {
			continue
		}
		vals := map[string][]string{"region-name": {r.name}, "opt-in-status": {status}, "endpoint": {"ec2." + r.name + ".amazonaws.com"}}
		ok, err := matchVals(fs, vals)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, Region{RegionName: r.name, RegionEndpoint: "ec2." + r.name + ".amazonaws.com", OptInStatus: status})
		}
	}
	for _, n := range names {
		if !slices.ContainsFunc(regions, func(r regionInfo) bool { return r.name == n }) {
			return nil, errorf("InvalidParameterValue", "Invalid region: %s", n)
		}
	}
	return struct {
		Regions []Region `xml:"regionInfo>item"`
	}{out}, nil
}

func describeAccountAttributes(c *call) (any, error) {
	type val struct {
		V string `xml:"attributeValue"`
	}
	type attr struct {
		Name   string `xml:"attributeName"`
		Values []val  `xml:"attributeValueSet>item"`
	}
	attrs := []attr{
		{"supported-platforms", []val{{"VPC"}}},
		{"default-vpc", []val{{c.defaultVpcID()}}},
		{"max-instances", []val{{"20"}}},
		{"max-elastic-ips", []val{{"5"}}},
		{"vpc-max-elastic-ips", []val{{"5"}}},
		{"vpc-max-security-groups-per-interface", []val{{"5"}}},
	}
	if names := c.p.list("AttributeName"); len(names) > 0 {
		var picked []attr
		for _, a := range attrs {
			if slices.Contains(names, a.Name) {
				picked = append(picked, a)
			}
		}
		attrs = picked
	}
	return struct {
		Attrs []attr `xml:"accountAttributeSet>item"`
	}{attrs}, nil
}

func getEbsEncryptionByDefault(c *call) (any, error) {
	return struct {
		Enabled bool `xml:"ebsEncryptionByDefault"`
	}{false}, nil
}

func getInstanceMetadataDefaults(c *call) (any, error) {
	return struct {
		Defaults struct{} `xml:"accountLevel"`
	}{}, nil
}

func describeDhcpOptions(c *call) (any, error) {
	type val struct {
		V string `xml:"value"`
	}
	type conf struct {
		Key    string `xml:"key"`
		Values []val  `xml:"valueSet>item"`
	}
	type dopt struct {
		ID      string `xml:"dhcpOptionsId"`
		OwnerId string `xml:"ownerId"`
		Conf    []conf `xml:"dhcpConfigurationSet>item"`
	}
	domain := "ec2.internal"
	if c.region != "us-east-1" {
		domain = c.region + ".compute.internal"
	}
	d := dopt{ID: c.dhcpOptionsID(), OwnerId: c.account, Conf: []conf{
		{"domain-name", []val{{domain}}},
		{"domain-name-servers", []val{{"AmazonProvidedDNS"}}},
	}}
	if ids := c.p.list("DhcpOptionsId"); len(ids) > 0 && !slices.Contains(ids, d.ID) {
		return nil, errorf("InvalidDhcpOptionID.NotFound", "The dhcpOption ID '%s' does not exist", ids[0])
	}
	return struct {
		Opts []dopt `xml:"dhcpOptionsSet>item"`
	}{[]dopt{d}}, nil
}

func (c *call) dhcpOptionsID() string { return stableID("dopt", c.ns, "dhcp") }

func describePrefixLists(c *call) (any, error) {
	type pl struct {
		ID    string   `xml:"prefixListId"`
		Name  string   `xml:"prefixListName"`
		Cidrs []string `xml:"cidrSet>item"`
	}
	lists := []pl{
		{stableID("pl", c.region, "s3")[:11], "com.amazonaws." + c.region + ".s3", []string{"52.216.0.0/15", "54.231.0.0/16"}},
		{stableID("pl", c.region, "dynamodb")[:11], "com.amazonaws." + c.region + ".dynamodb", []string{"52.94.0.0/22"}},
	}
	ids := c.p.list("PrefixListId")
	fs := c.p.filters()
	var out []pl
	for _, l := range lists {
		if len(ids) > 0 && !slices.Contains(ids, l.ID) {
			continue
		}
		vals := map[string][]string{"prefix-list-id": {l.ID}, "prefix-list-name": {l.Name}}
		ok, err := matchVals(fs, vals)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, l)
		}
	}
	return struct {
		Lists []pl `xml:"prefixListSet>item"`
	}{out}, nil
}
