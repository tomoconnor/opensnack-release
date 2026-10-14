// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

// The structs here are both the stored form (JSON) and the wire form (XML).
// Fields tagged xml:"-" are kept in the store but reported only through
// other calls (attributes) or derived on the way out.

type Tag struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}

type state struct {
	State string `xml:"state"`
}

type Vpc struct {
	VpcId                   string         `xml:"vpcId"`
	OwnerId                 string         `xml:"ownerId"`
	State                   string         `xml:"state"`
	CidrBlock               string         `xml:"cidrBlock"`
	CidrBlockAssociationSet []VpcCidrAssoc `xml:"cidrBlockAssociationSet>item"`
	DhcpOptionsId           string         `xml:"dhcpOptionsId"`
	InstanceTenancy         string         `xml:"instanceTenancy"`
	IsDefault               bool           `xml:"isDefault"`
	Tags                    []Tag          `xml:"tagSet>item,omitempty"`

	EnableDnsSupport                 bool `xml:"-"`
	EnableDnsHostnames               bool `xml:"-"`
	EnableNetworkAddressUsageMetrics bool `xml:"-"`
}

type VpcCidrAssoc struct {
	AssociationId  string `xml:"associationId"`
	CidrBlock      string `xml:"cidrBlock"`
	CidrBlockState state  `xml:"cidrBlockState"`
}

type Subnet struct {
	SubnetId                      string         `xml:"subnetId"`
	SubnetArn                     string         `xml:"subnetArn"`
	VpcId                         string         `xml:"vpcId"`
	OwnerId                       string         `xml:"ownerId"`
	State                         string         `xml:"state"`
	CidrBlock                     string         `xml:"cidrBlock"`
	AvailabilityZone              string         `xml:"availabilityZone"`
	AvailabilityZoneId            string         `xml:"availabilityZoneId"`
	AvailableIpAddressCount       int            `xml:"availableIpAddressCount" json:"-"`
	DefaultForAz                  bool           `xml:"defaultForAz"`
	MapPublicIpOnLaunch           bool           `xml:"mapPublicIpOnLaunch"`
	MapCustomerOwnedIpOnLaunch    bool           `xml:"mapCustomerOwnedIpOnLaunch"`
	AssignIpv6AddressOnCreation   bool           `xml:"assignIpv6AddressOnCreation"`
	EnableDns64                   bool           `xml:"enableDns64"`
	Ipv6Native                    bool           `xml:"ipv6Native"`
	EnableLniAtDeviceIndex        *int           `xml:"enableLniAtDeviceIndex,omitempty"`
	PrivateDnsNameOptionsOnLaunch PrivateDnsOpts `xml:"privateDnsNameOptionsOnLaunch"`
	Tags                          []Tag          `xml:"tagSet>item,omitempty"`

	// NetworkAclId is the ACL the subnet is associated with; ACLs report
	// their associations from it.
	NetworkAclId    string `xml:"-"`
	NaclAssociation string `xml:"-"`
}

type PrivateDnsOpts struct {
	HostnameType                    string `xml:"hostnameType"`
	EnableResourceNameDnsARecord    bool   `xml:"enableResourceNameDnsARecord"`
	EnableResourceNameDnsAAAARecord bool   `xml:"enableResourceNameDnsAAAARecord"`
}

type RouteTable struct {
	RouteTableId string    `xml:"routeTableId"`
	VpcId        string    `xml:"vpcId"`
	OwnerId      string    `xml:"ownerId"`
	Routes       []Route   `xml:"routeSet>item"`
	Associations []RTAssoc `xml:"associationSet>item"`
	Tags         []Tag     `xml:"tagSet>item,omitempty"`
}

type Route struct {
	DestinationCidrBlock        string `xml:"destinationCidrBlock,omitempty"`
	DestinationIpv6CidrBlock    string `xml:"destinationIpv6CidrBlock,omitempty"`
	DestinationPrefixListId     string `xml:"destinationPrefixListId,omitempty"`
	GatewayId                   string `xml:"gatewayId,omitempty"`
	NatGatewayId                string `xml:"natGatewayId,omitempty"`
	InstanceId                  string `xml:"instanceId,omitempty"`
	InstanceOwnerId             string `xml:"instanceOwnerId,omitempty"`
	NetworkInterfaceId          string `xml:"networkInterfaceId,omitempty"`
	VpcPeeringConnectionId      string `xml:"vpcPeeringConnectionId,omitempty"`
	TransitGatewayId            string `xml:"transitGatewayId,omitempty"`
	EgressOnlyInternetGatewayId string `xml:"egressOnlyInternetGatewayId,omitempty"`
	LocalGatewayId              string `xml:"localGatewayId,omitempty"`
	CarrierGatewayId            string `xml:"carrierGatewayId,omitempty"`
	CoreNetworkArn              string `xml:"coreNetworkArn,omitempty"`
	State                       string `xml:"state"`
	Origin                      string `xml:"origin"`
}

type RTAssoc struct {
	RouteTableAssociationId string `xml:"routeTableAssociationId"`
	RouteTableId            string `xml:"routeTableId"`
	SubnetId                string `xml:"subnetId,omitempty"`
	GatewayId               string `xml:"gatewayId,omitempty"`
	Main                    bool   `xml:"main"`
	AssociationState        state  `xml:"associationState"`
}

type InternetGateway struct {
	InternetGatewayId string      `xml:"internetGatewayId"`
	OwnerId           string      `xml:"ownerId"`
	Attachments       []IgwAttach `xml:"attachmentSet>item"`
	Tags              []Tag       `xml:"tagSet>item,omitempty"`
}

type IgwAttach struct {
	VpcId string `xml:"vpcId"`
	State string `xml:"state"`
}

type NatGateway struct {
	NatGatewayId     string    `xml:"natGatewayId"`
	SubnetId         string    `xml:"subnetId"`
	VpcId            string    `xml:"vpcId"`
	State            string    `xml:"state"`
	ConnectivityType string    `xml:"connectivityType"`
	AvailabilityMode string    `xml:"availabilityMode"`
	CreateTime       string    `xml:"createTime"`
	DeleteTime       string    `xml:"deleteTime,omitempty"`
	FailureCode      string    `xml:"failureCode,omitempty"`
	FailureMessage   string    `xml:"failureMessage,omitempty"`
	Addresses        []NatAddr `xml:"natGatewayAddressSet>item"`
	Tags             []Tag     `xml:"tagSet>item,omitempty"`
}

type NatAddr struct {
	AllocationId       string `xml:"allocationId,omitempty"`
	NetworkInterfaceId string `xml:"networkInterfaceId"`
	PrivateIp          string `xml:"privateIp"`
	PublicIp           string `xml:"publicIp,omitempty"`
	AssociationId      string `xml:"associationId,omitempty"`
	IsPrimary          bool   `xml:"isPrimary"`
	Status             string `xml:"status"`
}

type Address struct {
	AllocationId            string `xml:"allocationId"`
	PublicIp                string `xml:"publicIp"`
	Domain                  string `xml:"domain"`
	PublicIpv4Pool          string `xml:"publicIpv4Pool"`
	NetworkBorderGroup      string `xml:"networkBorderGroup"`
	AssociationId           string `xml:"associationId,omitempty"`
	InstanceId              string `xml:"instanceId,omitempty"`
	NetworkInterfaceId      string `xml:"networkInterfaceId,omitempty"`
	NetworkInterfaceOwnerId string `xml:"networkInterfaceOwnerId,omitempty"`
	PrivateIpAddress        string `xml:"privateIpAddress,omitempty"`
	Tags                    []Tag  `xml:"tagSet>item,omitempty"`
}

type SecurityGroup struct {
	GroupId             string         `xml:"groupId"`
	GroupName           string         `xml:"groupName"`
	Description         string         `xml:"groupDescription"`
	VpcId               string         `xml:"vpcId"`
	OwnerId             string         `xml:"ownerId"`
	SecurityGroupArn    string         `xml:"securityGroupArn"`
	IpPermissions       []IpPermission `xml:"ipPermissions>item" json:"-"`
	IpPermissionsEgress []IpPermission `xml:"ipPermissionsEgress>item" json:"-"`
	Tags                []Tag          `xml:"tagSet>item,omitempty"`

	Rules []SgRule `xml:"-"`
}

type IpPermission struct {
	IpProtocol    string            `xml:"ipProtocol"`
	FromPort      *int              `xml:"fromPort,omitempty"`
	ToPort        *int              `xml:"toPort,omitempty"`
	Groups        []UserIdGroupPair `xml:"groups>item"`
	IpRanges      []IpRange         `xml:"ipRanges>item"`
	Ipv6Ranges    []Ipv6Range       `xml:"ipv6Ranges>item"`
	PrefixListIds []PrefixListId    `xml:"prefixListIds>item"`
}

type UserIdGroupPair struct {
	GroupId     string `xml:"groupId"`
	UserId      string `xml:"userId"`
	Description string `xml:"description,omitempty"`
}

type IpRange struct {
	CidrIp      string `xml:"cidrIp"`
	Description string `xml:"description,omitempty"`
}

type Ipv6Range struct {
	CidrIpv6    string `xml:"cidrIpv6"`
	Description string `xml:"description,omitempty"`
}

type PrefixListId struct {
	PrefixListId string `xml:"prefixListId"`
	Description  string `xml:"description,omitempty"`
}

// SgRule is one rule as DescribeSecurityGroupRules shows it: a protocol and
// port range with exactly one source or destination.
type SgRule struct {
	SecurityGroupRuleId  string    `xml:"securityGroupRuleId"`
	SecurityGroupRuleArn string    `xml:"securityGroupRuleArn"`
	GroupId              string    `xml:"groupId"`
	GroupOwnerId         string    `xml:"groupOwnerId"`
	IsEgress             bool      `xml:"isEgress"`
	IpProtocol           string    `xml:"ipProtocol"`
	FromPort             int       `xml:"fromPort"`
	ToPort               int       `xml:"toPort"`
	CidrIpv4             string    `xml:"cidrIpv4,omitempty"`
	CidrIpv6             string    `xml:"cidrIpv6,omitempty"`
	PrefixListId         string    `xml:"prefixListId,omitempty"`
	ReferencedGroupInfo  *RefGroup `xml:"referencedGroupInfo,omitempty"`
	Description          string    `xml:"description,omitempty"`
	Tags                 []Tag     `xml:"tagSet>item,omitempty"`
}

type RefGroup struct {
	GroupId string `xml:"groupId"`
	UserId  string `xml:"userId"`
}

type NetworkAcl struct {
	NetworkAclId string      `xml:"networkAclId"`
	VpcId        string      `xml:"vpcId"`
	OwnerId      string      `xml:"ownerId"`
	IsDefault    bool        `xml:"default"`
	Entries      []NaclEntry `xml:"entrySet>item"`
	Associations []NaclAssoc `xml:"associationSet>item" json:"-"`
	Tags         []Tag       `xml:"tagSet>item,omitempty"`
}

type NaclEntry struct {
	RuleNumber    int        `xml:"ruleNumber"`
	Protocol      string     `xml:"protocol"`
	RuleAction    string     `xml:"ruleAction"`
	Egress        bool       `xml:"egress"`
	CidrBlock     string     `xml:"cidrBlock,omitempty"`
	Ipv6CidrBlock string     `xml:"ipv6CidrBlock,omitempty"`
	IcmpTypeCode  *IcmpCode  `xml:"icmpTypeCode,omitempty"`
	PortRange     *PortRange `xml:"portRange,omitempty"`
}

type IcmpCode struct {
	Code int `xml:"code"`
	Type int `xml:"type"`
}

type PortRange struct {
	From int `xml:"from"`
	To   int `xml:"to"`
}

type NaclAssoc struct {
	NetworkAclAssociationId string `xml:"networkAclAssociationId"`
	NetworkAclId            string `xml:"networkAclId"`
	SubnetId                string `xml:"subnetId"`
}

type NetworkInterface struct {
	NetworkInterfaceId string          `xml:"networkInterfaceId"`
	SubnetId           string          `xml:"subnetId"`
	VpcId              string          `xml:"vpcId"`
	AvailabilityZone   string          `xml:"availabilityZone"`
	AvailabilityZoneId string          `xml:"availabilityZoneId"`
	Description        string          `xml:"description"`
	OwnerId            string          `xml:"ownerId"`
	RequesterId        string          `xml:"requesterId,omitempty"`
	RequesterManaged   bool            `xml:"requesterManaged"`
	Status             string          `xml:"status"`
	MacAddress         string          `xml:"macAddress"`
	PrivateIpAddress   string          `xml:"privateIpAddress"`
	PrivateDnsName     string          `xml:"privateDnsName"`
	SourceDestCheck    bool            `xml:"sourceDestCheck"`
	InterfaceType      string          `xml:"interfaceType"`
	Groups             []GroupId       `xml:"groupSet>item"`
	Attachment         *EniAttachment  `xml:"attachment,omitempty"`
	Association        *EniAssociation `xml:"association,omitempty"`
	PrivateIpAddresses []EniPrivateIp  `xml:"privateIpAddressesSet>item"`
	Ipv6Addresses      []struct{}      `xml:"ipv6AddressesSet>item"`
	Tags               []Tag           `xml:"tagSet>item,omitempty"`
}

type GroupId struct {
	GroupId   string `xml:"groupId"`
	GroupName string `xml:"groupName"`
}

type EniAttachment struct {
	AttachmentId        string `xml:"attachmentId"`
	InstanceId          string `xml:"instanceId,omitempty"`
	InstanceOwnerId     string `xml:"instanceOwnerId"`
	DeviceIndex         int    `xml:"deviceIndex"`
	NetworkCardIndex    int    `xml:"networkCardIndex"`
	Status              string `xml:"status"`
	AttachTime          string `xml:"attachTime"`
	DeleteOnTermination bool   `xml:"deleteOnTermination"`
}

type EniAssociation struct {
	AllocationId  string `xml:"allocationId,omitempty"`
	AssociationId string `xml:"associationId,omitempty"`
	IpOwnerId     string `xml:"ipOwnerId"`
	PublicDnsName string `xml:"publicDnsName"`
	PublicIp      string `xml:"publicIp"`
}

type EniPrivateIp struct {
	PrivateIpAddress string          `xml:"privateIpAddress"`
	PrivateDnsName   string          `xml:"privateDnsName"`
	Primary          bool            `xml:"primary"`
	Association      *EniAssociation `xml:"association,omitempty"`
}

type KeyPair struct {
	KeyPairId      string `xml:"keyPairId"`
	KeyName        string `xml:"keyName"`
	KeyFingerprint string `xml:"keyFingerprint"`
	KeyType        string `xml:"keyType"`
	CreateTime     string `xml:"createTime"`
	PublicKey      string `xml:"publicKey,omitempty"`
	Tags           []Tag  `xml:"tagSet>item,omitempty"`
}

type Volume struct {
	VolumeId           string      `xml:"volumeId"`
	Size               int         `xml:"size"`
	SnapshotId         string      `xml:"snapshotId"`
	AvailabilityZone   string      `xml:"availabilityZone"`
	AvailabilityZoneId string      `xml:"availabilityZoneId"`
	Status             string      `xml:"status"`
	CreateTime         string      `xml:"createTime"`
	Attachments        []VolAttach `xml:"attachmentSet>item"`
	VolumeType         string      `xml:"volumeType"`
	Iops               *int        `xml:"iops,omitempty"`
	Throughput         *int        `xml:"throughput,omitempty"`
	Encrypted          bool        `xml:"encrypted"`
	KmsKeyId           string      `xml:"kmsKeyId,omitempty"`
	MultiAttachEnabled bool        `xml:"multiAttachEnabled"`
	Tags               []Tag       `xml:"tagSet>item,omitempty"`
}

type VolAttach struct {
	VolumeId            string `xml:"volumeId"`
	InstanceId          string `xml:"instanceId"`
	Device              string `xml:"device"`
	Status              string `xml:"status"`
	AttachTime          string `xml:"attachTime"`
	DeleteOnTermination bool   `xml:"deleteOnTermination"`
}

type VpcEndpoint struct {
	VpcEndpointId       string      `xml:"vpcEndpointId"`
	VpcEndpointType     string      `xml:"vpcEndpointType"`
	VpcId               string      `xml:"vpcId"`
	ServiceName         string      `xml:"serviceName"`
	State               string      `xml:"state"`
	PolicyDocument      string      `xml:"policyDocument"`
	RouteTableIds       []string    `xml:"routeTableIdSet>item"`
	SubnetIds           []string    `xml:"subnetIdSet>item"`
	Groups              []GroupId   `xml:"groupSet>item"`
	IpAddressType       string      `xml:"ipAddressType"`
	DnsOptions          *DnsOptions `xml:"dnsOptions,omitempty"`
	PrivateDnsEnabled   bool        `xml:"privateDnsEnabled"`
	RequesterManaged    bool        `xml:"requesterManaged"`
	NetworkInterfaceIds []string    `xml:"networkInterfaceIdSet>item"`
	CreationTimestamp   string      `xml:"creationTimestamp"`
	OwnerId             string      `xml:"ownerId"`
	Tags                []Tag       `xml:"tagSet>item,omitempty"`
}

type DnsOptions struct {
	DnsRecordIpType                          string `xml:"dnsRecordIpType"`
	PrivateDnsOnlyForInboundResolverEndpoint bool   `xml:"privateDnsOnlyForInboundResolverEndpoint"`
}
