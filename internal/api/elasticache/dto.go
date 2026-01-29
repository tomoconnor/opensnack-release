// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package elasticache

import (
	"encoding/xml"
	"time"
)

const (
	APIVersion         = "2015-02-02"
	elasticacheRegion  = "us-east-1"
	elasticacheAccount = "000000000000"
)

// CacheCluster represents an ElastiCache cache cluster
type CacheCluster struct {
	CacheClusterId             string                        `xml:"CacheClusterId"`
	ConfigurationEndpoint      *Endpoint                     `xml:"ConfigurationEndpoint,omitempty"`
	ClientDownloadLandingPage  string                        `xml:"ClientDownloadLandingPage"`
	CacheNodeType              string                        `xml:"CacheNodeType"`
	Engine                     string                        `xml:"Engine"`
	EngineVersion              string                        `xml:"EngineVersion"`
	CacheClusterStatus         string                        `xml:"CacheClusterStatus"`
	NumCacheNodes              int                           `xml:"NumCacheNodes"`
	PreferredAvailabilityZone  string                        `xml:"PreferredAvailabilityZone"`
	CacheClusterCreateTime     time.Time                     `xml:"CacheClusterCreateTime"`
	PreferredMaintenanceWindow string                        `xml:"PreferredMaintenanceWindow"`
	PendingModifiedValues      *PendingModifiedValues        `xml:"PendingModifiedValues,omitempty"`
	NotificationConfiguration  *NotificationConfiguration    `xml:"NotificationConfiguration,omitempty"`
	CacheSecurityGroups        CacheSecurityGroupMemberships `xml:"CacheSecurityGroups"`
	CacheParameterGroup        CacheParameterGroupStatus     `xml:"CacheParameterGroup"`
	CacheSubnetGroupName       string                        `xml:"CacheSubnetGroupName"`
	CacheNodes                 CacheNodeList                 `xml:"CacheNodes"`
	AutoMinorVersionUpgrade    bool                          `xml:"AutoMinorVersionUpgrade"`
	SecurityGroups             SecurityGroupMemberships      `xml:"SecurityGroups"`
	ReplicationGroupId         string                        `xml:"ReplicationGroupId,omitempty"`
	SnapshotRetentionLimit     int                           `xml:"SnapshotRetentionLimit"`
	SnapshotWindow             string                        `xml:"SnapshotWindow"`
	AuthTokenEnabled           bool                          `xml:"AuthTokenEnabled"`
	AuthTokenLastModifiedDate  time.Time                     `xml:"AuthTokenLastModifiedDate,omitempty"`
	TransitEncryptionEnabled   bool                          `xml:"TransitEncryptionEnabled"`
	AtRestEncryptionEnabled    bool                          `xml:"AtRestEncryptionEnabled"`
	ARN                        string                        `xml:"ARN"`
}

// Endpoint represents a cache cluster endpoint
type Endpoint struct {
	Address string `xml:"Address"`
	Port    int    `xml:"Port"`
}

// PendingModifiedValues represents pending modifications
type PendingModifiedValues struct {
	NumCacheNodes        int      `xml:"NumCacheNodes,omitempty"`
	CacheNodeIdsToRemove []string `xml:"CacheNodeIdsToRemove,omitempty"`
	EngineVersion        string   `xml:"EngineVersion,omitempty"`
	CacheNodeType        string   `xml:"CacheNodeType,omitempty"`
}

// NotificationConfiguration represents notification settings
type NotificationConfiguration struct {
	TopicArn    string `xml:"TopicArn"`
	TopicStatus string `xml:"TopicStatus"`
}

// CacheSecurityGroupMemberships represents cache security group memberships
type CacheSecurityGroupMemberships struct {
	CacheSecurityGroup []CacheSecurityGroup `xml:"CacheSecurityGroup"`
}

// CacheSecurityGroup represents a cache security group
type CacheSecurityGroup struct {
	CacheSecurityGroupName string `xml:"CacheSecurityGroupName"`
	Status                 string `xml:"Status"`
}

// CacheParameterGroupStatus represents parameter group status
type CacheParameterGroupStatus struct {
	CacheParameterGroupName string   `xml:"CacheParameterGroupName"`
	ParameterApplyStatus    string   `xml:"ParameterApplyStatus"`
	CacheNodeIdsToReboot    []string `xml:"CacheNodeIdsToReboot,omitempty"`
}

// CacheNodeList represents a list of cache nodes
type CacheNodeList struct {
	CacheNode []CacheNode `xml:"CacheNode"`
}

// CacheNode represents a cache node
type CacheNode struct {
	CacheNodeId              string    `xml:"CacheNodeId"`
	CacheNodeStatus          string    `xml:"CacheNodeStatus"`
	CacheNodeCreateTime      time.Time `xml:"CacheNodeCreateTime"`
	Endpoint                 *Endpoint `xml:"Endpoint,omitempty"`
	ParameterGroupStatus     string    `xml:"ParameterGroupStatus"`
	SourceCacheNodeId        string    `xml:"SourceCacheNodeId,omitempty"`
	CustomerAvailabilityZone string    `xml:"CustomerAvailabilityZone"`
}

// SecurityGroupMemberships represents security group memberships
type SecurityGroupMemberships struct {
	Member []SecurityGroupMember `xml:"Member"`
}

// SecurityGroupMember represents a security group member
type SecurityGroupMember struct {
	SecurityGroupId string `xml:"SecurityGroupId"`
	Status          string `xml:"Status"`
}

// CreateCacheClusterResponse represents the response for CreateCacheCluster
type CreateCacheClusterResponse struct {
	XMLName                  xml.Name                 `xml:"CreateCacheClusterResponse"`
	Xmlns                    string                   `xml:"xmlns,attr"`
	CreateCacheClusterResult CreateCacheClusterResult `xml:"CreateCacheClusterResult"`
	ResponseMetadata         ResponseMetadata         `xml:"ResponseMetadata"`
}

// CreateCacheClusterResult contains the cache cluster
type CreateCacheClusterResult struct {
	CacheCluster CacheCluster `xml:"CacheCluster"`
}

// DescribeCacheClustersResponse represents the response for DescribeCacheClusters
type DescribeCacheClustersResponse struct {
	XMLName                     xml.Name                    `xml:"DescribeCacheClustersResponse"`
	Xmlns                       string                      `xml:"xmlns,attr"`
	DescribeCacheClustersResult DescribeCacheClustersResult `xml:"DescribeCacheClustersResult"`
	ResponseMetadata            ResponseMetadata            `xml:"ResponseMetadata"`
}

// DescribeCacheClustersResult contains the cache clusters
type DescribeCacheClustersResult struct {
	CacheClusters CacheClusterList `xml:"CacheClusters"`
	Marker        string           `xml:"Marker,omitempty"`
}

// CacheClusterList represents a list of cache clusters
type CacheClusterList struct {
	CacheCluster []CacheCluster `xml:"CacheCluster"`
}

// DeleteCacheClusterResponse represents the response for DeleteCacheCluster
type DeleteCacheClusterResponse struct {
	XMLName                  xml.Name                 `xml:"DeleteCacheClusterResponse"`
	Xmlns                    string                   `xml:"xmlns,attr"`
	DeleteCacheClusterResult DeleteCacheClusterResult `xml:"DeleteCacheClusterResult"`
	ResponseMetadata         ResponseMetadata         `xml:"ResponseMetadata"`
}

// DeleteCacheClusterResult contains the cache cluster
type DeleteCacheClusterResult struct {
	CacheCluster CacheCluster `xml:"CacheCluster"`
}

// ResponseMetadata contains request metadata
type ResponseMetadata struct {
	RequestId string `xml:"RequestId"`
}

// ListTagsForResource structures
type Tag struct {
	XMLName xml.Name `xml:"Tag"`
	Key     string   `xml:"Key"`
	Value   string   `xml:"Value"`
}

type TagList struct {
	XMLName xml.Name `xml:"TagList"`
	Tags    []Tag    `xml:"Tag"`
}

type ListTagsForResourceResult struct {
	XMLName xml.Name `xml:"ListTagsForResourceResult"`
	TagList TagList  `xml:"TagList"`
}

type ListTagsForResourceResponse struct {
	XMLName                   xml.Name                  `xml:"ListTagsForResourceResponse"`
	ListTagsForResourceResult ListTagsForResourceResult `xml:"ListTagsForResourceResult"`
	ResponseMetadata          ResponseMetadata          `xml:"ResponseMetadata"`
}
