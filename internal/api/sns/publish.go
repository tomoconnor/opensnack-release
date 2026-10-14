// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sns

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"opensnack/internal/util"

	"github.com/google/uuid"
)

type publishInput struct {
	TopicArn               string
	Message                string
	Subject                string
	MessageStructure       string
	MessageAttributes      map[string]MessageAttribute
	MessageGroupID         string
	MessageDeduplicationID string
}

type publishOutput struct {
	MessageID      string
	SequenceNumber string
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request, f form) *apiError {
	arn := f.get("TopicArn")
	if arn == "" {
		arn = f.get("TargetArn")
	}
	if arn == "" {
		if f.get("PhoneNumber") != "" {
			return errInvalidParam("PhoneNumber: SMS is not supported by opensnack")
		}
		return errInvalidParam("TopicArn or TargetArn Reason: no value for required parameter")
	}
	t, aerr := h.findTopic(r, arn)
	if aerr != nil {
		return aerr
	}
	attrs, aerr := f.messageAttributes("MessageAttributes")
	if aerr != nil {
		return aerr
	}
	out, aerr := h.publishOne(r, t, &publishInput{
		TopicArn:               t.arn,
		Message:                f.get("Message"),
		Subject:                f.get("Subject"),
		MessageStructure:       f.get("MessageStructure"),
		MessageAttributes:      attrs,
		MessageGroupID:         f.get("MessageGroupId"),
		MessageDeduplicationID: f.get("MessageDeduplicationId"),
	})
	if aerr != nil {
		return aerr
	}
	writeQuery(w, "Publish", struct {
		XMLName        xml.Name `xml:"PublishResult"`
		MessageId      string   `xml:"MessageId"`
		SequenceNumber string   `xml:"SequenceNumber,omitempty"`
	}{MessageId: out.MessageID, SequenceNumber: out.SequenceNumber})
	return nil
}

var batchIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (h *Handler) publishBatch(w http.ResponseWriter, r *http.Request, f form) *apiError {
	t, aerr := h.findTopic(r, f.get("TopicArn"))
	if aerr != nil {
		return aerr
	}
	type entry struct {
		id string
		in *publishInput
	}
	var entries []entry
	seen := map[string]bool{}
	for i := 1; ; i++ {
		p := "PublishBatchRequestEntries.member." + strconv.Itoa(i) + "."
		if !f.has(p + "Id") {
			break
		}
		id := f.get(p + "Id")
		if !batchIDRe.MatchString(id) {
			return &apiError{http.StatusBadRequest, "InvalidBatchEntryId", "The batch entry id " + id + " is invalid."}
		}
		if seen[id] {
			return &apiError{http.StatusBadRequest, "BatchEntryIdsNotDistinct", "Two or more batch entries in the request have the same Id."}
		}
		seen[id] = true
		attrs, aerr := f.messageAttributes(p + "MessageAttributes")
		if aerr != nil {
			return aerr
		}
		entries = append(entries, entry{id: id, in: &publishInput{
			TopicArn:               t.arn,
			Message:                f.get(p + "Message"),
			Subject:                f.get(p + "Subject"),
			MessageStructure:       f.get(p + "MessageStructure"),
			MessageAttributes:      attrs,
			MessageGroupID:         f.get(p + "MessageGroupId"),
			MessageDeduplicationID: f.get(p + "MessageDeduplicationId"),
		}})
	}
	if len(entries) == 0 {
		return &apiError{http.StatusBadRequest, "EmptyBatchRequest", "The batch request doesn't contain any entries."}
	}
	if len(entries) > 10 {
		return &apiError{http.StatusBadRequest, "TooManyEntriesInBatchRequest", "The batch request contains more entries than permissible."}
	}

	type okEntry struct {
		Id             string `xml:"Id"`
		MessageId      string `xml:"MessageId"`
		SequenceNumber string `xml:"SequenceNumber,omitempty"`
	}
	type failEntry struct {
		Id          string `xml:"Id"`
		Code        string `xml:"Code"`
		Message     string `xml:"Message"`
		SenderFault bool   `xml:"SenderFault"`
	}
	result := struct {
		XMLName    xml.Name    `xml:"PublishBatchResult"`
		Successful []okEntry   `xml:"Successful>member"`
		Failed     []failEntry `xml:"Failed>member"`
	}{Successful: []okEntry{}, Failed: []failEntry{}}
	for _, e := range entries {
		out, aerr := h.publishOne(r, t, e.in)
		if aerr != nil {
			result.Failed = append(result.Failed, failEntry{Id: e.id, Code: aerr.Code, Message: aerr.Message, SenderFault: aerr.Status < 500})
			continue
		}
		result.Successful = append(result.Successful, okEntry{Id: e.id, MessageId: out.MessageID, SequenceNumber: out.SequenceNumber})
	}
	writeQuery(w, "PublishBatch", result)
	return nil
}

var fifoSeq atomic.Uint64

func validateMessageAttributes(attrs map[string]MessageAttribute) *apiError {
	if len(attrs) > 10 {
		return errInvalidParam("MessageAttributes: too many attributes")
	}
	for name, v := range attrs {
		switch strings.SplitN(v.DataType, ".", 2)[0] {
		case "String":
			if v.DataType == "String.Array" {
				var arr []any
				if json.Unmarshal([]byte(v.StringValue), &arr) != nil {
					return errInvalidParam("Message attribute '" + name + "' must be a JSON array of strings, numbers, booleans or nulls")
				}
			}
		case "Number":
			if _, err := strconv.ParseFloat(v.StringValue, 64); err != nil {
				return errInvalidParam("Could not cast message attribute '" + name + "' value to number.")
			}
		case "Binary":
		default:
			return errInvalidParam("The message attribute '" + name + "' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.")
		}
	}
	return nil
}

// publishOne validates a message and delivers it to every matching
// subscription of the topic.
func (h *Handler) publishOne(r *http.Request, t *topic, in *publishInput) (*publishOutput, *apiError) {
	if in.Message == "" {
		return nil, errInvalidParam("Empty message")
	}
	if len(in.Subject) > 100 {
		return nil, errInvalidParam("Subject")
	}
	if aerr := validateMessageAttributes(in.MessageAttributes); aerr != nil {
		return nil, aerr
	}
	var structured map[string]string
	if in.MessageStructure != "" {
		if in.MessageStructure != "json" {
			return nil, errInvalidParam("MessageStructure")
		}
		if json.Unmarshal([]byte(in.Message), &structured) != nil {
			return nil, errInvalidParam("Message Structure - JSON message body failed to parse")
		}
		if _, ok := structured["default"]; !ok {
			return nil, errInvalidParam("Message Structure - No default entry in JSON message body")
		}
	}

	n := Notification{
		MessageID:              uuid.NewString(),
		TopicArn:               t.arn,
		Subject:                in.Subject,
		Timestamp:              time.Now().UTC(),
		MessageAttributes:      in.MessageAttributes,
		MessageGroupID:         in.MessageGroupID,
		MessageDeduplicationID: in.MessageDeduplicationID,
	}
	if t.rec.fifo() {
		if in.MessageGroupID == "" {
			return nil, errInvalidParam("The MessageGroupId parameter is required for FIFO topics")
		}
		if n.MessageDeduplicationID == "" {
			if t.rec.Attributes["ContentBasedDeduplication"] != "true" {
				return nil, errInvalidParam("The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
			}
			sum := sha256.Sum256([]byte(in.Message))
			n.MessageDeduplicationID = hex.EncodeToString(sum[:])
		}
		n.SequenceNumber = fmt.Sprintf("1%019d", fifoSeq.Add(1))
	} else if in.MessageDeduplicationID != "" {
		return nil, errInvalidParam("MessageDeduplicationId Reason: The request includes MessageDeduplicationId parameter that is not valid for this topic type")
	}

	subs, err := h.subscriptionsOf(t)
	if err != nil {
		return nil, errInternal(err)
	}
	id := util.IdentityFrom(r)
	for _, s := range subs {
		d := h.deliverers[s.rec.Protocol]
		if d == nil || s.rec.pending() {
			continue
		}
		msg := in.Message
		if structured != nil {
			if v, ok := structured[s.rec.Protocol]; ok {
				msg = v
			} else {
				msg = structured["default"]
			}
		}
		ok, err := filterMatches(s.rec.Attributes["FilterPolicy"], s.rec.Attributes["FilterPolicyScope"], in.MessageAttributes, msg)
		if err != nil || !ok {
			continue
		}
		dn := n
		dn.Message = msg
		delivery := &Delivery{
			Namespace:       id.Namespace,
			AccountID:       id.AccountID,
			SubscriptionArn: s.arn,
			Protocol:        s.rec.Protocol,
			Endpoint:        s.rec.Endpoint,
			Raw:             s.rec.Attributes["RawMessageDelivery"] == "true",
			Notification:    dn,
			Envelope:        envelope(r, &dn, s.arn),
		}
		if err := d.Deliver(r.Context(), delivery); err != nil {
			// SNS delivers asynchronously: a failed delivery never fails
			// the Publish.
			slog.Warn("sns delivery failed", "subscription", s.arn, "endpoint", s.rec.Endpoint, "err", err)
		}
	}
	return &publishOutput{MessageID: n.MessageID, SequenceNumber: n.SequenceNumber}, nil
}

// envelope is the JSON document SNS delivers to SQS and HTTP endpoints when
// raw delivery is off. Fields are in AWS's order.
func envelope(r *http.Request, n *Notification, subscriptionArn string) string {
	type attr struct {
		Type  string `json:"Type"`
		Value string `json:"Value"`
	}
	base := util.BaseURL(r)
	doc := struct {
		Type              string          `json:"Type"`
		MessageId         string          `json:"MessageId"`
		SequenceNumber    string          `json:"SequenceNumber,omitempty"`
		TopicArn          string          `json:"TopicArn"`
		Subject           string          `json:"Subject,omitempty"`
		Message           string          `json:"Message"`
		Timestamp         string          `json:"Timestamp"`
		SignatureVersion  string          `json:"SignatureVersion"`
		Signature         string          `json:"Signature"`
		SigningCertURL    string          `json:"SigningCertURL"`
		UnsubscribeURL    string          `json:"UnsubscribeURL"`
		MessageAttributes map[string]attr `json:"MessageAttributes,omitempty"`
	}{
		Type:             "Notification",
		MessageId:        n.MessageID,
		SequenceNumber:   n.SequenceNumber,
		TopicArn:         n.TopicArn,
		Subject:          n.Subject,
		Message:          n.Message,
		Timestamp:        n.Timestamp.Format("2006-01-02T15:04:05.000Z"),
		SignatureVersion: "1",
		Signature:        base64.StdEncoding.EncodeToString([]byte("opensnack-unsigned:" + n.MessageID)),
		SigningCertURL:   base + "/SimpleNotificationService-opensnack.pem",
		UnsubscribeURL:   base + "/?Action=Unsubscribe&SubscriptionArn=" + url.QueryEscape(subscriptionArn),
	}
	for k, v := range n.MessageAttributes {
		if doc.MessageAttributes == nil {
			doc.MessageAttributes = map[string]attr{}
		}
		val := v.StringValue
		if strings.HasPrefix(v.DataType, "Binary") {
			val = base64.StdEncoding.EncodeToString(v.BinaryValue)
		}
		doc.MessageAttributes[k] = attr{Type: v.DataType, Value: val}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
	return strings.TrimSuffix(buf.String(), "\n")
}
