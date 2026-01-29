// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sns

import "encoding/xml"

// Common AWS ResponseMetadata
type ResponseMetadata struct {
	XMLName   xml.Name `xml:"ResponseMetadata"`
	RequestId string   `xml:"RequestId"`
}

// CreateTopic
type CreateTopicResult struct {
	XMLName  xml.Name `xml:"CreateTopicResult"`
	TopicArn string   `xml:"TopicArn"`
}

type CreateTopicResponse struct {
	XMLName           xml.Name          `xml:"CreateTopicResponse"`
	CreateTopicResult CreateTopicResult `xml:"CreateTopicResult"`
	ResponseMetadata  ResponseMetadata  `xml:"ResponseMetadata"`
}

// ListTopics
type TopicArnMember struct {
	XMLName  xml.Name `xml:"member"`
	TopicArn string   `xml:"TopicArn"`
}

type ListTopicsResult struct {
	XMLName xml.Name         `xml:"ListTopicsResult"`
	Topics  []TopicArnMember `xml:"Topics>member"`
}

type ListTopicsResponse struct {
	XMLName          xml.Name         `xml:"ListTopicsResponse"`
	ListTopicsResult ListTopicsResult `xml:"ListTopicsResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// DeleteTopic
type DeleteTopicResponse struct {
	XMLName          xml.Name         `xml:"DeleteTopicResponse"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// Publish
type PublishResult struct {
	XMLName   xml.Name `xml:"PublishResult"`
	MessageId string   `xml:"MessageId"`
}

type PublishResponse struct {
	XMLName          xml.Name         `xml:"PublishResponse"`
	PublishResult    PublishResult    `xml:"PublishResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// GetTopicAttributes
type AttributeEntry struct {
	XMLName xml.Name `xml:"entry"`
	Key     string   `xml:"key"`
	Value   string   `xml:"value"`
}

type Attributes struct {
	XMLName xml.Name         `xml:"Attributes"`
	Entries []AttributeEntry `xml:"entry"`
}

type GetTopicAttributesResult struct {
	XMLName    xml.Name   `xml:"GetTopicAttributesResult"`
	Attributes Attributes `xml:"Attributes"`
}

type GetTopicAttributesResponse struct {
	XMLName                  xml.Name                 `xml:"GetTopicAttributesResponse"`
	GetTopicAttributesResult GetTopicAttributesResult `xml:"GetTopicAttributesResult"`
	ResponseMetadata         ResponseMetadata         `xml:"ResponseMetadata"`
}

// SetTopicAttributes
type SetTopicAttributesResponse struct {
	XMLName          xml.Name         `xml:"SetTopicAttributesResponse"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// ListTagsForResource
type Tag struct {
	XMLName xml.Name `xml:"member"`
	Key     string   `xml:"Key"`
	Value   string   `xml:"Value"`
}

type Tags struct {
	XMLName xml.Name `xml:"Tags"`
	Tags    []Tag    `xml:"member"`
}

type ListTagsForResourceResult struct {
	XMLName xml.Name `xml:"ListTagsForResourceResult"`
	Tags    Tags     `xml:"Tags"`
}

type ListTagsForResourceResponse struct {
	XMLName                   xml.Name                  `xml:"ListTagsForResourceResponse"`
	ListTagsForResourceResult ListTagsForResourceResult `xml:"ListTagsForResourceResult"`
	ResponseMetadata          ResponseMetadata          `xml:"ResponseMetadata"`
}

// Subscribe
type SubscribeResult struct {
	XMLName         xml.Name `xml:"SubscribeResult"`
	SubscriptionArn string   `xml:"SubscriptionArn"`
}

type SubscribeResponse struct {
	XMLName          xml.Name         `xml:"SubscribeResponse"`
	SubscribeResult  SubscribeResult  `xml:"SubscribeResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// GetSubscriptionAttributes
type GetSubscriptionAttributesResult struct {
	XMLName    xml.Name   `xml:"GetSubscriptionAttributesResult"`
	Attributes Attributes `xml:"Attributes"`
}

type GetSubscriptionAttributesResponse struct {
	XMLName                         xml.Name                        `xml:"GetSubscriptionAttributesResponse"`
	GetSubscriptionAttributesResult GetSubscriptionAttributesResult `xml:"GetSubscriptionAttributesResult"`
	ResponseMetadata                ResponseMetadata                `xml:"ResponseMetadata"`
}

// ListSubscriptionsByTopic
type Subscription struct {
	XMLName         xml.Name `xml:"member"`
	SubscriptionArn string   `xml:"SubscriptionArn"`
	TopicArn        string   `xml:"TopicArn"`
	Protocol        string   `xml:"Protocol"`
	Endpoint        string   `xml:"Endpoint"`
	Owner           string   `xml:"Owner"`
}

type Subscriptions struct {
	XMLName       xml.Name       `xml:"Subscriptions"`
	Subscriptions []Subscription `xml:"member"`
}

type ListSubscriptionsByTopicResult struct {
	XMLName       xml.Name      `xml:"ListSubscriptionsByTopicResult"`
	Subscriptions Subscriptions `xml:"Subscriptions"`
}

type ListSubscriptionsByTopicResponse struct {
	XMLName                        xml.Name                       `xml:"ListSubscriptionsByTopicResponse"`
	ListSubscriptionsByTopicResult ListSubscriptionsByTopicResult `xml:"ListSubscriptionsByTopicResult"`
	ResponseMetadata               ResponseMetadata               `xml:"ResponseMetadata"`
}

// Unsubscribe
type UnsubscribeResponse struct {
	XMLName          xml.Name         `xml:"UnsubscribeResponse"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}
