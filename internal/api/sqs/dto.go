// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import "encoding/xml"

// Generic AWS SQS ResponseMetadata block
type ResponseMetadata struct {
	XMLName   xml.Name `xml:"ResponseMetadata"`
	RequestId string   `xml:"RequestId"`
}

// ─────────────────────────────────────────────────────────────
// CreateQueue
// ─────────────────────────────────────────────────────────────

type CreateQueueResult struct {
	XMLName  xml.Name `xml:"CreateQueueResult"`
	QueueUrl string   `xml:"QueueUrl"`
}

type CreateQueueResponse struct {
	XMLName           xml.Name          `xml:"CreateQueueResponse"`
	CreateQueueResult CreateQueueResult `xml:"CreateQueueResult"`
	ResponseMetadata  ResponseMetadata  `xml:"ResponseMetadata"`
}

// ─────────────────────────────────────────────────────────────
// ListQueues
// ─────────────────────────────────────────────────────────────

type ListQueuesResult struct {
	XMLName   xml.Name `xml:"ListQueuesResult"`
	QueueUrls []string `xml:"QueueUrl"`
}

type ListQueuesResponse struct {
	XMLName          xml.Name         `xml:"ListQueuesResponse"`
	ListQueuesResult ListQueuesResult `xml:"ListQueuesResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

// ─────────────────────────────────────────────────────────────
// GetQueueUrl
// ─────────────────────────────────────────────────────────────

type GetQueueUrlResult struct {
	XMLName  xml.Name `xml:"GetQueueUrlResult"`
	QueueUrl string   `xml:"QueueUrl"`
}

type GetQueueUrlResponse struct {
	XMLName           xml.Name          `xml:"GetQueueUrlResponse"`
	GetQueueUrlResult GetQueueUrlResult `xml:"GetQueueUrlResult"`
	ResponseMetadata  ResponseMetadata  `xml:"ResponseMetadata"`
}

// ─────────────────────────────────────────────────────────────
// JSON API DTOs (for X-Amz-Target requests)
// ─────────────────────────────────────────────────────────────

// CreateQueue JSON API Request
type CreateQueueJSONRequest struct {
	QueueName  string            `json:"QueueName"`
	Attributes map[string]string `json:"Attributes,omitempty"`
}

// CreateQueue JSON API Response
type CreateQueueJSONResponse struct {
	QueueUrl string `json:"QueueUrl"`
}
