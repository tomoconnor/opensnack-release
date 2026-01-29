// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import "encoding/xml"

//
// COMMON
//

type ResponseMetadata struct {
	XMLName   xml.Name `xml:"ResponseMetadata"`
	RequestId string   `xml:"RequestId"`
}

//
// RunInstances
//

type RunInstancesResult struct {
	XMLName       xml.Name   `xml:"RunInstancesResult"`
	ReservationId string     `xml:"reservationId"`
	OwnerId       string     `xml:"ownerId"`
	GroupSet      GroupSet   `xml:"groupSet"`
	Instances     []Instance `xml:"instancesSet>item"`
}

type GroupSet struct {
	XMLName xml.Name        `xml:"groupSet"`
	Items   []SecurityGroup `xml:"item"`
}

type SecurityGroup struct {
	XMLName   xml.Name `xml:"item"`
	GroupId   string   `xml:"groupId"`
	GroupName string   `xml:"groupName"`
}

type RunInstancesResponse struct {
	XMLName       xml.Name   `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ RunInstancesResponse"`
	RequestId     string     `xml:"requestId"`
	ReservationId string     `xml:"reservationId"`
	OwnerId       string     `xml:"ownerId"`
	GroupSet      GroupSet   `xml:"groupSet"`
	Instances     []Instance `xml:"instancesSet>item"`
}

//
// DescribeInstances
//

type DescribeInstancesResponse struct {
	XMLName      xml.Name      `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeInstancesResponse"`
	RequestId    string        `xml:"requestId"`
	Reservations []Reservation `xml:"reservationSet>item"`
}

type Reservation struct {
	XMLName       xml.Name   `xml:"item"`
	ReservationId string     `xml:"reservationId"`
	OwnerId       string     `xml:"ownerId"`
	GroupSet      GroupSet   `xml:"groupSet"`
	Instances     []Instance `xml:"instancesSet>item"`
}

type Instance struct {
	XMLName             xml.Name              `xml:"item"`
	InstanceId          string                `xml:"instanceId"`
	ImageId             string                `xml:"imageId"`
	InstanceType        string                `xml:"instanceType"`
	InstanceState       InstanceState         `xml:"instanceState"`
	PrivateDnsName      string                `xml:"privateDnsName"`
	PrivateIpAddress    string                `xml:"privateIpAddress"`
	PublicDnsName       string                `xml:"dnsName,omitempty"`
	PublicIpAddress     string                `xml:"ipAddress,omitempty"`
	SubnetId            string                `xml:"subnetId"`
	VpcId               string                `xml:"vpcId"`
	RootDeviceType      string                `xml:"rootDeviceType"`
	RootDeviceName      string                `xml:"rootDeviceName"`
	LaunchTime          string                `xml:"launchTime"`
	Placement           Placement             `xml:"placement"`
	BlockDeviceMappings BlockDeviceMappingSet `xml:"blockDeviceMapping"`
	NetworkInterfaces   NetworkInterfaceSet   `xml:"networkInterfaceSet"`
	SecurityGroups      SecurityGroupSet      `xml:"securityGroupSet"`
	MetadataOptions     MetadataOptions       `xml:"metadataOptions"`
}

type MetadataOptions struct {
	XMLName                 xml.Name `xml:"metadataOptions"`
	HttpTokens              string   `xml:"httpTokens"`
	HttpPutResponseHopLimit int      `xml:"httpPutResponseHopLimit"`
	HttpEndpoint            string   `xml:"httpEndpoint"`
}

type BlockDeviceMappingSet struct {
	XMLName xml.Name             `xml:"blockDeviceMapping"`
	Items   []BlockDeviceMapping `xml:"item"`
}

type BlockDeviceMapping struct {
	XMLName    xml.Name       `xml:"item"`
	DeviceName string         `xml:"deviceName"`
	Ebs        EbsBlockDevice `xml:"ebs"`
}

type EbsBlockDevice struct {
	XMLName             xml.Name `xml:"ebs"`
	VolumeId            string   `xml:"volumeId"`
	Status              string   `xml:"status"`
	AttachTime          string   `xml:"attachTime"`
	DeleteOnTermination bool     `xml:"deleteOnTermination"`
}

type NetworkInterfaceSet struct {
	XMLName xml.Name           `xml:"networkInterfaceSet"`
	Items   []NetworkInterface `xml:"item"`
}

type NetworkInterface struct {
	XMLName               xml.Name              `xml:"item"`
	NetworkInterfaceId    string                `xml:"networkInterfaceId"`
	SubnetId              string                `xml:"subnetId"`
	VpcId                 string                `xml:"vpcId"`
	OwnerId               string                `xml:"ownerId"`
	Status                string                `xml:"status"`
	MacAddress            string                `xml:"macAddress"`
	PrivateIpAddress      string                `xml:"privateIpAddress"`
	PrivateIpAddressesSet PrivateIpAddressesSet `xml:"privateIpAddressesSet"`
	Groups                GroupSet              `xml:"groupSet"`
	Attachment            NetworkAttachment     `xml:"attachment"`
	SourceDestCheck       bool                  `xml:"sourceDestCheck"`
}

type PrivateIpAddressesSet struct {
	XMLName xml.Name               `xml:"privateIpAddressesSet"`
	Items   []PrivateIpAddressInfo `xml:"item"`
}

type PrivateIpAddressInfo struct {
	XMLName          xml.Name `xml:"item"`
	PrivateIpAddress string   `xml:"privateIpAddress"`
	Primary          bool     `xml:"primary"`
}

type NetworkAttachment struct {
	XMLName             xml.Name `xml:"attachment"`
	AttachmentId        string   `xml:"attachmentId"`
	DeviceIndex         int      `xml:"deviceIndex"`
	Status              string   `xml:"status"`
	AttachTime          string   `xml:"attachTime"`
	DeleteOnTermination bool     `xml:"deleteOnTermination"`
}

//
// DescribeInstanceTypes
//

type DescribeInstanceTypesResponse struct {
	XMLName       xml.Name           `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeInstanceTypesResponse"`
	RequestId     string             `xml:"requestId"`
	InstanceTypes []InstanceTypeInfo `xml:"instanceTypeSet>item"`
}

type InstanceTypeInfo struct {
	XMLName      xml.Name   `xml:"item"`
	InstanceType string     `xml:"instanceType"`
	VcpuInfo     VcpuInfo   `xml:"vcpuInfo"`
	MemoryInfo   MemoryInfo `xml:"memoryInfo"`
}

type VcpuInfo struct {
	XMLName      xml.Name `xml:"vcpuInfo"`
	DefaultVcpus int      `xml:"defaultVCpus"`
}

type MemoryInfo struct {
	XMLName   xml.Name `xml:"memoryInfo"`
	SizeInMiB int      `xml:"sizeInMiB"`
}

//
// DescribeTags
//

type DescribeTagsResponse struct {
	XMLName   xml.Name `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeTagsResponse"`
	RequestId string   `xml:"requestId"`
	TagSet    TagSet   `xml:"tagSet"`
}

type TagSet struct {
	XMLName xml.Name `xml:"tagSet"`
	Items   []Tag    `xml:"item"`
}

type Tag struct {
	XMLName      xml.Name `xml:"item"`
	ResourceId   string   `xml:"resourceId"`
	ResourceType string   `xml:"resourceType"`
	Key          string   `xml:"key"`
	Value        string   `xml:"value"`
}

//
// DescribeVpcs
//

type DescribeVpcsResponse struct {
	XMLName   xml.Name `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeVpcsResponse"`
	RequestId string   `xml:"requestId"`
	Vpcs      []Vpc    `xml:"vpcSet>item"`
}

type Vpc struct {
	XMLName         xml.Name `xml:"item"`
	VpcId           string   `xml:"vpcId"`
	OwnerId         string   `xml:"ownerId"`
	CidrBlock       string   `xml:"cidrBlock"`
	InstanceTenancy string   `xml:"instanceTenancy"`
	IsDefault       bool     `xml:"isDefault"`
	DhcpOptionsId   string   `xml:"dhcpOptionsId,omitempty"`
	State           string   `xml:"state"`
}

//
// DescribeInstanceAttribute
//

type DescribeInstanceAttributeResponse struct {
	XMLName                           xml.Name        `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeInstanceAttributeResponse"`
	RequestId                         string          `xml:"requestId"`
	InstanceId                        string          `xml:"instanceId"`
	InstanceInitiatedShutdownBehavior *AttributeValue `xml:"instanceInitiatedShutdownBehavior,omitempty"`
	DisableApiStop                    *AttributeValue `xml:"disableApiStop,omitempty"`
	DisableApiTermination             *AttributeValue `xml:"disableApiTermination,omitempty"`
}

type AttributeValue struct {
	Value string `xml:"value"`
}

type SecurityGroupSet struct {
	XMLName xml.Name        `xml:"securityGroupSet"`
	Items   []SecurityGroup `xml:"item"`
}

type InstanceState struct {
	XMLName xml.Name `xml:"instanceState"`
	Code    int      `xml:"code"`
	Name    string   `xml:"name"`
}

type Placement struct {
	XMLName          xml.Name `xml:"placement"`
	AvailabilityZone string   `xml:"availabilityZone"`
}

//
// TerminateInstances
//

type TerminateInstancesResult struct {
	XMLName   xml.Name              `xml:"TerminateInstancesResult"`
	Instances []InstanceStateChange `xml:"instancesSet>item"`
}

type TerminateInstancesResponse struct {
	XMLName                  xml.Name                 `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ TerminateInstancesResponse"`
	TerminateInstancesResult TerminateInstancesResult `xml:"TerminateInstancesResult"`
	ResponseMetadata         ResponseMetadata         `xml:"ResponseMetadata"`
}

type InstanceStateChange struct {
	XMLName       xml.Name      `xml:"item"`
	InstanceId    string        `xml:"instanceId"`
	CurrentState  InstanceState `xml:"currentState"`
	PreviousState InstanceState `xml:"previousState"`
}

//
// CreateVolume
//

type CreateVolumeResponse struct {
	XMLName          xml.Name         `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ CreateVolumeResponse"`
	VolumeId         string           `xml:"volumeId"`
	Size             int              `xml:"size"`
	VolumeType       string           `xml:"volumeType"`
	State            string           `xml:"state"`
	AvailabilityZone string           `xml:"availabilityZone"`
	CreateTime       string           `xml:"createTime"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// DescribeVolumes
//

type DescribeVolumesResponse struct {
	XMLName   xml.Name `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DescribeVolumesResponse"`
	RequestId string   `xml:"requestId"`
	Volumes   []Volume `xml:"volumeSet>item"`
}

type Volume struct {
	XMLName          xml.Name           `xml:"item"`
	VolumeId         string             `xml:"volumeId"`
	Size             int                `xml:"size"`
	VolumeType       string             `xml:"volumeType"`
	State            string             `xml:"state"`
	AvailabilityZone string             `xml:"availabilityZone"`
	CreateTime       string             `xml:"createTime"`
	Attachments      []VolumeAttachment `xml:"attachmentSet>item,omitempty"`
}

//
// DeleteVolume
//

type DeleteVolumeResponse struct {
	XMLName          xml.Name         `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DeleteVolumeResponse"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// AttachVolume
//

type AttachVolumeResult struct {
	XMLName    xml.Name         `xml:"AttachVolumeResult"`
	Attachment VolumeAttachment `xml:"attachment"`
}

type AttachVolumeResponse struct {
	XMLName            xml.Name           `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ AttachVolumeResponse"`
	AttachVolumeResult AttachVolumeResult `xml:"AttachVolumeResult"`
	ResponseMetadata   ResponseMetadata   `xml:"ResponseMetadata"`
}

//
// DetachVolume
//

type DetachVolumeResult struct {
	XMLName    xml.Name         `xml:"DetachVolumeResult"`
	Attachment VolumeAttachment `xml:"attachment"`
}

type DetachVolumeResponse struct {
	XMLName            xml.Name           `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ DetachVolumeResponse"`
	DetachVolumeResult DetachVolumeResult `xml:"DetachVolumeResult"`
	ResponseMetadata   ResponseMetadata   `xml:"ResponseMetadata"`
}

type VolumeAttachment struct {
	XMLName             xml.Name `xml:"item"`
	VolumeId            string   `xml:"volumeId"`
	InstanceId          string   `xml:"instanceId"`
	Device              string   `xml:"device"`
	State               string   `xml:"status"`
	AttachTime          string   `xml:"attachTime,omitempty"`
	DeleteOnTermination bool     `xml:"deleteOnTermination,omitempty"`
}

//
// ModifyInstanceAttribute
//

type ModifyInstanceAttributeResponse struct {
	XMLName          xml.Name         `xml:"http://ec2.amazonaws.com/doc/2016-11-15/ ModifyInstanceAttributeResponse"`
	Return           bool             `xml:"return"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}
