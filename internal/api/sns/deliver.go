// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sns

import (
	"context"
	"time"
)

// MessageAttribute is an SNS message attribute. DataType is String,
// String.Array, Number or Binary.
type MessageAttribute struct {
	DataType    string
	StringValue string
	BinaryValue []byte
}

// Notification is one published message, as every subscriber sees it.
type Notification struct {
	MessageID string
	TopicArn  string
	Subject   string
	// Message is the text for this subscription's protocol (with
	// MessageStructure=json, the protocol's entry or "default").
	Message                string
	Timestamp              time.Time
	MessageAttributes      map[string]MessageAttribute
	MessageGroupID         string
	MessageDeduplicationID string
	SequenceNumber         string
}

// Delivery is a notification on its way to one subscription.
type Delivery struct {
	// Namespace is the tenant; the endpoint resolves inside it.
	Namespace       string
	AccountID       string
	SubscriptionArn string
	Protocol        string
	Endpoint        string
	// Raw is the subscription's RawMessageDelivery.
	Raw          bool
	Notification Notification
	// Envelope is the JSON notification document SNS sends when delivery
	// is not raw (SQS, HTTP).
	Envelope string
}

// Deliverer hands notifications to the endpoints of one protocol. The SQS
// handler is the "sqs" deliverer; a Lambda one registers as "lambda" once
// Lambda can invoke functions (it would build the Records/Sns event from
// Notification).
type Deliverer interface {
	Deliver(ctx context.Context, d *Delivery) error
}
