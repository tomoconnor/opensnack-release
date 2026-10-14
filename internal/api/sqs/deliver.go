// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"context"
	"fmt"

	"opensnack/internal/api/sns"
	"opensnack/internal/util"
)

// snsSenderID is the SenderId SQS reports for messages SNS delivered.
const snsSenderID = "AIDAIT2UOQQY3AUEKVGXU"

// Deliver makes the SQS handler the SNS "sqs" deliverer: the notification
// lands in the queue the subscription endpoint names, in the publisher's
// tenant and the region of the queue ARN.
func (h *Handler) Deliver(_ context.Context, d *sns.Delivery) error {
	region, _, name, ok := parseQueueArn(d.Endpoint)
	if !ok {
		return fmt.Errorf("not an SQS queue ARN: %s", d.Endpoint)
	}
	scope := util.RegionalScope(d.Namespace, region)
	res, err := h.Store.Get(name, "sqs", "queue", scope)
	if err != nil || res == nil {
		return fmt.Errorf("queue %s does not exist", d.Endpoint)
	}
	rec := decodeQueue(res)
	cfg := h.configFor(d.Namespace, scope, d.Endpoint, rec, true)

	in := &sendMessageInput{MessageBody: d.Envelope}
	if d.Raw {
		in.MessageBody = d.Notification.Message
		for k, v := range d.Notification.MessageAttributes {
			if in.MessageAttributes == nil {
				in.MessageAttributes = map[string]MessageAttributeValue{}
			}
			in.MessageAttributes[k] = MessageAttributeValue{DataType: v.DataType, StringValue: v.StringValue, BinaryValue: v.BinaryValue}
		}
	}
	if cfg.fifo {
		in.MessageGroupId = d.Notification.MessageGroupID
		in.MessageDeduplicationId = d.Notification.MessageDeduplicationID
	}
	if _, aerr := h.send(cfg, rec, snsSenderID, in); aerr != nil {
		return aerr
	}
	return nil
}
