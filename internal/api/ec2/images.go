// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"slices"
	"strings"
)

const (
	amazonOwner    = "137112412989"
	canonicalOwner = "099720109477"
)

type Image struct {
	ImageId            string     `xml:"imageId"`
	ImageLocation      string     `xml:"imageLocation"`
	ImageState         string     `xml:"imageState"`
	ImageOwnerId       string     `xml:"imageOwnerId"`
	ImageOwnerAlias    string     `xml:"imageOwnerAlias,omitempty"`
	CreationDate       string     `xml:"creationDate"`
	DeprecationTime    string     `xml:"deprecationTime,omitempty"`
	IsPublic           bool       `xml:"isPublic"`
	Architecture       string     `xml:"architecture"`
	ImageType          string     `xml:"imageType"`
	PlatformDetails    string     `xml:"platformDetails"`
	UsageOperation     string     `xml:"usageOperation"`
	Name               string     `xml:"name"`
	Description        string     `xml:"description"`
	RootDeviceType     string     `xml:"rootDeviceType"`
	RootDeviceName     string     `xml:"rootDeviceName"`
	BlockDeviceMapping []ImageBDM `xml:"blockDeviceMapping>item"`
	VirtualizationType string     `xml:"virtualizationType"`
	Hypervisor         string     `xml:"hypervisor"`
	EnaSupport         bool       `xml:"enaSupport"`
	SriovNetSupport    string     `xml:"sriovNetSupport"`
	BootMode           string     `xml:"bootMode,omitempty"`
	ImdsSupport        string     `xml:"imdsSupport,omitempty"`
}

type ImageBDM struct {
	DeviceName string   `xml:"deviceName"`
	Ebs        ImageEbs `xml:"ebs"`
}

type ImageEbs struct {
	SnapshotId          string `xml:"snapshotId"`
	VolumeSize          int    `xml:"volumeSize"`
	DeleteOnTermination bool   `xml:"deleteOnTermination"`
	VolumeType          string `xml:"volumeType"`
	Encrypted           bool   `xml:"encrypted"`
	Iops                *int   `xml:"iops,omitempty"`
	Throughput          *int   `xml:"throughput,omitempty"`
}

type imageSpec struct {
	name, desc, arch, owner, alias, date, rootDev, volType string
	imdsV2                                                 bool
}

// imageCatalogue is a small set of public AMIs, two releases of each family
// so that most_recent has something to choose between.
var imageCatalogue = []imageSpec{
	{"al2023-ami-2023.8.20250915.0-kernel-6.1-x86_64", "Amazon Linux 2023 AMI 2023.8.20250915.0 x86_64 HVM kernel-6.1", "x86_64", amazonOwner, "amazon", "2025-09-15T20:11:42.000Z", "/dev/xvda", "gp3", true},
	{"al2023-ami-2023.9.20260929.0-kernel-6.1-x86_64", "Amazon Linux 2023 AMI 2023.9.20260929.0 x86_64 HVM kernel-6.1", "x86_64", amazonOwner, "amazon", "2026-09-29T21:04:13.000Z", "/dev/xvda", "gp3", true},
	{"al2023-ami-2023.8.20250915.0-kernel-6.1-arm64", "Amazon Linux 2023 AMI 2023.8.20250915.0 arm64 HVM kernel-6.1", "arm64", amazonOwner, "amazon", "2025-09-15T20:11:40.000Z", "/dev/xvda", "gp3", true},
	{"al2023-ami-2023.9.20260929.0-kernel-6.1-arm64", "Amazon Linux 2023 AMI 2023.9.20260929.0 arm64 HVM kernel-6.1", "arm64", amazonOwner, "amazon", "2026-09-29T21:04:11.000Z", "/dev/xvda", "gp3", true},
	{"al2023-ami-minimal-2023.9.20260929.0-kernel-6.1-x86_64", "Amazon Linux 2023 AMI 2023.9.20260929.0 x86_64 Minimal HVM kernel-6.1", "x86_64", amazonOwner, "amazon", "2026-09-29T21:04:20.000Z", "/dev/xvda", "gp3", true},
	{"amzn2-ami-kernel-5.10-hvm-2.0.20250915.0-x86_64-gp2", "Amazon Linux 2 Kernel 5.10 AMI 2.0.20250915.0 x86_64 HVM gp2", "x86_64", amazonOwner, "amazon", "2025-09-16T01:10:05.000Z", "/dev/xvda", "gp2", false},
	{"amzn2-ami-kernel-5.10-hvm-2.0.20260915.0-x86_64-gp2", "Amazon Linux 2 Kernel 5.10 AMI 2.0.20260915.0 x86_64 HVM gp2", "x86_64", amazonOwner, "amazon", "2026-09-16T01:12:44.000Z", "/dev/xvda", "gp2", false},
	{"ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20260301", "Canonical, Ubuntu, 24.04, amd64 noble image", "x86_64", canonicalOwner, "", "2026-03-01T08:44:01.000Z", "/dev/sda1", "gp3", false},
	{"ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20260912", "Canonical, Ubuntu, 24.04, amd64 noble image", "x86_64", canonicalOwner, "", "2026-09-12T07:31:18.000Z", "/dev/sda1", "gp3", false},
	{"ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-20260912", "Canonical, Ubuntu, 24.04, arm64 noble image", "arm64", canonicalOwner, "", "2026-09-12T07:31:09.000Z", "/dev/sda1", "gp3", false},
	{"ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-20260301", "Canonical, Ubuntu, 22.04, amd64 jammy image", "x86_64", canonicalOwner, "", "2026-03-01T09:02:55.000Z", "/dev/sda1", "gp2", false},
	{"ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-20260910", "Canonical, Ubuntu, 22.04, amd64 jammy image", "x86_64", canonicalOwner, "", "2026-09-10T06:58:31.000Z", "/dev/sda1", "gp2", false},
}

func images(region string) []Image {
	var out []Image
	for _, s := range imageCatalogue {
		id := stableID("ami", region, s.name)
		loc := s.owner + "/" + s.name
		if s.alias != "" {
			loc = s.alias + "/" + s.name
		}
		img := Image{
			ImageId: id, ImageLocation: loc, ImageState: "available", ImageOwnerId: s.owner, ImageOwnerAlias: s.alias,
			CreationDate: s.date, IsPublic: true, Architecture: s.arch, ImageType: "machine",
			PlatformDetails: "Linux/UNIX", UsageOperation: "RunInstances", Name: s.name, Description: s.desc,
			RootDeviceType: "ebs", RootDeviceName: s.rootDev, VirtualizationType: "hvm", Hypervisor: "xen",
			EnaSupport: true, SriovNetSupport: "simple",
			BlockDeviceMapping: []ImageBDM{{DeviceName: s.rootDev, Ebs: ImageEbs{
				SnapshotId: stableID("snap", region, s.name), VolumeSize: 8, DeleteOnTermination: true, VolumeType: s.volType,
			}}},
		}
		if strings.HasPrefix(s.date, "2026") {
			img.DeprecationTime = "2028" + s.date[4:]
		} else {
			img.DeprecationTime = "2027" + s.date[4:]
		}
		if s.volType == "gp3" {
			iops, tp := 3000, 125
			img.BlockDeviceMapping[0].Ebs.Iops, img.BlockDeviceMapping[0].Ebs.Throughput = &iops, &tp
		}
		if s.imdsV2 {
			img.BootMode, img.ImdsSupport = "uefi-preferred", "v2.0"
		}
		out = append(out, img)
	}
	return out
}

func imageByID(region, id string) (Image, bool) {
	for _, img := range images(region) {
		if img.ImageId == id {
			return img, true
		}
	}
	return Image{}, false
}

func (img Image) filterValues() map[string][]string {
	bdm := img.BlockDeviceMapping[0]
	return map[string][]string{
		"architecture": {img.Architecture}, "description": {img.Description}, "image-id": {img.ImageId},
		"image-type": {img.ImageType}, "is-public": {b2s(img.IsPublic)}, "name": {img.Name},
		"owner-alias": nonEmpty(img.ImageOwnerAlias), "owner-id": {img.ImageOwnerId},
		"platform-details": {img.PlatformDetails}, "root-device-name": {img.RootDeviceName},
		"root-device-type": {img.RootDeviceType}, "state": {img.ImageState},
		"virtualization-type": {img.VirtualizationType}, "hypervisor": {img.Hypervisor},
		"ena-support": {b2s(img.EnaSupport)}, "creation-date": {img.CreationDate},
		"manifest-location": {img.ImageLocation}, "sriov-net-support": {img.SriovNetSupport},
		"usage-operation": {img.UsageOperation}, "boot-mode": nonEmpty(img.BootMode),
		"block-device-mapping.device-name":           {bdm.DeviceName},
		"block-device-mapping.snapshot-id":           {bdm.Ebs.SnapshotId},
		"block-device-mapping.volume-size":           {i2s(bdm.Ebs.VolumeSize)},
		"block-device-mapping.volume-type":           {bdm.Ebs.VolumeType},
		"block-device-mapping.delete-on-termination": {b2s(bdm.Ebs.DeleteOnTermination)},
		"block-device-mapping.encrypted":             {b2s(bdm.Ebs.Encrypted)},
		"platform":                                   nil,
		"product-code":                               nil,
	}
}

func describeImages(c *call) (any, error) {
	ids := c.p.list("ImageId")
	owners := c.p.list("Owner")
	fs := c.p.filters()
	var out []Image
	for _, img := range images(c.region) {
		if len(ids) > 0 && !slices.Contains(ids, img.ImageId) {
			continue
		}
		if len(owners) > 0 && !slices.ContainsFunc(owners, func(o string) bool {
			return o == img.ImageOwnerId || (o != "" && o == img.ImageOwnerAlias)
		}) {
			continue
		}
		vals := img.filterValues()
		ok, err := matchVals(fs, vals)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, img)
		}
	}
	for _, id := range ids {
		if !slices.ContainsFunc(out, func(i Image) bool { return i.ImageId == id }) && len(fs) == 0 && len(owners) == 0 {
			if _, ok := imageByID(c.region, id); !ok {
				return nil, errorf("InvalidAMIID.NotFound", "The image id '[%s]' does not exist", id)
			}
		}
	}
	return struct {
		Images []Image `xml:"imagesSet>item"`
	}{out}, nil
}

// Instance types.

type instanceTypeSpec struct {
	name       string
	vcpus, mem int
	arch       string
	burstable  bool
	freeTier   bool
	network    string
}

var instanceTypes = []instanceTypeSpec{
	{"t2.nano", 1, 512, "x86_64", true, false, "Low to Moderate"},
	{"t2.micro", 1, 1024, "x86_64", true, true, "Low to Moderate"},
	{"t2.small", 1, 2048, "x86_64", true, false, "Low to Moderate"},
	{"t2.medium", 2, 4096, "x86_64", true, false, "Low to Moderate"},
	{"t3.nano", 2, 512, "x86_64", true, false, "Up to 5 Gigabit"},
	{"t3.micro", 2, 1024, "x86_64", true, true, "Up to 5 Gigabit"},
	{"t3.small", 2, 2048, "x86_64", true, false, "Up to 5 Gigabit"},
	{"t3.medium", 2, 4096, "x86_64", true, false, "Up to 5 Gigabit"},
	{"t3.large", 2, 8192, "x86_64", true, false, "Up to 5 Gigabit"},
	{"t3.xlarge", 4, 16384, "x86_64", true, false, "Up to 5 Gigabit"},
	{"t3a.micro", 2, 1024, "x86_64", true, false, "Up to 5 Gigabit"},
	{"t3a.small", 2, 2048, "x86_64", true, false, "Up to 5 Gigabit"},
	{"t3a.medium", 2, 4096, "x86_64", true, false, "Up to 5 Gigabit"},
	{"t4g.nano", 2, 512, "arm64", true, false, "Up to 5 Gigabit"},
	{"t4g.micro", 2, 1024, "arm64", true, true, "Up to 5 Gigabit"},
	{"t4g.small", 2, 2048, "arm64", true, false, "Up to 5 Gigabit"},
	{"t4g.medium", 2, 4096, "arm64", true, false, "Up to 5 Gigabit"},
	{"m5.large", 2, 8192, "x86_64", false, false, "Up to 10 Gigabit"},
	{"m5.xlarge", 4, 16384, "x86_64", false, false, "Up to 10 Gigabit"},
	{"m6i.large", 2, 8192, "x86_64", false, false, "Up to 12.5 Gigabit"},
	{"m7g.large", 2, 8192, "arm64", false, false, "Up to 12.5 Gigabit"},
	{"c5.large", 2, 4096, "x86_64", false, false, "Up to 10 Gigabit"},
	{"c6i.large", 2, 4096, "x86_64", false, false, "Up to 12.5 Gigabit"},
	{"r5.large", 2, 16384, "x86_64", false, false, "Up to 10 Gigabit"},
}

func instanceTypeByName(name string) (instanceTypeSpec, bool) {
	for _, t := range instanceTypes {
		if t.name == name {
			return t, true
		}
	}
	return instanceTypeSpec{}, false
}

type InstanceTypeInfo struct {
	InstanceType                 string   `xml:"instanceType"`
	CurrentGeneration            bool     `xml:"currentGeneration"`
	FreeTierEligible             bool     `xml:"freeTierEligible"`
	SupportedUsageClasses        []string `xml:"supportedUsageClasses>item"`
	SupportedRootDeviceTypes     []string `xml:"supportedRootDeviceTypes>item"`
	SupportedVirtualizationTypes []string `xml:"supportedVirtualizationTypes>item"`
	BareMetal                    bool     `xml:"bareMetal"`
	Hypervisor                   string   `xml:"hypervisor"`
	ProcessorInfo                struct {
		SupportedArchitectures   []string `xml:"supportedArchitectures>item"`
		SustainedClockSpeedInGhz float64  `xml:"sustainedClockSpeedInGhz"`
	} `xml:"processorInfo"`
	VCpuInfo struct {
		DefaultVCpus          int `xml:"defaultVCpus"`
		DefaultCores          int `xml:"defaultCores"`
		DefaultThreadsPerCore int `xml:"defaultThreadsPerCore"`
	} `xml:"vCpuInfo"`
	MemoryInfo struct {
		SizeInMiB int `xml:"sizeInMiB"`
	} `xml:"memoryInfo"`
	InstanceStorageSupported bool `xml:"instanceStorageSupported"`
	EbsInfo                  struct {
		EbsOptimizedSupport string `xml:"ebsOptimizedSupport"`
		EncryptionSupport   string `xml:"encryptionSupport"`
		NvmeSupport         string `xml:"nvmeSupport"`
		EbsOptimizedInfo    struct {
			BaselineBandwidthInMbps  int     `xml:"baselineBandwidthInMbps"`
			BaselineIops             int     `xml:"baselineIops"`
			BaselineThroughputInMBps float64 `xml:"baselineThroughputInMBps"`
			MaximumBandwidthInMbps   int     `xml:"maximumBandwidthInMbps"`
			MaximumIops              int     `xml:"maximumIops"`
			MaximumThroughputInMBps  float64 `xml:"maximumThroughputInMBps"`
		} `xml:"ebsOptimizedInfo"`
	} `xml:"ebsInfo"`
	NetworkInfo struct {
		NetworkPerformance        string        `xml:"networkPerformance"`
		MaximumNetworkInterfaces  int           `xml:"maximumNetworkInterfaces"`
		Ipv4AddressesPerInterface int           `xml:"ipv4AddressesPerInterface"`
		Ipv6AddressesPerInterface int           `xml:"ipv6AddressesPerInterface"`
		Ipv6Supported             bool          `xml:"ipv6Supported"`
		EnaSupport                string        `xml:"enaSupport"`
		MaximumNetworkCards       int           `xml:"maximumNetworkCards"`
		DefaultNetworkCardIndex   int           `xml:"defaultNetworkCardIndex"`
		EfaSupported              bool          `xml:"efaSupported"`
		EnaSrdSupported           bool          `xml:"enaSrdSupported"`
		EncryptionInTransit       bool          `xml:"encryptionInTransitSupported"`
		NetworkCards              []networkCard `xml:"networkCards>item"`
	} `xml:"networkInfo"`
	PlacementGroupInfo struct {
		SupportedStrategies []string `xml:"supportedStrategies>item"`
	} `xml:"placementGroupInfo"`
	HibernationSupported          bool     `xml:"hibernationSupported"`
	BurstablePerformanceSupported bool     `xml:"burstablePerformanceSupported"`
	DedicatedHostsSupported       bool     `xml:"dedicatedHostsSupported"`
	AutoRecoverySupported         bool     `xml:"autoRecoverySupported"`
	SupportedBootModes            []string `xml:"supportedBootModes>item"`
}

type networkCard struct {
	NetworkCardIndex         int    `xml:"networkCardIndex"`
	NetworkPerformance       string `xml:"networkPerformance"`
	MaximumNetworkInterfaces int    `xml:"maximumNetworkInterfaces"`
}

func (t instanceTypeSpec) info() InstanceTypeInfo {
	var i InstanceTypeInfo
	i.InstanceType = t.name
	i.CurrentGeneration = !strings.HasPrefix(t.name, "t2.")
	i.FreeTierEligible = t.freeTier
	i.SupportedUsageClasses = []string{"on-demand", "spot"}
	i.SupportedRootDeviceTypes = []string{"ebs"}
	i.SupportedVirtualizationTypes = []string{"hvm"}
	i.Hypervisor = "nitro"
	if strings.HasPrefix(t.name, "t2.") {
		i.Hypervisor = "xen"
	}
	i.ProcessorInfo.SupportedArchitectures = []string{t.arch}
	i.ProcessorInfo.SustainedClockSpeedInGhz = 2.5
	i.VCpuInfo.DefaultVCpus = t.vcpus
	i.VCpuInfo.DefaultThreadsPerCore = 1
	if t.vcpus > 1 && t.arch == "x86_64" {
		i.VCpuInfo.DefaultThreadsPerCore = 2
	}
	i.VCpuInfo.DefaultCores = t.vcpus / i.VCpuInfo.DefaultThreadsPerCore
	i.MemoryInfo.SizeInMiB = t.mem
	i.EbsInfo.EbsOptimizedSupport = "default"
	i.EbsInfo.EncryptionSupport = "supported"
	i.EbsInfo.NvmeSupport = "required"
	i.NetworkInfo.NetworkPerformance = t.network
	i.NetworkInfo.MaximumNetworkInterfaces = 3
	i.NetworkInfo.Ipv4AddressesPerInterface = 6
	i.NetworkInfo.Ipv6AddressesPerInterface = 6
	i.NetworkInfo.Ipv6Supported = true
	i.NetworkInfo.EnaSupport = "required"
	i.NetworkInfo.MaximumNetworkCards = 1
	i.NetworkInfo.NetworkCards = []networkCard{{0, t.network, 3}}
	i.PlacementGroupInfo.SupportedStrategies = []string{"partition", "spread"}
	if !t.burstable {
		i.PlacementGroupInfo.SupportedStrategies = []string{"cluster", "partition", "spread"}
	}
	e := &i.EbsInfo.EbsOptimizedInfo
	e.BaselineBandwidthInMbps, e.BaselineIops, e.BaselineThroughputInMBps = 2085, 12000, 260.625
	e.MaximumBandwidthInMbps, e.MaximumIops, e.MaximumThroughputInMBps = 4750, 20000, 593.75
	i.HibernationSupported = true
	i.BurstablePerformanceSupported = t.burstable
	i.AutoRecoverySupported = true
	i.SupportedBootModes = []string{"legacy-bios", "uefi"}
	if t.arch == "arm64" {
		i.SupportedBootModes = []string{"uefi"}
	}
	return i
}

func describeInstanceTypes(c *call) (any, error) {
	names := c.p.list("InstanceType")
	fs := c.p.filters()
	var out []InstanceTypeInfo
	for _, n := range names {
		if _, ok := instanceTypeByName(n); !ok {
			return nil, errorf("InvalidInstanceType", "The following supplied instance types do not exist: [%s]", n)
		}
	}
	for _, t := range instanceTypes {
		if len(names) > 0 && !slices.Contains(names, t.name) {
			continue
		}
		vals := map[string][]string{
			"instance-type": {t.name}, "processor-info.supported-architecture": {t.arch},
			"burstable-performance-supported": {b2s(t.burstable)}, "free-tier-eligible": {b2s(t.freeTier)},
			"current-generation": {b2s(!strings.HasPrefix(t.name, "t2."))}, "vcpu-info.default-vcpus": {i2s(t.vcpus)},
			"memory-info.size-in-mib": {i2s(t.mem)},
		}
		ok, err := matchVals(fs, vals)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, t.info())
		}
	}
	return struct {
		Types []InstanceTypeInfo `xml:"instanceTypeSet>item"`
	}{out}, nil
}

func describeInstanceTypeOfferings(c *call) (any, error) {
	type offering struct {
		InstanceType string `xml:"instanceType"`
		LocationType string `xml:"locationType"`
		Location     string `xml:"location"`
	}
	locType := c.p.get("LocationType")
	if locType == "" {
		locType = "region"
	}
	locs := []string{c.region}
	if locType == "availability-zone" {
		locs = zoneNames(c.region)
	} else if locType == "availability-zone-id" {
		locs = nil
		for _, z := range zones(c.region) {
			locs = append(locs, z.ID)
		}
	}
	fs := c.p.filters()
	var out []offering
	for _, t := range instanceTypes {
		for _, l := range locs {
			vals := map[string][]string{"instance-type": {t.name}, "location": {l}}
			ok, err := matchVals(fs, vals)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, offering{t.name, locType, l})
			}
		}
	}
	return struct {
		Offerings []offering `xml:"instanceTypeOfferingSet>item"`
	}{out}, nil
}
