// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package route53

import "encoding/xml"

const xmlns = "https://route53.amazonaws.com/doc/2013-04-01/"

// Record sets. The same struct is the wire shape and the stored JSON.

type ResourceRecordSet struct {
	Name                 string                `xml:"Name" json:"name"`
	Type                 string                `xml:"Type" json:"type"`
	SetIdentifier        string                `xml:"SetIdentifier,omitempty" json:"set_identifier,omitempty"`
	Weight               *int64                `xml:"Weight,omitempty" json:"weight,omitempty"`
	Region               string                `xml:"Region,omitempty" json:"region,omitempty"`
	GeoLocation          *GeoLocation          `xml:"GeoLocation,omitempty" json:"geo_location,omitempty"`
	Failover             string                `xml:"Failover,omitempty" json:"failover,omitempty"`
	MultiValueAnswer     *bool                 `xml:"MultiValueAnswer,omitempty" json:"multi_value_answer,omitempty"`
	TTL                  *int64                `xml:"TTL,omitempty" json:"ttl,omitempty"`
	ResourceRecords      []ResourceRecord      `xml:"ResourceRecords>ResourceRecord,omitempty" json:"records,omitempty"`
	AliasTarget          *AliasTarget          `xml:"AliasTarget,omitempty" json:"alias_target,omitempty"`
	HealthCheckID        string                `xml:"HealthCheckId,omitempty" json:"health_check_id,omitempty"`
	CidrRoutingConfig    *CidrRoutingConfig    `xml:"CidrRoutingConfig,omitempty" json:"cidr_routing_config,omitempty"`
	GeoProximityLocation *GeoProximityLocation `xml:"GeoProximityLocation,omitempty" json:"geo_proximity_location,omitempty"`
}

type ResourceRecord struct {
	Value string `xml:"Value" json:"value"`
}

type AliasTarget struct {
	HostedZoneID         string `xml:"HostedZoneId" json:"hosted_zone_id"`
	DNSName              string `xml:"DNSName" json:"dns_name"`
	EvaluateTargetHealth bool   `xml:"EvaluateTargetHealth" json:"evaluate_target_health"`
}

type GeoLocation struct {
	ContinentCode   string `xml:"ContinentCode,omitempty" json:"continent_code,omitempty"`
	CountryCode     string `xml:"CountryCode,omitempty" json:"country_code,omitempty"`
	SubdivisionCode string `xml:"SubdivisionCode,omitempty" json:"subdivision_code,omitempty"`
}

type CidrRoutingConfig struct {
	CollectionID string `xml:"CollectionId" json:"collection_id"`
	LocationName string `xml:"LocationName" json:"location_name"`
}

type GeoProximityLocation struct {
	AWSRegion      string       `xml:"AWSRegion,omitempty" json:"aws_region,omitempty"`
	LocalZoneGroup string       `xml:"LocalZoneGroup,omitempty" json:"local_zone_group,omitempty"`
	Coordinates    *Coordinates `xml:"Coordinates,omitempty" json:"coordinates,omitempty"`
	Bias           *int64       `xml:"Bias,omitempty" json:"bias,omitempty"`
}

type Coordinates struct {
	Latitude  string `xml:"Latitude" json:"latitude"`
	Longitude string `xml:"Longitude" json:"longitude"`
}

type ChangeResourceRecordSetsRequest struct {
	XMLName     xml.Name `xml:"ChangeResourceRecordSetsRequest"`
	ChangeBatch struct {
		Comment string   `xml:"Comment"`
		Changes []Change `xml:"Changes>Change"`
	} `xml:"ChangeBatch"`
}

type Change struct {
	Action            string            `xml:"Action"`
	ResourceRecordSet ResourceRecordSet `xml:"ResourceRecordSet"`
}

type ChangeInfo struct {
	ID          string `xml:"Id"`
	Status      string `xml:"Status"`
	SubmittedAt string `xml:"SubmittedAt"`
	Comment     string `xml:"Comment,omitempty"`
}

type ChangeResourceRecordSetsResponse struct {
	XMLName    xml.Name   `xml:"ChangeResourceRecordSetsResponse"`
	Xmlns      string     `xml:"xmlns,attr"`
	ChangeInfo ChangeInfo `xml:"ChangeInfo"`
}

type ListResourceRecordSetsResponse struct {
	XMLName              xml.Name            `xml:"ListResourceRecordSetsResponse"`
	Xmlns                string              `xml:"xmlns,attr"`
	ResourceRecordSets   []ResourceRecordSet `xml:"ResourceRecordSets>ResourceRecordSet"`
	IsTruncated          bool                `xml:"IsTruncated"`
	NextRecordName       string              `xml:"NextRecordName,omitempty"`
	NextRecordType       string              `xml:"NextRecordType,omitempty"`
	NextRecordIdentifier string              `xml:"NextRecordIdentifier,omitempty"`
	MaxItems             int                 `xml:"MaxItems"`
}

type GetChangeResponse struct {
	XMLName    xml.Name   `xml:"GetChangeResponse"`
	Xmlns      string     `xml:"xmlns,attr"`
	ChangeInfo ChangeInfo `xml:"ChangeInfo"`
}

// Hosted zones.

type HostedZoneConfig struct {
	Comment     string `xml:"Comment,omitempty"`
	PrivateZone bool   `xml:"PrivateZone"`
}

type VPC struct {
	VPCRegion string `xml:"VPCRegion,omitempty" json:"vpc_region"`
	VPCId     string `xml:"VPCId" json:"vpc_id"`
}

type CreateHostedZoneRequest struct {
	XMLName          xml.Name          `xml:"CreateHostedZoneRequest"`
	Name             string            `xml:"Name"`
	CallerReference  string            `xml:"CallerReference"`
	HostedZoneConfig *HostedZoneConfig `xml:"HostedZoneConfig"`
	VPC              *VPC              `xml:"VPC"`
	DelegationSetID  string            `xml:"DelegationSetId"`
}

type UpdateHostedZoneCommentRequest struct {
	XMLName xml.Name `xml:"UpdateHostedZoneCommentRequest"`
	Comment *string  `xml:"Comment"`
}

type VPCAssociationRequest struct {
	VPC     VPC    `xml:"VPC"`
	Comment string `xml:"Comment"`
}

type HostedZone struct {
	ID                     string           `xml:"Id"`
	Name                   string           `xml:"Name"`
	CallerReference        string           `xml:"CallerReference"`
	Config                 HostedZoneConfig `xml:"Config"`
	ResourceRecordSetCount int              `xml:"ResourceRecordSetCount"`
}

type DelegationSet struct {
	ID              string   `xml:"Id,omitempty"`
	CallerReference string   `xml:"CallerReference,omitempty"`
	NameServers     []string `xml:"NameServers>NameServer"`
}

type CreateHostedZoneResponse struct {
	XMLName       xml.Name      `xml:"CreateHostedZoneResponse"`
	Xmlns         string        `xml:"xmlns,attr"`
	HostedZone    HostedZone    `xml:"HostedZone"`
	ChangeInfo    ChangeInfo    `xml:"ChangeInfo"`
	DelegationSet DelegationSet `xml:"DelegationSet"`
	VPC           *VPC          `xml:"VPC,omitempty"`
}

type GetHostedZoneResponse struct {
	XMLName       xml.Name       `xml:"GetHostedZoneResponse"`
	Xmlns         string         `xml:"xmlns,attr"`
	HostedZone    HostedZone     `xml:"HostedZone"`
	DelegationSet *DelegationSet `xml:"DelegationSet,omitempty"`
	VPCs          []VPC          `xml:"VPCs>VPC,omitempty"`
}

type UpdateHostedZoneCommentResponse struct {
	XMLName    xml.Name   `xml:"UpdateHostedZoneCommentResponse"`
	Xmlns      string     `xml:"xmlns,attr"`
	HostedZone HostedZone `xml:"HostedZone"`
}

type ListHostedZonesResponse struct {
	XMLName     xml.Name     `xml:"ListHostedZonesResponse"`
	Xmlns       string       `xml:"xmlns,attr"`
	HostedZones []HostedZone `xml:"HostedZones>HostedZone"`
	Marker      string       `xml:"Marker"`
	IsTruncated bool         `xml:"IsTruncated"`
	NextMarker  string       `xml:"NextMarker,omitempty"`
	MaxItems    int          `xml:"MaxItems"`
}

type ListHostedZonesByNameResponse struct {
	XMLName          xml.Name     `xml:"ListHostedZonesByNameResponse"`
	Xmlns            string       `xml:"xmlns,attr"`
	HostedZones      []HostedZone `xml:"HostedZones>HostedZone"`
	DNSName          string       `xml:"DNSName,omitempty"`
	HostedZoneID     string       `xml:"HostedZoneId,omitempty"`
	IsTruncated      bool         `xml:"IsTruncated"`
	NextDNSName      string       `xml:"NextDNSName,omitempty"`
	NextHostedZoneID string       `xml:"NextHostedZoneId,omitempty"`
	MaxItems         int          `xml:"MaxItems"`
}

type GetHostedZoneCountResponse struct {
	XMLName         xml.Name `xml:"GetHostedZoneCountResponse"`
	Xmlns           string   `xml:"xmlns,attr"`
	HostedZoneCount int      `xml:"HostedZoneCount"`
}

type ChangeInfoResponse struct {
	XMLName    xml.Name
	Xmlns      string     `xml:"xmlns,attr"`
	ChangeInfo ChangeInfo `xml:"ChangeInfo"`
}

type GetDNSSECResponse struct {
	XMLName xml.Name `xml:"GetDNSSECResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Status  struct {
		ServeSignature string `xml:"ServeSignature"`
	} `xml:"Status"`
	KeySigningKeys struct{} `xml:"KeySigningKeys"`
}

// Reusable delegation sets.

type CreateReusableDelegationSetRequest struct {
	XMLName         xml.Name `xml:"CreateReusableDelegationSetRequest"`
	CallerReference string   `xml:"CallerReference"`
	HostedZoneID    string   `xml:"HostedZoneId"`
}

type DelegationSetResponse struct {
	XMLName       xml.Name
	Xmlns         string        `xml:"xmlns,attr"`
	DelegationSet DelegationSet `xml:"DelegationSet"`
}

type ListReusableDelegationSetsResponse struct {
	XMLName        xml.Name        `xml:"ListReusableDelegationSetsResponse"`
	Xmlns          string          `xml:"xmlns,attr"`
	DelegationSets []DelegationSet `xml:"DelegationSets>DelegationSet"`
	Marker         string          `xml:"Marker"`
	IsTruncated    bool            `xml:"IsTruncated"`
	MaxItems       int             `xml:"MaxItems"`
}

type EmptyResponse struct {
	XMLName xml.Name
	Xmlns   string `xml:"xmlns,attr"`
}

// Tags.

type Tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type ChangeTagsForResourceRequest struct {
	XMLName       xml.Name `xml:"ChangeTagsForResourceRequest"`
	AddTags       []Tag    `xml:"AddTags>Tag"`
	RemoveTagKeys []string `xml:"RemoveTagKeys>Key"`
}

type ResourceTagSet struct {
	ResourceType string `xml:"ResourceType"`
	ResourceID   string `xml:"ResourceId"`
	Tags         []Tag  `xml:"Tags>Tag"`
}

type ListTagsForResourceResponse struct {
	XMLName        xml.Name       `xml:"ListTagsForResourceResponse"`
	Xmlns          string         `xml:"xmlns,attr"`
	ResourceTagSet ResourceTagSet `xml:"ResourceTagSet"`
}

type ListTagsForResourcesRequest struct {
	XMLName     xml.Name `xml:"ListTagsForResourcesRequest"`
	ResourceIDs []string `xml:"ResourceIds>ResourceId"`
}

type ListTagsForResourcesResponse struct {
	XMLName         xml.Name         `xml:"ListTagsForResourcesResponse"`
	Xmlns           string           `xml:"xmlns,attr"`
	ResourceTagSets []ResourceTagSet `xml:"ResourceTagSets>ResourceTagSet"`
}

// Health checks. HealthCheckConfig is also the stored JSON.

type AlarmIdentifier struct {
	Region string `xml:"Region" json:"region"`
	Name   string `xml:"Name" json:"name"`
}

type HealthCheckConfig struct {
	IPAddress                    string           `xml:"IPAddress,omitempty" json:"ip_address,omitempty"`
	Port                         *int64           `xml:"Port,omitempty" json:"port,omitempty"`
	Type                         string           `xml:"Type" json:"type"`
	ResourcePath                 string           `xml:"ResourcePath,omitempty" json:"resource_path,omitempty"`
	FullyQualifiedDomainName     string           `xml:"FullyQualifiedDomainName,omitempty" json:"fqdn,omitempty"`
	SearchString                 string           `xml:"SearchString,omitempty" json:"search_string,omitempty"`
	RequestInterval              *int64           `xml:"RequestInterval,omitempty" json:"request_interval,omitempty"`
	FailureThreshold             *int64           `xml:"FailureThreshold,omitempty" json:"failure_threshold,omitempty"`
	MeasureLatency               *bool            `xml:"MeasureLatency,omitempty" json:"measure_latency,omitempty"`
	Inverted                     *bool            `xml:"Inverted,omitempty" json:"inverted,omitempty"`
	Disabled                     *bool            `xml:"Disabled,omitempty" json:"disabled,omitempty"`
	HealthThreshold              *int64           `xml:"HealthThreshold,omitempty" json:"health_threshold,omitempty"`
	ChildHealthChecks            []string         `xml:"ChildHealthChecks>ChildHealthCheck,omitempty" json:"child_health_checks,omitempty"`
	EnableSNI                    *bool            `xml:"EnableSNI,omitempty" json:"enable_sni,omitempty"`
	Regions                      []string         `xml:"Regions>Region,omitempty" json:"regions,omitempty"`
	AlarmIdentifier              *AlarmIdentifier `xml:"AlarmIdentifier,omitempty" json:"alarm_identifier,omitempty"`
	InsufficientDataHealthStatus string           `xml:"InsufficientDataHealthStatus,omitempty" json:"insufficient_data_health_status,omitempty"`
	RoutingControlArn            string           `xml:"RoutingControlArn,omitempty" json:"routing_control_arn,omitempty"`
}

type CreateHealthCheckRequest struct {
	XMLName           xml.Name          `xml:"CreateHealthCheckRequest"`
	CallerReference   string            `xml:"CallerReference"`
	HealthCheckConfig HealthCheckConfig `xml:"HealthCheckConfig"`
}

// UpdateHealthCheckRequest uses pointers so an absent element leaves the
// stored value alone.
type UpdateHealthCheckRequest struct {
	XMLName                      xml.Name         `xml:"UpdateHealthCheckRequest"`
	HealthCheckVersion           *int64           `xml:"HealthCheckVersion"`
	IPAddress                    *string          `xml:"IPAddress"`
	Port                         *int64           `xml:"Port"`
	ResourcePath                 *string          `xml:"ResourcePath"`
	FullyQualifiedDomainName     *string          `xml:"FullyQualifiedDomainName"`
	SearchString                 *string          `xml:"SearchString"`
	FailureThreshold             *int64           `xml:"FailureThreshold"`
	Inverted                     *bool            `xml:"Inverted"`
	Disabled                     *bool            `xml:"Disabled"`
	HealthThreshold              *int64           `xml:"HealthThreshold"`
	ChildHealthChecks            []string         `xml:"ChildHealthChecks>ChildHealthCheck"`
	EnableSNI                    *bool            `xml:"EnableSNI"`
	Regions                      []string         `xml:"Regions>Region"`
	AlarmIdentifier              *AlarmIdentifier `xml:"AlarmIdentifier"`
	InsufficientDataHealthStatus *string          `xml:"InsufficientDataHealthStatus"`
	ResetElements                []string         `xml:"ResetElements>ResettableElementName"`
}

type HealthCheck struct {
	ID                 string            `xml:"Id"`
	CallerReference    string            `xml:"CallerReference"`
	HealthCheckConfig  HealthCheckConfig `xml:"HealthCheckConfig"`
	HealthCheckVersion int64             `xml:"HealthCheckVersion"`
}

type HealthCheckResponse struct {
	XMLName     xml.Name
	Xmlns       string      `xml:"xmlns,attr"`
	HealthCheck HealthCheck `xml:"HealthCheck"`
}

type ListHealthChecksResponse struct {
	XMLName      xml.Name      `xml:"ListHealthChecksResponse"`
	Xmlns        string        `xml:"xmlns,attr"`
	HealthChecks []HealthCheck `xml:"HealthChecks>HealthCheck"`
	Marker       string        `xml:"Marker"`
	IsTruncated  bool          `xml:"IsTruncated"`
	NextMarker   string        `xml:"NextMarker,omitempty"`
	MaxItems     int           `xml:"MaxItems"`
}

type GetHealthCheckCountResponse struct {
	XMLName          xml.Name `xml:"GetHealthCheckCountResponse"`
	Xmlns            string   `xml:"xmlns,attr"`
	HealthCheckCount int      `xml:"HealthCheckCount"`
}

// Errors.

type errorResponse struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Error   struct {
		Type    string `xml:"Type"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
	RequestID string `xml:"RequestId"`
}

type invalidChangeBatchResponse struct {
	XMLName   xml.Name `xml:"InvalidChangeBatch"`
	Xmlns     string   `xml:"xmlns,attr"`
	Messages  []string `xml:"Messages>Message"`
	RequestID string   `xml:"RequestId"`
}
