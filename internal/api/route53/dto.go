// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package route53

import "encoding/xml"

// CreateHostedZoneInput represents the request to create a hosted zone
type CreateHostedZoneInput struct {
	XMLName          xml.Name          `xml:"CreateHostedZoneRequest"`
	Name             string            `xml:"Name"`
	CallerReference  string            `xml:"CallerReference"`
	HostedZoneConfig *HostedZoneConfig `xml:"HostedZoneConfig,omitempty"`
	VPC              *VPC              `xml:"VPC,omitempty"`
}

// HostedZoneConfig represents hosted zone configuration
type HostedZoneConfig struct {
	Comment     string `xml:"Comment,omitempty"`
	PrivateZone bool   `xml:"PrivateZone,omitempty"`
}

// VPC represents VPC configuration
type VPC struct {
	VPCRegion string `xml:"VPCRegion"`
	VPCId     string `xml:"VPCId"`
}

// CreateHostedZoneOutput represents the response from creating a hosted zone
type CreateHostedZoneOutput struct {
	XMLName          xml.Name         `xml:"https://route53.amazonaws.com/doc/2013-04-01/ CreateHostedZoneResponse"`
	HostedZone       HostedZone       `xml:"HostedZone"`
	ChangeInfo       ChangeInfo       `xml:"ChangeInfo"`
	DelegationSet    DelegationSet    `xml:"DelegationSet"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// HostedZone represents a Route53 hosted zone
type HostedZone struct {
	ID                     string            `xml:"Id"`
	Name                   string            `xml:"Name"`
	CallerReference        string            `xml:"CallerReference"`
	Config                 *HostedZoneConfig `xml:"Config"`
	ResourceRecordSetCount int64             `xml:"ResourceRecordSetCount"`
}

// ChangeInfo represents change information
type ChangeInfo struct {
	ID          string `xml:"Id"`
	Status      string `xml:"Status"`
	SubmittedAt string `xml:"SubmittedAt"`
}

// DelegationSet represents name servers
type DelegationSet struct {
	ID          string      `xml:"Id,omitempty"`
	NameServers NameServers `xml:"NameServers"`
}

// NameServers represents a list of name servers
type NameServers struct {
	NameServer []string `xml:"NameServer"`
}

// GetHostedZoneInput represents the request to get a hosted zone
type GetHostedZoneInput struct {
	ID string
}

// GetHostedZoneOutput represents the response from getting a hosted zone
type GetHostedZoneOutput struct {
	XMLName          xml.Name         `xml:"https://route53.amazonaws.com/doc/2013-04-01/ GetHostedZoneResponse"`
	HostedZone       HostedZone       `xml:"HostedZone"`
	DelegationSet    DelegationSet    `xml:"DelegationSet"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// GetChangeOutput represents the response from getting a change
type GetChangeOutput struct {
	XMLName          xml.Name         `xml:"GetChangeResponse"`
	ChangeInfo       ChangeInfo       `xml:"ChangeInfo"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// ListHostedZonesInput represents the request to list hosted zones
type ListHostedZonesInput struct {
	MaxItems string
	Marker   string
}

// ListHostedZonesOutput represents the response from listing hosted zones
type ListHostedZonesOutput struct {
	XMLName               xml.Name              `xml:"ListHostedZonesResponse"`
	ListHostedZonesResult ListHostedZonesResult `xml:"ListHostedZonesResult"`
	ResponseMetadata      ResponseMetadata      `xml:"ResponseMetadata"`
}

// ListHostedZonesResult represents the result of listing hosted zones
type ListHostedZonesResult struct {
	HostedZones HostedZones `xml:"HostedZones"`
	IsTruncated bool        `xml:"IsTruncated"`
	MaxItems    string      `xml:"MaxItems"`
	Marker      string      `xml:"Marker,omitempty"`
	NextMarker  string      `xml:"NextMarker,omitempty"`
}

// HostedZones represents a list of hosted zones
type HostedZones struct {
	HostedZone []HostedZone `xml:"HostedZone"`
}

// ChangeResourceRecordSetsInput represents the request to change resource record sets
type ChangeResourceRecordSetsInput struct {
	XMLName      xml.Name    `xml:"ChangeResourceRecordSetsRequest"`
	HostedZoneID string      `xml:"HostedZoneId"`
	ChangeBatch  ChangeBatch `xml:"ChangeBatch"`
}

// ChangeBatch represents a batch of changes
type ChangeBatch struct {
	Changes Changes `xml:"Changes"`
	Comment string  `xml:"Comment,omitempty"`
}

// Changes represents a list of changes
type Changes struct {
	Change []Change `xml:"Change"`
}

// Change represents a single change
type Change struct {
	Action            string            `xml:"Action"`
	ResourceRecordSet ResourceRecordSet `xml:"ResourceRecordSet"`
}

// ResourceRecordSet represents a DNS record set
type ResourceRecordSet struct {
	Name            string          `xml:"Name"`
	Type            string          `xml:"Type"`
	TTL             int64           `xml:"TTL"`
	ResourceRecords ResourceRecords `xml:"ResourceRecords"`
}

// ResourceRecords represents a list of resource records
type ResourceRecords struct {
	ResourceRecord []ResourceRecord `xml:"ResourceRecord"`
}

// ResourceRecord represents a single DNS record value
type ResourceRecord struct {
	Value string `xml:"Value"`
}

// ChangeResourceRecordSetsOutput represents the response from changing resource record sets
type ChangeResourceRecordSetsOutput struct {
	XMLName                        xml.Name                       `xml:"ChangeResourceRecordSetsResponse"`
	ChangeResourceRecordSetsResult ChangeResourceRecordSetsResult `xml:"ChangeResourceRecordSetsResult"`
	ResponseMetadata               ResponseMetadata               `xml:"ResponseMetadata"`
}

// ChangeResourceRecordSetsResult represents the result of changing resource record sets
type ChangeResourceRecordSetsResult struct {
	ChangeInfo ChangeInfo `xml:"ChangeInfo"`
}

// ListResourceRecordSetsInput represents the request to list resource record sets
type ListResourceRecordSetsInput struct {
	HostedZoneID    string
	StartRecordName string
	StartRecordType string
	MaxItems        string
}

// ListResourceRecordSetsOutput represents the response from listing resource record sets
type ListResourceRecordSetsOutput struct {
	XMLName            xml.Name           `xml:"https://route53.amazonaws.com/doc/2013-04-01/ ListResourceRecordSetsResponse"`
	ResourceRecordSets ResourceRecordSets `xml:"ResourceRecordSets"`
	IsTruncated        bool               `xml:"IsTruncated"`
	MaxItems           string             `xml:"MaxItems"`
	NextRecordName     string             `xml:"NextRecordName,omitempty"`
	NextRecordType     string             `xml:"NextRecordType,omitempty"`
	ResponseMetadata   ResponseMetadata   `xml:"ResponseMetadata"`
}

// ResourceRecordSets represents a list of resource record sets
type ResourceRecordSets struct {
	ResourceRecordSet []ResourceRecordSet `xml:"ResourceRecordSet"`
}

// ResponseMetadata represents response metadata
type ResponseMetadata struct {
	RequestID string `xml:"RequestId"`
}

// ListTagsForResourceOutput represents the response from listing tags
type ListTagsForResourceOutput struct {
	XMLName          xml.Name         `xml:"https://route53.amazonaws.com/doc/2013-04-01/ ListTagsForResourceResponse"`
	ResourceTagSet   ResourceTagSet   `xml:"ResourceTagSet"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// ResourceTagSet represents a set of tags for a resource
type ResourceTagSet struct {
	ResourceType string `xml:"ResourceType"`
	ResourceID   string `xml:"ResourceId"`
	Tags         Tags   `xml:"Tags"`
}

// Tags represents a list of tags
type Tags struct {
	Tag []Tag `xml:"Tag"`
}

// Tag represents a single tag
type Tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

// DeleteHostedZoneOutput represents the response from deleting a hosted zone
type DeleteHostedZoneOutput struct {
	XMLName          xml.Name         `xml:"https://route53.amazonaws.com/doc/2013-04-01/ DeleteHostedZoneResponse"`
	ChangeInfo       ChangeInfo       `xml:"ChangeInfo"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}
