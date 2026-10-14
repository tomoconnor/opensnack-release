// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"slices"
	"strings"
	"time"
)

var instanceActions = map[string]action{
	"RunInstances":                           runInstances,
	"DescribeInstances":                      describeInstances,
	"TerminateInstances":                     terminateInstances,
	"StopInstances":                          stopInstances,
	"StartInstances":                         startInstances,
	"RebootInstances":                        rebootInstances,
	"ModifyInstanceAttribute":                modifyInstanceAttribute,
	"DescribeInstanceAttribute":              describeInstanceAttribute,
	"DescribeInstanceCreditSpecifications":   describeInstanceCreditSpecifications,
	"ModifyInstanceCreditSpecification":      modifyInstanceCreditSpecification,
	"ModifyInstanceMetadataOptions":          modifyInstanceMetadataOptions,
	"ModifyInstanceMaintenanceOptions":       modifyInstanceMaintenanceOptions,
	"DescribeInstanceStatus":                 describeInstanceStatus,
	"DescribeIamInstanceProfileAssociations": describeIamInstanceProfileAssociations,
	"AssociateIamInstanceProfile":            associateIamInstanceProfile,
	"DisassociateIamInstanceProfile":         disassociateIamInstanceProfile,
	"ReplaceIamInstanceProfileAssociation":   replaceIamInstanceProfileAssociation,
}

type InstanceState struct {
	Code int    `xml:"code"`
	Name string `xml:"name"`
}

var (
	statePending      = InstanceState{0, "pending"}
	stateRunning      = InstanceState{16, "running"}
	stateShuttingDown = InstanceState{32, "shutting-down"}
	stateTerminated   = InstanceState{48, "terminated"}
	stateStopping     = InstanceState{64, "stopping"}
	stateStopped      = InstanceState{80, "stopped"}
)

type MetadataOptions struct {
	State                   string `xml:"state"`
	HttpTokens              string `xml:"httpTokens"`
	HttpPutResponseHopLimit int    `xml:"httpPutResponseHopLimit"`
	HttpEndpoint            string `xml:"httpEndpoint"`
	HttpProtocolIpv6        string `xml:"httpProtocolIpv6"`
	InstanceMetadataTags    string `xml:"instanceMetadataTags"`
}

type instanceVolume struct {
	VolumeId            string
	DeviceName          string
	DeleteOnTermination bool
	AttachTime          string
}

// Instance is the stored form of an instance. Its interfaces and volumes
// are separate records; render assembles the API view.
type Instance struct {
	InstanceId       string
	ReservationId    string
	ImageId          string
	InstanceType     string
	KeyName          string
	State            InstanceState
	StateReason      string
	SubnetId         string
	VpcId            string
	AvailabilityZone string
	Architecture     string
	RootDeviceName   string
	BootMode         string
	LaunchTime       string
	ClientToken      string
	Enis             []string
	Volumes          []instanceVolume
	Groups           []GroupId
	// AutoPublicIP is true when the primary interface got an Amazon-owned
	// public address at launch; it is released on stop.
	AutoPublicIP          bool
	Monitoring            bool
	EbsOptimized          bool
	DisableApiTermination bool
	DisableApiStop        bool
	ShutdownBehavior      string
	UserData              string
	Tenancy               string
	CpuCredits            string
	CoreCount             int
	ThreadsPerCore        int
	Hibernation           bool
	IamInstanceProfile    string // ARN
	IamAssociationId      string
	Metadata              MetadataOptions
	HostnameType          string
	DnsARecord            bool
	DnsAAAARecord         bool
	AutoRecovery          string
	Tags                  []Tag
}

func (i *Instance) objectID() string { return i.InstanceId }
func (i *Instance) tagList() *[]Tag  { return &i.Tags }
func (i *Instance) filterValues() map[string][]string {
	m := map[string][]string{
		"instance-id": {i.InstanceId}, "instance-state-name": {i.State.Name}, "instance-state-code": {i2s(i.State.Code)},
		"instance-type": {i.InstanceType}, "image-id": {i.ImageId}, "vpc-id": nonEmpty(i.VpcId),
		"subnet-id": nonEmpty(i.SubnetId), "availability-zone": {i.AvailabilityZone}, "key-name": nonEmpty(i.KeyName),
		"reservation-id": {i.ReservationId}, "architecture": {i.Architecture}, "root-device-type": {"ebs"},
		"root-device-name": {i.RootDeviceName}, "tenancy": {i.Tenancy}, "launch-time": {i.LaunchTime},
		"monitoring-state": {monitoringState(i.Monitoring)}, "iam-instance-profile.arn": nonEmpty(i.IamInstanceProfile),
		"client-token": nonEmpty(i.ClientToken), "virtualization-type": {"hvm"}, "hypervisor": {"xen"},
		"platform-details": {"Linux/UNIX"}, "instance-lifecycle": nil, "platform": nil,
		"network-interface.network-interface-id": i.Enis,
		"private-ip-address":                     nil, "ip-address": nil, "private-dns-name": nil, "dns-name": nil,
		"metadata-options.http-tokens": {i.Metadata.HttpTokens},
	}
	for _, g := range i.Groups {
		for _, k := range []string{"instance.group-id", "group-id"} {
			m[k] = append(m[k], g.GroupId)
		}
		for _, k := range []string{"instance.group-name", "group-name"} {
			m[k] = append(m[k], g.GroupName)
		}
	}
	for _, k := range []string{"instance.group-id", "group-id", "instance.group-name", "group-name"} {
		if _, ok := m[k]; !ok {
			m[k] = nil
		}
	}
	for _, v := range i.Volumes {
		m["block-device-mapping.volume-id"] = append(m["block-device-mapping.volume-id"], v.VolumeId)
		m["block-device-mapping.device-name"] = append(m["block-device-mapping.device-name"], v.DeviceName)
	}
	return m
}

func monitoringState(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

// goneInstance is a terminated instance, kept in memory so waiters see the
// "terminated" state.
type goneInstance struct {
	inst    *Instance
	expires time.Time
}

const terminatedVisible = time.Hour

func (h *Handler) goneInstance(ns, id string) (*Instance, bool) {
	g, ok := h.gone[ns+"/"+id]
	if !ok || time.Now().After(g.expires) {
		delete(h.gone, ns+"/"+id)
		return nil, false
	}
	return g.inst, true
}

func (h *Handler) goneInstances(ns string) []*Instance {
	var out []*Instance
	for k, g := range h.gone {
		if strings.HasPrefix(k, ns+"/") {
			if time.Now().After(g.expires) {
				delete(h.gone, k)
				continue
			}
			out = append(out, g.inst)
		}
	}
	slices.SortFunc(out, func(a, b *Instance) int { return strings.Compare(a.InstanceId, b.InstanceId) })
	return out
}

func (c *call) instance(id string) (*Instance, error) {
	return must[Instance](c, tInstance, id, "InvalidInstanceID.NotFound")
}

// The API view of an instance.

type instanceXML struct {
	InstanceId         string           `xml:"instanceId"`
	ImageId            string           `xml:"imageId"`
	State              InstanceState    `xml:"instanceState"`
	PrivateDnsName     string           `xml:"privateDnsName"`
	DnsName            string           `xml:"dnsName"`
	Reason             string           `xml:"reason"`
	KeyName            string           `xml:"keyName,omitempty"`
	AmiLaunchIndex     int              `xml:"amiLaunchIndex"`
	InstanceType       string           `xml:"instanceType"`
	LaunchTime         string           `xml:"launchTime"`
	Placement          placementXML     `xml:"placement"`
	Monitoring         state            `xml:"monitoring"`
	SubnetId           string           `xml:"subnetId,omitempty"`
	VpcId              string           `xml:"vpcId,omitempty"`
	PrivateIpAddress   string           `xml:"privateIpAddress,omitempty"`
	IpAddress          string           `xml:"ipAddress,omitempty"`
	SourceDestCheck    *bool            `xml:"sourceDestCheck,omitempty"`
	Groups             []GroupId        `xml:"groupSet>item"`
	StateReason        *stateReasonXML  `xml:"stateReason,omitempty"`
	Architecture       string           `xml:"architecture"`
	RootDeviceType     string           `xml:"rootDeviceType"`
	RootDeviceName     string           `xml:"rootDeviceName"`
	BlockDeviceMapping []instanceBDMXML `xml:"blockDeviceMapping>item"`
	VirtualizationType string           `xml:"virtualizationType"`
	ClientToken        string           `xml:"clientToken"`
	Tags               []Tag            `xml:"tagSet>item,omitempty"`
	Hypervisor         string           `xml:"hypervisor"`
	NetworkInterfaces  []instanceENIXML `xml:"networkInterfaceSet>item"`
	IamInstanceProfile *iamProfileXML   `xml:"iamInstanceProfile,omitempty"`
	EbsOptimized       bool             `xml:"ebsOptimized"`
	EnaSupport         bool             `xml:"enaSupport"`
	CpuOptions         cpuOptionsXML    `xml:"cpuOptions"`
	CapacityRes        capacityResXML   `xml:"capacityReservationSpecification"`
	Hibernation        hibernationXML   `xml:"hibernationOptions"`
	MetadataOptions    MetadataOptions  `xml:"metadataOptions"`
	Enclave            enclaveXML       `xml:"enclaveOptions"`
	BootMode           string           `xml:"bootMode,omitempty"`
	PlatformDetails    string           `xml:"platformDetails"`
	UsageOperation     string           `xml:"usageOperation"`
	UsageOpUpdateTime  string           `xml:"usageOperationUpdateTime"`
	PrivateDnsOptions  PrivateDnsOpts   `xml:"privateDnsNameOptions"`
	Maintenance        maintenanceXML   `xml:"maintenanceOptions"`
	CurrentBootMode    string           `xml:"currentInstanceBootMode"`
}

type placementXML struct {
	AvailabilityZone string `xml:"availabilityZone"`
	GroupName        string `xml:"groupName"`
	Tenancy          string `xml:"tenancy"`
}

type stateReasonXML struct {
	Code    string `xml:"code"`
	Message string `xml:"message"`
}

type instanceBDMXML struct {
	DeviceName string `xml:"deviceName"`
	Ebs        struct {
		VolumeId            string `xml:"volumeId"`
		Status              string `xml:"status"`
		AttachTime          string `xml:"attachTime"`
		DeleteOnTermination bool   `xml:"deleteOnTermination"`
	} `xml:"ebs"`
}

type instanceENIXML struct {
	NetworkInterfaceId string          `xml:"networkInterfaceId"`
	SubnetId           string          `xml:"subnetId"`
	VpcId              string          `xml:"vpcId"`
	Description        string          `xml:"description"`
	OwnerId            string          `xml:"ownerId"`
	Status             string          `xml:"status"`
	MacAddress         string          `xml:"macAddress"`
	PrivateIpAddress   string          `xml:"privateIpAddress"`
	PrivateDnsName     string          `xml:"privateDnsName"`
	SourceDestCheck    bool            `xml:"sourceDestCheck"`
	Groups             []GroupId       `xml:"groupSet>item"`
	Attachment         *EniAttachment  `xml:"attachment,omitempty"`
	Association        *EniAssociation `xml:"association,omitempty"`
	PrivateIpAddresses []EniPrivateIp  `xml:"privateIpAddressesSet>item"`
	InterfaceType      string          `xml:"interfaceType"`
}

type iamProfileXML struct {
	Arn string `xml:"arn"`
	Id  string `xml:"id"`
}

type cpuOptionsXML struct {
	CoreCount      int `xml:"coreCount"`
	ThreadsPerCore int `xml:"threadsPerCore"`
}

type capacityResXML struct {
	Preference string `xml:"capacityReservationPreference"`
}

type hibernationXML struct {
	Configured bool `xml:"configured"`
}

type enclaveXML struct {
	Enabled bool `xml:"enabled"`
}

type maintenanceXML struct {
	AutoRecovery string `xml:"autoRecovery"`
}

func (c *call) render(i *Instance) instanceXML {
	x := instanceXML{
		InstanceId: i.InstanceId, ImageId: i.ImageId, State: i.State, KeyName: i.KeyName,
		InstanceType: i.InstanceType, LaunchTime: i.LaunchTime,
		Placement:  placementXML{AvailabilityZone: i.AvailabilityZone, Tenancy: i.Tenancy},
		Monitoring: state{monitoringState(i.Monitoring)}, Architecture: i.Architecture,
		RootDeviceType: "ebs", RootDeviceName: i.RootDeviceName, VirtualizationType: "hvm",
		ClientToken: i.ClientToken, Tags: i.Tags, Hypervisor: "xen", EbsOptimized: i.EbsOptimized, EnaSupport: true,
		CpuOptions:      cpuOptionsXML{i.CoreCount, i.ThreadsPerCore},
		CapacityRes:     capacityResXML{"open"},
		Hibernation:     hibernationXML{i.Hibernation},
		MetadataOptions: i.Metadata, BootMode: i.BootMode,
		PlatformDetails: "Linux/UNIX", UsageOperation: "RunInstances", UsageOpUpdateTime: i.LaunchTime,
		PrivateDnsOptions: PrivateDnsOpts{i.HostnameType, i.DnsARecord, i.DnsAAAARecord},
		Maintenance:       maintenanceXML{i.AutoRecovery},
		CurrentBootMode:   "legacy-bios",
		Groups:            i.Groups,
	}
	if i.BootMode == "uefi" || i.Architecture == "arm64" {
		x.CurrentBootMode = "uefi"
	}
	if i.StateReason != "" {
		x.Reason = i.StateReason
		x.StateReason = &stateReasonXML{"Client.UserInitiatedShutdown", "Client.UserInitiatedShutdown: User initiated shutdown"}
		if i.State == stateTerminated {
			x.StateReason.Code = "Client.UserInitiatedShutdown"
		}
	}
	if i.IamInstanceProfile != "" {
		x.IamInstanceProfile = &iamProfileXML{i.IamInstanceProfile, "AIPA" + strings.ToUpper(stableID("x", i.IamInstanceProfile)[2:19])}
	}
	if i.State == stateTerminated {
		x.Groups = nil
		return x
	}
	x.SubnetId, x.VpcId = i.SubnetId, i.VpcId
	for _, v := range i.Volumes {
		var b instanceBDMXML
		b.DeviceName = v.DeviceName
		b.Ebs.VolumeId, b.Ebs.Status, b.Ebs.AttachTime, b.Ebs.DeleteOnTermination = v.VolumeId, "attached", v.AttachTime, v.DeleteOnTermination
		x.BlockDeviceMapping = append(x.BlockDeviceMapping, b)
	}
	for _, id := range i.Enis {
		e, err := c.eni(id)
		if err != nil {
			continue
		}
		ix := instanceENIXML{
			NetworkInterfaceId: e.NetworkInterfaceId, SubnetId: e.SubnetId, VpcId: e.VpcId, Description: e.Description,
			OwnerId: e.OwnerId, Status: e.Status, MacAddress: e.MacAddress, PrivateIpAddress: e.PrivateIpAddress,
			PrivateDnsName: e.PrivateDnsName, SourceDestCheck: e.SourceDestCheck, Groups: e.Groups,
			Attachment: e.Attachment, Association: e.Association, PrivateIpAddresses: e.PrivateIpAddresses,
			InterfaceType: e.InterfaceType,
		}
		x.NetworkInterfaces = append(x.NetworkInterfaces, ix)
		if e.Attachment != nil && e.Attachment.DeviceIndex == 0 {
			x.PrivateIpAddress, x.PrivateDnsName = e.PrivateIpAddress, e.PrivateDnsName
			sdc := e.SourceDestCheck
			x.SourceDestCheck = &sdc
			x.Groups = e.Groups
			if e.Association != nil {
				x.IpAddress, x.DnsName = e.Association.PublicIp, e.Association.PublicDnsName
			}
		}
	}
	return x
}

type reservationXML struct {
	ReservationId string        `xml:"reservationId"`
	OwnerId       string        `xml:"ownerId"`
	Groups        []GroupId     `xml:"groupSet>item"`
	Instances     []instanceXML `xml:"instancesSet>item"`
}

// The launch.

type bdmSpec struct {
	device              string
	size                int
	volType             string
	iops, throughput    *int
	deleteOnTermination *bool
	encrypted           bool
	kmsKeyID            string
	snapshotID          string
	noDevice            bool
}

func parseBDMs(p params) []bdmSpec {
	var out []bdmSpec
	for _, b := range p.sub("BlockDeviceMapping") {
		s := bdmSpec{
			device: b.get("DeviceName"), size: b.integer("Ebs.VolumeSize", 0), volType: b.get("Ebs.VolumeType"),
			iops: b.optInt("Ebs.Iops"), throughput: b.optInt("Ebs.Throughput"), deleteOnTermination: b.optBool("Ebs.DeleteOnTermination"),
			encrypted: b.boolean("Ebs.Encrypted"), kmsKeyID: b.get("Ebs.KmsKeyId"), snapshotID: b.get("Ebs.SnapshotId"),
			noDevice: b.has("NoDevice"),
		}
		out = append(out, s)
	}
	return out
}

// newVolume builds a volume record for a launch or CreateVolume, filling
// AWS's per-type defaults for IOPS and throughput.
func newVolume(az azInfo, size int, volType string, iops, throughput *int, encrypted bool, kmsKey, snapshot string) *Volume {
	if volType == "" {
		volType = "gp2"
	}
	v := &Volume{
		VolumeId: newID("vol"), Size: size, SnapshotId: snapshot, AvailabilityZone: az.Name, AvailabilityZoneId: az.ID,
		Status: "available", CreateTime: now(), VolumeType: volType, Encrypted: encrypted || kmsKey != "", KmsKeyId: kmsKey,
		MultiAttachEnabled: false,
	}
	switch volType {
	case "gp3":
		i, t := 3000, 125
		if iops != nil {
			i = *iops
		}
		if throughput != nil {
			t = *throughput
		}
		v.Iops, v.Throughput = &i, &t
	case "gp2":
		i := max(100, min(16000, 3*size))
		v.Iops = &i
	case "io1", "io2":
		v.Iops = iops
	}
	if v.Encrypted && v.KmsKeyId == "" {
		v.KmsKeyId = "arn:aws:kms:" + az.Name[:len(az.Name)-1] + ":000000000000:alias/aws/ebs"
	}
	return v
}

func runInstances(c *call) (any, error) {
	p := c.p
	img, ok := imageByID(c.region, p.get("ImageId"))
	if p.get("ImageId") == "" {
		return nil, missing("ImageId")
	}
	if !ok {
		return nil, errorf("InvalidAMIID.NotFound", "The image id '[%s]' does not exist", p.get("ImageId"))
	}
	itype := p.get("InstanceType")
	if itype == "" {
		itype = "m1.small"
	}
	spec, ok := instanceTypeByName(itype)
	if !ok {
		return nil, errorf("InvalidParameterValue", "Invalid value '%s' for InstanceType.", itype)
	}
	if spec.arch != img.Architecture {
		return nil, errorf("InvalidParameterValue", "The architecture '%s' of the specified instance type does not match the architecture '%s' of the specified AMI. Specify an instance type and an AMI that have matching architectures, and try again.", spec.arch, img.Architecture)
	}
	minCount, maxCount := p.integer("MinCount", 1), p.integer("MaxCount", 1)
	if minCount < 1 || maxCount < minCount {
		return nil, invalidParam("MinCount", p.get("MinCount"))
	}
	if maxCount > 20 {
		return nil, errorf("InstanceLimitExceeded", "You have requested more instances (%d) than your current instance limit of 20 allows.", maxCount)
	}
	if k := p.get("KeyName"); k != "" {
		if _, err := c.keyPairByName(k); err != nil {
			return nil, err
		}
	}

	// Network placement: the subnet from the first interface or SubnetId,
	// else the default subnet for the zone.
	nis := p.sub("NetworkInterface")
	var primaryNI params
	for _, ni := range nis {
		if ni.get("DeviceIndex") == "0" || len(nis) == 1 {
			primaryNI = ni
		}
	}
	subnetID := p.get("SubnetId")
	var existingENI *NetworkInterface
	if primaryNI != nil {
		if id := primaryNI.get("NetworkInterfaceId"); id != "" {
			e, err := c.eni(id)
			if err != nil {
				return nil, err
			}
			if e.attached() {
				return nil, errorf("InvalidNetworkInterface.InUse", "Interface: [%s] in use.", id)
			}
			existingENI, subnetID = e, e.SubnetId
		} else if s := primaryNI.get("SubnetId"); s != "" {
			subnetID = s
		}
	}
	azWanted := p.get("Placement.AvailabilityZone")
	var subnet *Subnet
	var err error
	if subnetID != "" {
		if subnet, err = c.subnet(subnetID); err != nil {
			return nil, err
		}
		if azWanted != "" && azWanted != subnet.AvailabilityZone {
			return nil, errorf("InvalidParameterValue", "Value (%s) for parameter availabilityZone is invalid. Subnet %s is in availability zone %s", azWanted, subnet.SubnetId, subnet.AvailabilityZone)
		}
	} else {
		z, err := c.pickZone(azWanted, "")
		if err != nil {
			return nil, err
		}
		subnets, _ := all[Subnet](c, tSubnet)
		for _, s := range subnets {
			if s.DefaultForAz && s.AvailabilityZone == z.Name {
				subnet = s
			}
		}
		if subnet == nil {
			return nil, errorf("VPCIdNotSpecified", "No default VPC for this user. GroupName is only supported for EC2-Classic and default VPC.")
		}
	}
	z, _ := zoneByNameOrID(c.region, subnet.AvailabilityZone)

	groupIDs, groupNames := p.list("SecurityGroupId"), p.list("SecurityGroup")
	if primaryNI != nil && len(primaryNI.list("SecurityGroupId")) > 0 {
		groupIDs = primaryNI.list("SecurityGroupId")
	}
	groups, err := c.resolveGroups(subnet.VpcId, groupIDs, groupNames)
	if err != nil {
		return nil, err
	}

	publicIP := subnet.MapPublicIpOnLaunch
	if primaryNI != nil && primaryNI.has("AssociatePublicIpAddress") {
		publicIP = primaryNI.boolean("AssociatePublicIpAddress")
	}
	privateIP := p.get("PrivateIpAddress")
	nSecondary := 0
	var secondary []string
	if primaryNI != nil {
		if v := primaryNI.get("PrivateIpAddress"); v != "" {
			privateIP = v
		}
		nSecondary = primaryNI.integer("SecondaryPrivateIpAddressCount", 0)
		for _, a := range primaryNI.sub("PrivateIpAddresses") {
			if a.boolean("Primary") {
				privateIP = a.get("PrivateIpAddress")
			} else {
				secondary = append(secondary, a.get("PrivateIpAddress"))
			}
		}
	}
	if privateIP != "" && maxCount > 1 {
		return nil, errorf("InvalidParameterCombination", "Cannot specify a private IP address when launching more than one instance")
	}

	instTags, err := c.tagSpec("instance")
	if err != nil {
		return nil, err
	}
	volTags, err := c.tagSpec("volume")
	if err != nil {
		return nil, err
	}
	eniTags, err := c.tagSpec("network-interface")
	if err != nil {
		return nil, err
	}

	bdms := parseBDMs(p)
	threads := 1
	if spec.vcpus > 1 && spec.arch == "x86_64" {
		threads = 2
	}
	cores, tpc := spec.vcpus/threads, threads
	if v := p.integer("CpuOptions.CoreCount", 0); v > 0 {
		cores = v
	}
	if v := p.integer("CpuOptions.ThreadsPerCore", 0); v > 0 {
		tpc = v
	}
	md := MetadataOptions{State: "applied", HttpTokens: "optional", HttpPutResponseHopLimit: 1, HttpEndpoint: "enabled",
		HttpProtocolIpv6: "disabled", InstanceMetadataTags: "disabled"}
	if img.ImdsSupport == "v2.0" {
		md.HttpTokens, md.HttpPutResponseHopLimit = "required", 2
	}
	if v := p.get("MetadataOptions.HttpTokens"); v != "" {
		md.HttpTokens = v
	}
	if v := p.integer("MetadataOptions.HttpPutResponseHopLimit", 0); v > 0 {
		md.HttpPutResponseHopLimit = v
	}
	if v := p.get("MetadataOptions.HttpEndpoint"); v != "" {
		md.HttpEndpoint = v
	}
	if v := p.get("MetadataOptions.HttpProtocolIpv6"); v != "" {
		md.HttpProtocolIpv6 = v
	}
	if v := p.get("MetadataOptions.InstanceMetadataTags"); v != "" {
		md.InstanceMetadataTags = v
	}
	credits := ""
	if spec.burstable {
		credits = "unlimited"
		if strings.HasPrefix(itype, "t2.") {
			credits = "standard"
		}
		if v := p.get("CreditSpecification.CpuCredits"); v != "" {
			credits = v
		}
	}
	profile := p.get("IamInstanceProfile.Arn")
	if n := p.get("IamInstanceProfile.Name"); n != "" {
		profile = "arn:aws:iam::" + c.account + ":instance-profile/" + n
	}
	tenancy := p.get("Placement.Tenancy")
	if tenancy == "" {
		tenancy = "default"
	}
	shutdown := p.get("InstanceInitiatedShutdownBehavior")
	if shutdown == "" {
		shutdown = "stop"
	}
	hostnameType := p.get("PrivateDnsNameOptions.HostnameType")
	if hostnameType == "" {
		hostnameType = subnet.PrivateDnsNameOptionsOnLaunch.HostnameType
	}
	autoRecovery := p.get("MaintenanceOptions.AutoRecovery")
	if autoRecovery == "" {
		autoRecovery = "default"
	}

	res := reservationXML{ReservationId: newID("r"), OwnerId: c.account, Groups: nil}
	launched := now()
	for n := 0; n < maxCount; n++ {
		inst := &Instance{
			InstanceId: newID("i"), ReservationId: res.ReservationId, ImageId: img.ImageId, InstanceType: itype,
			KeyName: p.get("KeyName"), State: stateRunning, SubnetId: subnet.SubnetId, VpcId: subnet.VpcId,
			AvailabilityZone: subnet.AvailabilityZone, Architecture: img.Architecture, RootDeviceName: img.RootDeviceName,
			BootMode: img.BootMode, LaunchTime: launched, ClientToken: p.get("ClientToken"), Groups: groups,
			Monitoring: p.boolean("Monitoring.Enabled"), EbsOptimized: p.boolean("EbsOptimized"),
			DisableApiTermination: p.boolean("DisableApiTermination"), DisableApiStop: p.boolean("DisableApiStop"),
			ShutdownBehavior: shutdown, UserData: p.get("UserData"), Tenancy: tenancy, CpuCredits: credits,
			CoreCount: cores, ThreadsPerCore: tpc, Hibernation: p.boolean("HibernationOptions.Configured"),
			IamInstanceProfile: profile, Metadata: md, HostnameType: hostnameType,
			DnsARecord:    p.boolean("PrivateDnsNameOptions.EnableResourceNameDnsARecord"),
			DnsAAAARecord: p.boolean("PrivateDnsNameOptions.EnableResourceNameDnsAAAARecord"),
			AutoRecovery:  autoRecovery, Tags: instTags,
		}
		if profile != "" {
			inst.IamAssociationId = newID("iip-assoc")
		}

		// Primary interface.
		var eni *NetworkInterface
		if existingENI != nil {
			eni = existingENI
		} else {
			eni, err = c.createEni(subnet, eniSpec{ip: privateIP, secondary: secondary, nSecondary: nSecondary,
				description: primaryNI.get("Description"), groups: idsOf(groups)})
			if err != nil {
				return nil, err
			}
			eni.Tags = eniTags
		}
		deleteENI := existingENI == nil
		if primaryNI != nil && primaryNI.has("DeleteOnTermination") {
			deleteENI = primaryNI.boolean("DeleteOnTermination")
		}
		eni.Status = "in-use"
		eni.Groups = groups
		eni.Attachment = &EniAttachment{AttachmentId: newID("eni-attach"), InstanceId: inst.InstanceId, InstanceOwnerId: c.account,
			Status: "attached", AttachTime: launched, DeleteOnTermination: deleteENI}
		if publicIP {
			ip, err := c.publicIP()
			if err != nil {
				return nil, err
			}
			eni.Association = &EniAssociation{IpOwnerId: "amazon", PublicIp: ip, PublicDnsName: publicDNS(ip)}
			eni.PrivateIpAddresses[0].Association = eni.Association
			inst.AutoPublicIP = true
		}
		if err := c.put(tENI, eni); err != nil {
			return nil, err
		}
		inst.Enis = []string{eni.NetworkInterfaceId}

		// Volumes: the AMI's root device, overridden by the request, plus
		// any extra EBS mappings.
		root := img.BlockDeviceMapping[0]
		rootSpec := bdmSpec{device: root.DeviceName, size: root.Ebs.VolumeSize, volType: root.Ebs.VolumeType, snapshotID: root.Ebs.SnapshotId}
		var extra []bdmSpec
		for _, b := range bdms {
			if b.device == root.DeviceName {
				if b.size > 0 {
					if b.size < root.Ebs.VolumeSize {
						return nil, errorf("InvalidBlockDeviceMapping", "Volume of size %dGB is smaller than snapshot '%s', expect size >= %dGB", b.size, root.Ebs.SnapshotId, root.Ebs.VolumeSize)
					}
					rootSpec.size = b.size
				}
				if b.volType != "" {
					rootSpec.volType = b.volType
				}
				rootSpec.iops, rootSpec.throughput, rootSpec.deleteOnTermination = b.iops, b.throughput, b.deleteOnTermination
				rootSpec.encrypted, rootSpec.kmsKeyID = b.encrypted, b.kmsKeyID
			} else if !b.noDevice {
				extra = append(extra, b)
			}
		}
		for _, b := range append([]bdmSpec{rootSpec}, extra...) {
			size := b.size
			if size == 0 {
				size = 8
			}
			v := newVolume(z, size, b.volType, b.iops, b.throughput, b.encrypted, b.kmsKeyID, b.snapshotID)
			del := true
			if b.deleteOnTermination != nil {
				del = *b.deleteOnTermination
			}
			v.Status = "in-use"
			v.Attachments = []VolAttach{{VolumeId: v.VolumeId, InstanceId: inst.InstanceId, Device: b.device, Status: "attached", AttachTime: launched, DeleteOnTermination: del}}
			v.Tags = volTags
			if err := c.put(tVolume, v); err != nil {
				return nil, err
			}
			inst.Volumes = append(inst.Volumes, instanceVolume{v.VolumeId, b.device, del, launched})
		}

		if err := c.put(tInstance, inst); err != nil {
			return nil, err
		}
		x := c.render(inst)
		x.State = statePending
		x.AmiLaunchIndex = n
		res.Instances = append(res.Instances, x)
	}
	return res, nil
}

func idsOf(groups []GroupId) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g.GroupId)
	}
	return out
}

func describeInstances(c *call) (any, error) {
	insts, err := all[Instance](c, tInstance)
	if err != nil {
		return nil, err
	}
	insts = append(insts, c.h.goneInstances(c.ns)...)
	if ids := c.p.list("InstanceId"); len(ids) > 0 {
		var picked []*Instance
		for _, id := range ids {
			i := slices.IndexFunc(insts, func(x *Instance) bool { return x.InstanceId == id })
			if i < 0 {
				return nil, notFound("InvalidInstanceID.NotFound", id)
			}
			picked = append(picked, insts[i])
		}
		insts = picked
	}
	var reservations []reservationXML
	for _, inst := range insts {
		x := c.render(inst)
		vals := inst.filterValues()
		vals["private-ip-address"] = nonEmpty(x.PrivateIpAddress)
		vals["ip-address"] = nonEmpty(x.IpAddress)
		vals["private-dns-name"] = nonEmpty(x.PrivateDnsName)
		vals["dns-name"] = nonEmpty(x.DnsName)
		ok, err := matches(c.p.filters(), &filtered{inst, vals})
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		i := slices.IndexFunc(reservations, func(r reservationXML) bool { return r.ReservationId == inst.ReservationId })
		if i < 0 {
			reservations = append(reservations, reservationXML{ReservationId: inst.ReservationId, OwnerId: c.account})
			i = len(reservations) - 1
		}
		reservations[i].Instances = append(reservations[i].Instances, x)
	}
	return struct {
		Reservations []reservationXML `xml:"reservationSet>item"`
	}{reservations}, nil
}

// filtered overrides an object's filter values.
type filtered struct {
	object
	vals map[string][]string
}

func (f *filtered) filterValues() map[string][]string { return f.vals }

type stateChange struct {
	InstanceId    string        `xml:"instanceId"`
	CurrentState  InstanceState `xml:"currentState"`
	PreviousState InstanceState `xml:"previousState"`
}

// eachInstance loads every InstanceId.N, failing on the first unknown one.
func (c *call) eachInstance(fn func(*Instance) (stateChange, error)) ([]stateChange, error) {
	ids := c.p.list("InstanceId")
	if len(ids) == 0 {
		return nil, missing("InstanceId")
	}
	var insts []*Instance
	for _, id := range ids {
		inst, err := c.instance(id)
		if err != nil {
			if gone, ok := c.h.goneInstance(c.ns, id); ok {
				insts = append(insts, gone)
				continue
			}
			return nil, err
		}
		insts = append(insts, inst)
	}
	var out []stateChange
	for _, inst := range insts {
		ch, err := fn(inst)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, nil
}

func terminateInstances(c *call) (any, error) {
	changes, err := c.eachInstance(func(inst *Instance) (stateChange, error) {
		prev := inst.State
		if prev == stateTerminated {
			return stateChange{inst.InstanceId, stateTerminated, stateTerminated}, nil
		}
		if inst.DisableApiTermination {
			return stateChange{}, errorf("OperationNotPermitted", "The instance '%s' may not be terminated. Modify its 'disableApiTermination' instance attribute and try again.", inst.InstanceId)
		}
		if err := c.releaseInstanceResources(inst); err != nil {
			return stateChange{}, err
		}
		inst.State, inst.StateReason = stateTerminated, "User initiated ("+time.Now().UTC().Format("2006-01-02 15:04:05 MST")+")"
		if err := c.remove(tInstance, inst.InstanceId); err != nil {
			return stateChange{}, err
		}
		c.h.gone[c.ns+"/"+inst.InstanceId] = goneInstance{inst, time.Now().Add(terminatedVisible)}
		return stateChange{inst.InstanceId, stateShuttingDown, prev}, nil
	})
	if err != nil {
		return nil, err
	}
	return struct {
		Changes []stateChange `xml:"instancesSet>item"`
	}{changes}, nil
}

// releaseInstanceResources detaches or deletes an instance's interfaces and
// volumes on termination, following their DeleteOnTermination flags.
func (c *call) releaseInstanceResources(inst *Instance) error {
	for _, id := range inst.Enis {
		e, err := c.eni(id)
		if err != nil {
			continue
		}
		if e.Association != nil && e.Association.AllocationId != "" {
			if a, err := c.address(e.Association.AllocationId); err == nil {
				a.clearAssociation()
				if err := c.put(tEIP, a); err != nil {
					return err
				}
			}
		}
		if e.Attachment != nil && e.Attachment.DeleteOnTermination {
			if err := c.remove(tENI, id); err != nil {
				return err
			}
			continue
		}
		e.Attachment, e.Association, e.Status = nil, nil, "available"
		for i := range e.PrivateIpAddresses {
			e.PrivateIpAddresses[i].Association = nil
		}
		if err := c.put(tENI, e); err != nil {
			return err
		}
	}
	for _, v := range inst.Volumes {
		vol := new(Volume)
		if ok, _ := c.get(tVolume, v.VolumeId, vol); !ok {
			continue
		}
		if v.DeleteOnTermination {
			if err := c.remove(tVolume, v.VolumeId); err != nil {
				return err
			}
			continue
		}
		vol.Attachments, vol.Status = nil, "available"
		if err := c.put(tVolume, vol); err != nil {
			return err
		}
	}
	inst.Enis, inst.Volumes = nil, nil
	return nil
}

func stopInstances(c *call) (any, error) {
	changes, err := c.eachInstance(func(inst *Instance) (stateChange, error) {
		prev := inst.State
		switch prev {
		case stateStopped:
			return stateChange{inst.InstanceId, stateStopped, stateStopped}, nil
		case stateTerminated:
			return stateChange{}, errorf("IncorrectInstanceState", "This instance '%s' is not in a state from which it can be stopped.", inst.InstanceId)
		}
		if inst.DisableApiStop {
			return stateChange{}, errorf("OperationNotPermitted", "The instance '%s' may not be stopped. Modify its 'disableApiStop' instance attribute and try again.", inst.InstanceId)
		}
		inst.State, inst.StateReason = stateStopped, "User initiated ("+time.Now().UTC().Format("2006-01-02 15:04:05 MST")+")"
		if inst.AutoPublicIP {
			if err := c.setAutoPublicIP(inst, false); err != nil {
				return stateChange{}, err
			}
		}
		return stateChange{inst.InstanceId, stateStopping, prev}, c.put(tInstance, inst)
	})
	if err != nil {
		return nil, err
	}
	return struct {
		Changes []stateChange `xml:"instancesSet>item"`
	}{changes}, nil
}

// setAutoPublicIP gives the primary interface a fresh Amazon-owned public
// address, or takes it away (a stopped instance loses it).
func (c *call) setAutoPublicIP(inst *Instance, on bool) error {
	if len(inst.Enis) == 0 {
		return nil
	}
	e, err := c.eni(inst.Enis[0])
	if err != nil {
		return err
	}
	if e.Association != nil && e.Association.AllocationId != "" {
		return nil // an elastic IP stays put
	}
	e.Association = nil
	if on {
		ip, err := c.publicIP()
		if err != nil {
			return err
		}
		e.Association = &EniAssociation{IpOwnerId: "amazon", PublicIp: ip, PublicDnsName: publicDNS(ip)}
	}
	e.PrivateIpAddresses[0].Association = e.Association
	return c.put(tENI, e)
}

func startInstances(c *call) (any, error) {
	changes, err := c.eachInstance(func(inst *Instance) (stateChange, error) {
		prev := inst.State
		switch prev {
		case stateRunning:
			return stateChange{inst.InstanceId, stateRunning, stateRunning}, nil
		case stateTerminated:
			return stateChange{}, errorf("IncorrectInstanceState", "The instance '%s' is not in a state from which it can be started.", inst.InstanceId)
		}
		inst.State, inst.StateReason = stateRunning, ""
		if inst.AutoPublicIP {
			if err := c.setAutoPublicIP(inst, true); err != nil {
				return stateChange{}, err
			}
		}
		return stateChange{inst.InstanceId, statePending, prev}, c.put(tInstance, inst)
	})
	if err != nil {
		return nil, err
	}
	return struct {
		Changes []stateChange `xml:"instancesSet>item"`
	}{changes}, nil
}

func rebootInstances(c *call) (any, error) {
	_, err := c.eachInstance(func(inst *Instance) (stateChange, error) {
		if inst.State != stateRunning {
			return stateChange{}, errorf("IncorrectState", "The instance '%s' is not in the 'running' state.", inst.InstanceId)
		}
		return stateChange{}, nil
	})
	return nil, err
}

func modifyInstanceAttribute(c *call) (any, error) {
	inst, err := c.instance(c.p.get("InstanceId"))
	if err != nil {
		return nil, err
	}
	p := c.p
	// The attribute either comes as Attribute+Value or as its own member.
	attr, value := p.get("Attribute"), p.get("Value")
	members := map[string]string{
		"InstanceType.Value": "instanceType", "UserData.Value": "userData", "DisableApiTermination.Value": "disableApiTermination",
		"DisableApiStop.Value": "disableApiStop", "InstanceInitiatedShutdownBehavior.Value": "instanceInitiatedShutdownBehavior",
		"SourceDestCheck.Value": "sourceDestCheck", "EbsOptimized.Value": "ebsOptimized", "EnaSupport.Value": "enaSupport",
		"SriovNetSupport.Value": "sriovNetSupport",
	}
	n := 0
	for k, a := range members {
		if p.has(k) {
			attr, value = a, p.get(k)
			n++
		}
	}
	if ids := p.list("GroupId"); len(ids) > 0 {
		attr = "groupSet"
		n++
	}
	if len(p.sub("BlockDeviceMapping")) > 0 {
		attr = "blockDeviceMapping"
		n++
	}
	if n > 1 {
		return nil, errorf("InvalidParameterCombination", "Fields for multiple attribute types specified: %s", attr)
	}
	needStopped := func() error {
		if inst.State != stateStopped {
			return errorf("IncorrectInstanceState", "The instance '%s' is not in the 'stopped' state.", inst.InstanceId)
		}
		return nil
	}
	switch attr {
	case "instanceType":
		if err := needStopped(); err != nil {
			return nil, err
		}
		spec, ok := instanceTypeByName(value)
		if !ok {
			return nil, errorf("InvalidInstanceAttributeValue", "Value (%s) for parameter instanceType is invalid.", value)
		}
		if spec.arch != inst.Architecture {
			return nil, errorf("InvalidInstanceAttributeValue", "The instance type '%s' does not support the architecture of the instance (%s).", value, inst.Architecture)
		}
		inst.InstanceType = value
		threads := 1
		if spec.vcpus > 1 && spec.arch == "x86_64" {
			threads = 2
		}
		inst.CoreCount, inst.ThreadsPerCore = spec.vcpus/threads, threads
		if !spec.burstable {
			inst.CpuCredits = ""
		} else if inst.CpuCredits == "" {
			inst.CpuCredits = "unlimited"
			if strings.HasPrefix(value, "t2.") {
				inst.CpuCredits = "standard"
			}
		}
	case "userData":
		if err := needStopped(); err != nil {
			return nil, err
		}
		inst.UserData = value
	case "disableApiTermination":
		inst.DisableApiTermination = strings.EqualFold(value, "true")
	case "disableApiStop":
		inst.DisableApiStop = strings.EqualFold(value, "true")
	case "instanceInitiatedShutdownBehavior":
		if value != "stop" && value != "terminate" {
			return nil, invalidParam("InstanceInitiatedShutdownBehavior", value)
		}
		inst.ShutdownBehavior = value
	case "ebsOptimized":
		if err := needStopped(); err != nil {
			return nil, err
		}
		inst.EbsOptimized = strings.EqualFold(value, "true")
	case "sourceDestCheck", "groupSet":
		if len(inst.Enis) == 0 {
			return nil, errorf("IncorrectInstanceState", "The instance '%s' has no network interface.", inst.InstanceId)
		}
		e, err := c.eni(inst.Enis[0])
		if err != nil {
			return nil, err
		}
		if attr == "sourceDestCheck" {
			e.SourceDestCheck = strings.EqualFold(value, "true")
		} else {
			if e.Groups, err = c.resolveGroups(e.VpcId, p.list("GroupId"), nil); err != nil {
				return nil, err
			}
			inst.Groups = e.Groups
		}
		if err := c.put(tENI, e); err != nil {
			return nil, err
		}
	case "blockDeviceMapping":
		for _, b := range parseBDMs(p) {
			for i := range inst.Volumes {
				if inst.Volumes[i].DeviceName == b.device && b.deleteOnTermination != nil {
					inst.Volumes[i].DeleteOnTermination = *b.deleteOnTermination
				}
			}
		}
	case "enaSupport", "sriovNetSupport":
	default:
		return nil, errorf("InvalidParameterValue", "Value (%s) for parameter attribute is invalid.", attr)
	}
	return nil, c.put(tInstance, inst)
}

func describeInstanceAttribute(c *call) (any, error) {
	inst, err := c.instance(c.p.get("InstanceId"))
	if err != nil {
		return nil, err
	}
	type val struct {
		Value string `xml:"value"`
	}
	type blob struct {
		Value string `xml:"value,omitempty"`
	}
	type out struct {
		InstanceId          string           `xml:"instanceId"`
		InstanceType        *val             `xml:"instanceType,omitempty"`
		UserData            *blob            `xml:"userData,omitempty"`
		DisableApiTerm      *val             `xml:"disableApiTermination,omitempty"`
		DisableApiStop      *val             `xml:"disableApiStop,omitempty"`
		ShutdownBehavior    *val             `xml:"instanceInitiatedShutdownBehavior,omitempty"`
		RootDeviceName      *val             `xml:"rootDeviceName,omitempty"`
		SourceDestCheck     *val             `xml:"sourceDestCheck,omitempty"`
		EbsOptimized        *val             `xml:"ebsOptimized,omitempty"`
		EnaSupport          *val             `xml:"enaSupport,omitempty"`
		SriovNetSupport     *val             `xml:"sriovNetSupport,omitempty"`
		Groups              []GroupId        `xml:"groupSet>item,omitempty"`
		BlockDeviceMappings []instanceBDMXML `xml:"blockDeviceMapping>item,omitempty"`
		Enclave             *enclaveXML      `xml:"enclaveOptions,omitempty"`
		ProductCodes        *struct{}        `xml:"productCodes,omitempty"`
		Kernel              *struct{}        `xml:"kernel,omitempty"`
		Ramdisk             *struct{}        `xml:"ramdisk,omitempty"`
	}
	o := out{InstanceId: inst.InstanceId}
	x := c.render(inst)
	switch a := c.p.get("Attribute"); a {
	case "instanceType":
		o.InstanceType = &val{inst.InstanceType}
	case "userData":
		o.UserData = &blob{inst.UserData}
	case "disableApiTermination":
		o.DisableApiTerm = &val{b2s(inst.DisableApiTermination)}
	case "disableApiStop":
		o.DisableApiStop = &val{b2s(inst.DisableApiStop)}
	case "instanceInitiatedShutdownBehavior":
		o.ShutdownBehavior = &val{inst.ShutdownBehavior}
	case "rootDeviceName":
		o.RootDeviceName = &val{inst.RootDeviceName}
	case "sourceDestCheck":
		sdc := true
		if x.SourceDestCheck != nil {
			sdc = *x.SourceDestCheck
		}
		o.SourceDestCheck = &val{b2s(sdc)}
	case "ebsOptimized":
		o.EbsOptimized = &val{b2s(inst.EbsOptimized)}
	case "enaSupport":
		o.EnaSupport = &val{"true"}
	case "sriovNetSupport":
		o.SriovNetSupport = &val{"simple"}
	case "groupSet":
		o.Groups = x.Groups
	case "blockDeviceMapping":
		o.BlockDeviceMappings = x.BlockDeviceMapping
	case "enclaveOptions":
		o.Enclave = &enclaveXML{}
	case "productCodes":
		o.ProductCodes = &struct{}{}
	case "kernel":
		o.Kernel = &struct{}{}
	case "ramdisk":
		o.Ramdisk = &struct{}{}
	default:
		return nil, errorf("InvalidParameterValue", "Value (%s) for parameter attribute is invalid. Unknown attribute.", a)
	}
	return o, nil
}

type creditSpec struct {
	InstanceId string `xml:"instanceId"`
	CpuCredits string `xml:"cpuCredits"`
}

func describeInstanceCreditSpecifications(c *call) (any, error) {
	var out []creditSpec
	ids := c.p.list("InstanceId")
	insts, err := all[Instance](c, tInstance)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !slices.ContainsFunc(insts, func(i *Instance) bool { return i.InstanceId == id }) {
			return nil, notFound("InvalidInstanceID.NotFound", id)
		}
	}
	for _, inst := range insts {
		if len(ids) > 0 && !slices.Contains(ids, inst.InstanceId) {
			continue
		}
		if inst.CpuCredits == "" {
			if len(ids) > 0 {
				return nil, errorf("InvalidInstanceType", "The instance type %s does not support CPU credits.", inst.InstanceType)
			}
			continue
		}
		out = append(out, creditSpec{inst.InstanceId, inst.CpuCredits})
	}
	return struct {
		Specs []creditSpec `xml:"instanceCreditSpecificationSet>item"`
	}{out}, nil
}

func modifyInstanceCreditSpecification(c *call) (any, error) {
	type ok struct {
		InstanceId string `xml:"instanceId"`
	}
	var done []ok
	for _, s := range c.p.sub("InstanceCreditSpecification") {
		inst, err := c.instance(s.get("InstanceId"))
		if err != nil {
			return nil, err
		}
		credits := s.get("CpuCredits")
		if credits != "standard" && credits != "unlimited" {
			return nil, invalidParam("CpuCredits", credits)
		}
		if inst.CpuCredits == "" {
			return nil, errorf("InvalidInstanceType", "The instance type %s does not support CPU credits.", inst.InstanceType)
		}
		inst.CpuCredits = credits
		if err := c.put(tInstance, inst); err != nil {
			return nil, err
		}
		done = append(done, ok{inst.InstanceId})
	}
	return struct {
		OK []ok `xml:"successfulInstanceCreditSpecificationSet>item"`
	}{done}, nil
}

func modifyInstanceMetadataOptions(c *call) (any, error) {
	inst, err := c.instance(c.p.get("InstanceId"))
	if err != nil {
		return nil, err
	}
	md := &inst.Metadata
	if v := c.p.get("HttpTokens"); v != "" {
		md.HttpTokens = v
	}
	if v := c.p.integer("HttpPutResponseHopLimit", 0); v > 0 {
		md.HttpPutResponseHopLimit = v
	}
	if v := c.p.get("HttpEndpoint"); v != "" {
		md.HttpEndpoint = v
	}
	if v := c.p.get("HttpProtocolIpv6"); v != "" {
		md.HttpProtocolIpv6 = v
	}
	if v := c.p.get("InstanceMetadataTags"); v != "" {
		md.InstanceMetadataTags = v
	}
	md.State = "applied"
	if err := c.put(tInstance, inst); err != nil {
		return nil, err
	}
	return struct {
		InstanceId string          `xml:"instanceId"`
		Options    MetadataOptions `xml:"instanceMetadataOptions"`
	}{inst.InstanceId, *md}, nil
}

func modifyInstanceMaintenanceOptions(c *call) (any, error) {
	inst, err := c.instance(c.p.get("InstanceId"))
	if err != nil {
		return nil, err
	}
	if v := c.p.get("AutoRecovery"); v != "" {
		inst.AutoRecovery = v
	}
	if err := c.put(tInstance, inst); err != nil {
		return nil, err
	}
	return struct {
		InstanceId   string `xml:"instanceId"`
		AutoRecovery string `xml:"autoRecovery"`
	}{inst.InstanceId, inst.AutoRecovery}, nil
}

func describeInstanceStatus(c *call) (any, error) {
	type detail struct {
		Name   string `xml:"name"`
		Status string `xml:"status"`
	}
	type summary struct {
		Status  string   `xml:"status"`
		Details []detail `xml:"details>item"`
	}
	type status struct {
		InstanceId       string        `xml:"instanceId"`
		AvailabilityZone string        `xml:"availabilityZone"`
		State            InstanceState `xml:"instanceState"`
		SystemStatus     summary       `xml:"systemStatus"`
		InstanceStatus   summary       `xml:"instanceStatus"`
	}
	insts, err := all[Instance](c, tInstance)
	if err != nil {
		return nil, err
	}
	ids := c.p.list("InstanceId")
	includeAll := c.p.boolean("IncludeAllInstances")
	var out []status
	for _, inst := range insts {
		if len(ids) > 0 && !slices.Contains(ids, inst.InstanceId) {
			continue
		}
		if inst.State != stateRunning && !includeAll {
			continue
		}
		ok := summary{"ok", []detail{{"reachability", "passed"}}}
		if inst.State != stateRunning {
			ok = summary{"not-applicable", nil}
		}
		out = append(out, status{inst.InstanceId, inst.AvailabilityZone, inst.State, ok, ok})
	}
	return struct {
		S []status `xml:"instanceStatusSet>item"`
	}{out}, nil
}

type iamAssocXML struct {
	AssociationId string        `xml:"associationId"`
	InstanceId    string        `xml:"instanceId"`
	Profile       iamProfileXML `xml:"iamInstanceProfile"`
	State         string        `xml:"state"`
}

func (c *call) iamAssoc(inst *Instance) iamAssocXML {
	x := c.render(inst)
	return iamAssocXML{inst.IamAssociationId, inst.InstanceId, *x.IamInstanceProfile, "associated"}
}

func describeIamInstanceProfileAssociations(c *call) (any, error) {
	insts, err := all[Instance](c, tInstance)
	if err != nil {
		return nil, err
	}
	ids := c.p.list("AssociationId")
	var out []iamAssocXML
	for _, inst := range insts {
		if inst.IamInstanceProfile == "" || (len(ids) > 0 && !slices.Contains(ids, inst.IamAssociationId)) {
			continue
		}
		vals := map[string][]string{"instance-id": {inst.InstanceId}, "state": {"associated"}}
		if ok, err := matchVals(c.p.filters(), vals); err != nil {
			return nil, err
		} else if ok {
			out = append(out, c.iamAssoc(inst))
		}
	}
	return struct {
		A []iamAssocXML `xml:"iamInstanceProfileAssociationSet>item"`
	}{out}, nil
}

func (c *call) profileARN() string {
	if n := c.p.get("IamInstanceProfile.Name"); n != "" {
		return "arn:aws:iam::" + c.account + ":instance-profile/" + n
	}
	return c.p.get("IamInstanceProfile.Arn")
}

func associateIamInstanceProfile(c *call) (any, error) {
	inst, err := c.instance(c.p.get("InstanceId"))
	if err != nil {
		return nil, err
	}
	if inst.IamInstanceProfile != "" {
		return nil, errorf("IncorrectState", "There is an existing association for instance %s", inst.InstanceId)
	}
	inst.IamInstanceProfile, inst.IamAssociationId = c.profileARN(), newID("iip-assoc")
	if err := c.put(tInstance, inst); err != nil {
		return nil, err
	}
	return struct {
		A iamAssocXML `xml:"iamInstanceProfileAssociation"`
	}{c.iamAssoc(inst)}, nil
}

func (c *call) instanceByIamAssoc(id string) (*Instance, error) {
	insts, err := all[Instance](c, tInstance)
	if err != nil {
		return nil, err
	}
	for _, inst := range insts {
		if inst.IamAssociationId == id && id != "" {
			return inst, nil
		}
	}
	return nil, errorf("InvalidAssociationID.NotFound", "An association with ID %s does not exist.", id)
}

func disassociateIamInstanceProfile(c *call) (any, error) {
	inst, err := c.instanceByIamAssoc(c.p.get("AssociationId"))
	if err != nil {
		return nil, err
	}
	a := c.iamAssoc(inst)
	a.State = "disassociating"
	inst.IamInstanceProfile, inst.IamAssociationId = "", ""
	return struct {
		A iamAssocXML `xml:"iamInstanceProfileAssociation"`
	}{a}, c.put(tInstance, inst)
}

func replaceIamInstanceProfileAssociation(c *call) (any, error) {
	inst, err := c.instanceByIamAssoc(c.p.get("AssociationId"))
	if err != nil {
		return nil, err
	}
	inst.IamInstanceProfile, inst.IamAssociationId = c.profileARN(), newID("iip-assoc")
	return struct {
		A iamAssocXML `xml:"iamInstanceProfileAssociation"`
	}{c.iamAssoc(inst)}, c.put(tInstance, inst)
}
