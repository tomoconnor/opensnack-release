// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// The operations below are protocol-neutral: json.go and query.go decode
// into these inputs and encode the outputs in their own wire format.

type createQueueInput struct {
	QueueName  string            `json:"QueueName"`
	Attributes map[string]string `json:"Attributes"`
	Tags       map[string]string `json:"tags"`
}

type queueURLOutput struct {
	QueueUrl string `json:"QueueUrl"`
}

var queueNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (h *Handler) createQueue(r *http.Request, in *createQueueInput) (*queueURLOutput, *apiError) {
	if in.QueueName == "" {
		return nil, errMissing("QueueName")
	}
	isFifoName := strings.HasSuffix(in.QueueName, ".fifo")
	base := strings.TrimSuffix(in.QueueName, ".fifo")
	if !queueNameRe.MatchString(base) || len(in.QueueName) > 80 {
		return nil, errInvalidParam("Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length")
	}

	attrs := map[string]string{}
	for k, v := range in.Attributes {
		if !isSettableAttr(k) {
			return nil, errInvalidAttrName(k)
		}
		nv, aerr := normalizeAttr(k, v)
		if aerr != nil {
			return nil, aerr
		}
		attrs[k] = nv
	}
	fifo := attrs["FifoQueue"] == "true"
	if fifo != isFifoName {
		if fifo {
			return nil, errInvalidParam("The name of a FIFO queue can only include alphanumeric characters, hyphens, or underscores, must end with .fifo suffix and be 1 to 80 in length.")
		}
		return nil, errInvalidAttrName("FifoQueue")
	}
	if attrs["FifoQueue"] == "false" {
		delete(attrs, "FifoQueue")
	}
	for k := range attrs {
		if fifoOnlyAttrs[k] && !fifo {
			return nil, errInvalidAttrName(k)
		}
	}
	if aerr := h.checkRedrive(r, attrs, fifo); aerr != nil {
		return nil, aerr
	}
	if aerr := validateTags(in.Tags); aerr != nil {
		return nil, aerr
	}

	scope := util.NamespaceFromHeader(r)
	if existing, err := h.Store.Get(in.QueueName, "sqs", "queue", scope); err == nil && existing != nil {
		// Idempotent when every given attribute matches; otherwise SQS says
		// the name is taken.
		eff := effectiveAttrs(decodeQueue(existing))
		for k, v := range attrs {
			if eff[k] != v {
				return nil, newErr("QueueNameExists", "QueueAlreadyExists", "A queue already exists with the same name and a different value for attribute %s", k)
			}
		}
		return &queueURLOutput{QueueUrl: buildQueueURL(r, in.QueueName)}, nil
	}

	now := time.Now().Unix()
	rec := &queueRecord{Name: in.QueueName, Attributes: map[string]string{}, Tags: map[string]string{}, Created: now, Modified: now}
	applyAttrs(rec.Attributes, attrs)
	for k, v := range in.Tags {
		rec.Tags[k] = v
	}
	buf, _ := json.Marshal(rec)
	if err := h.Store.Create(&resource.Resource{ID: in.QueueName, Namespace: scope, Service: "sqs", Type: "queue", Attributes: buf}); err != nil {
		return nil, errInternal(err)
	}
	h.Broker.Drop(queueKey{scope: scope, name: in.QueueName})
	return &queueURLOutput{QueueUrl: buildQueueURL(r, in.QueueName)}, nil
}

// checkRedrive rejects a RedrivePolicy whose dead-letter queue doesn't exist
// or is of the other kind (FIFO vs standard).
func (h *Handler) checkRedrive(r *http.Request, attrs map[string]string, fifo bool) *apiError {
	rp := attrs["RedrivePolicy"]
	if rp == "" {
		return nil
	}
	p, aerr := parseRedrivePolicy(rp)
	if aerr != nil {
		return aerr
	}
	res, _, ok := h.queueByArn(util.IdentityFrom(r).Namespace, p.DeadLetterTargetArn)
	if !ok {
		return errInvalidAttrValue("RedrivePolicy", "Dead-letter target does not exist.")
	}
	if decodeQueue(res).fifo() != fifo {
		return errInvalidAttrValue("RedrivePolicy", "Dead-letter queue must be same type of queue as the source.")
	}
	return nil
}

type queueNameInput struct {
	QueueName              string `json:"QueueName"`
	QueueOwnerAWSAccountId string `json:"QueueOwnerAWSAccountId"`
}

func (h *Handler) getQueueURL(r *http.Request, in *queueNameInput) (*queueURLOutput, *apiError) {
	if in.QueueName == "" {
		return nil, errMissing("QueueName")
	}
	q, aerr := h.lookup(r, in.QueueName)
	if aerr != nil {
		return nil, aerr
	}
	return &queueURLOutput{QueueUrl: q.url}, nil
}

type listQueuesInput struct {
	QueueNamePrefix string `json:"QueueNamePrefix"`
	MaxResults      *int   `json:"MaxResults"`
	NextToken       string `json:"NextToken"`
}

type listQueuesOutput struct {
	QueueUrls []string `json:"QueueUrls,omitempty"`
	NextToken string   `json:"NextToken,omitempty"`
}

func (h *Handler) listQueues(r *http.Request, in *listQueuesInput) (*listQueuesOutput, *apiError) {
	items, err := h.Store.List("sqs", "queue", util.NamespaceFromHeader(r))
	if err != nil {
		return nil, errInternal(err)
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		if strings.HasPrefix(it.ID, in.QueueNamePrefix) {
			names = append(names, it.ID)
		}
	}
	return paginateURLs(r, names, in.MaxResults, in.NextToken)
}

func paginateURLs(r *http.Request, names []string, max *int, token string) (*listQueuesOutput, *apiError) {
	sort.Strings(names)
	if max != nil && (*max < 1 || *max > 1000) {
		return nil, errInvalidParam("Value for parameter MaxResults is invalid. Reason: MaxResults must be an integer between 1 and 1000.")
	}
	if token != "" {
		after, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return nil, errInvalidParam("Invalid NextToken value.")
		}
		i := sort.SearchStrings(names, string(after)+"\x00")
		names = names[i:]
	}
	out := &listQueuesOutput{}
	if max != nil && len(names) > *max {
		names = names[:*max]
		out.NextToken = base64.RawURLEncoding.EncodeToString([]byte(names[len(names)-1]))
	}
	for _, n := range names {
		out.QueueUrls = append(out.QueueUrls, buildQueueURL(r, n))
	}
	return out, nil
}

type queueURLInput struct {
	QueueUrl string `json:"QueueUrl"`
}

func (h *Handler) deleteQueue(r *http.Request, in *queueURLInput) (*struct{}, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	if err := h.Store.Delete(q.rec.Name, "sqs", "queue", q.scope); err != nil {
		return nil, errInternal(err)
	}
	h.Broker.Drop(q.key())
	return &struct{}{}, nil
}

type getQueueAttributesInput struct {
	QueueUrl       string   `json:"QueueUrl"`
	AttributeNames []string `json:"AttributeNames"`
}

type attributesOutput struct {
	Attributes map[string]string `json:"Attributes,omitempty"`
}

func (h *Handler) getQueueAttributes(r *http.Request, in *getQueueAttributesInput) (*attributesOutput, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	all := map[string]string{}
	for k, v := range effectiveAttrs(q.rec) {
		if v != "" {
			all[k] = v
		}
	}
	counts := h.Broker.Counts(h.config(r, q))
	all["QueueArn"] = q.arn
	all["ApproximateNumberOfMessages"] = strconv.Itoa(counts.visible)
	all["ApproximateNumberOfMessagesNotVisible"] = strconv.Itoa(counts.inflight)
	all["ApproximateNumberOfMessagesDelayed"] = strconv.Itoa(counts.delayed)
	all["CreatedTimestamp"] = strconv.FormatInt(q.rec.Created, 10)
	all["LastModifiedTimestamp"] = strconv.FormatInt(q.rec.Modified, 10)

	out := &attributesOutput{Attributes: map[string]string{}}
	for _, name := range in.AttributeNames {
		if !isKnownAttr(name) {
			return nil, errInvalidAttrName(name)
		}
		if name == "All" {
			out.Attributes = all
			break
		}
		if v, ok := all[name]; ok {
			out.Attributes[name] = v
		}
	}
	return out, nil
}

type setQueueAttributesInput struct {
	QueueUrl   string            `json:"QueueUrl"`
	Attributes map[string]string `json:"Attributes"`
}

func (h *Handler) setQueueAttributes(r *http.Request, in *setQueueAttributesInput) (*struct{}, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	if len(in.Attributes) == 0 {
		return nil, errMissing("Attribute.Name")
	}
	changes := map[string]string{}
	for k, v := range in.Attributes {
		if !isSettableAttr(k) {
			return nil, errInvalidAttrName(k)
		}
		if k == "FifoQueue" {
			// Immutable after creation; setting the current value is a no-op.
			if strings.EqualFold(v, strconv.FormatBool(q.rec.fifo())) {
				continue
			}
			return nil, errInvalidAttrName(k)
		}
		if fifoOnlyAttrs[k] && !q.rec.fifo() {
			return nil, errInvalidAttrName(k)
		}
		nv, aerr := normalizeAttr(k, v)
		if aerr != nil {
			return nil, aerr
		}
		changes[k] = nv
	}
	if aerr := h.checkRedrive(r, changes, q.rec.fifo()); aerr != nil {
		return nil, aerr
	}
	applyAttrs(q.rec.Attributes, changes)
	q.rec.Modified = time.Now().Unix()
	if aerr := h.saveQueue(q); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

type tagQueueInput struct {
	QueueUrl string            `json:"QueueUrl"`
	Tags     map[string]string `json:"Tags"`
}

func validateTags(tags map[string]string) *apiError {
	if len(tags) > 50 {
		return errInvalidParam("Too many tags added for queue.")
	}
	for k, v := range tags {
		if k == "" || len(k) > 128 || len(v) > 256 {
			return errInvalidParam("Tag keys must be 1 to 128 characters and values at most 256.")
		}
	}
	return nil
}

func (h *Handler) tagQueue(r *http.Request, in *tagQueueInput) (*struct{}, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	if len(in.Tags) == 0 {
		return nil, errMissing("Tags")
	}
	if aerr := validateTags(in.Tags); aerr != nil {
		return nil, aerr
	}
	for k, v := range in.Tags {
		q.rec.Tags[k] = v
	}
	if aerr := validateTags(q.rec.Tags); aerr != nil {
		return nil, aerr
	}
	if aerr := h.saveQueue(q); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

type untagQueueInput struct {
	QueueUrl string   `json:"QueueUrl"`
	TagKeys  []string `json:"TagKeys"`
}

func (h *Handler) untagQueue(r *http.Request, in *untagQueueInput) (*struct{}, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	if len(in.TagKeys) == 0 {
		return nil, errMissing("TagKeys")
	}
	for _, k := range in.TagKeys {
		delete(q.rec.Tags, k)
	}
	if aerr := h.saveQueue(q); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

type tagsOutput struct {
	Tags map[string]string `json:"Tags,omitempty"`
}

func (h *Handler) listQueueTags(r *http.Request, in *queueURLInput) (*tagsOutput, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	return &tagsOutput{Tags: q.rec.Tags}, nil
}

type listDLQSourcesInput struct {
	QueueUrl   string `json:"QueueUrl"`
	MaxResults *int   `json:"MaxResults"`
	NextToken  string `json:"NextToken"`
}

type listDLQSourcesOutput struct {
	QueueUrls []string `json:"queueUrls"`
	NextToken string   `json:"NextToken,omitempty"`
}

func (h *Handler) listDeadLetterSourceQueues(r *http.Request, in *listDLQSourcesInput) (*listDLQSourcesOutput, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	items, err := h.Store.List("sqs", "queue", q.scope)
	if err != nil {
		return nil, errInternal(err)
	}
	var names []string
	for i := range items {
		rec := decodeQueue(&items[i])
		if rp := rec.Attributes["RedrivePolicy"]; rp != "" {
			if p, aerr := parseRedrivePolicy(rp); aerr == nil && p.DeadLetterTargetArn == q.arn {
				names = append(names, rec.Name)
			}
		}
	}
	page, aerr := paginateURLs(r, names, in.MaxResults, in.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	out := &listDLQSourcesOutput{QueueUrls: page.QueueUrls, NextToken: page.NextToken}
	if out.QueueUrls == nil {
		out.QueueUrls = []string{}
	}
	return out, nil
}

// ── messages ──────────────────────────────────────────────────

type sendMessageInput struct {
	QueueUrl               string                           `json:"QueueUrl"`
	MessageBody            string                           `json:"MessageBody"`
	DelaySeconds           *int                             `json:"DelaySeconds"`
	MessageAttributes      map[string]MessageAttributeValue `json:"MessageAttributes"`
	MessageGroupId         string                           `json:"MessageGroupId"`
	MessageDeduplicationId string                           `json:"MessageDeduplicationId"`
}

type sendMessageOutput struct {
	MessageId              string `json:"MessageId"`
	MD5OfMessageBody       string `json:"MD5OfMessageBody"`
	MD5OfMessageAttributes string `json:"MD5OfMessageAttributes,omitempty"`
	SequenceNumber         string `json:"SequenceNumber,omitempty"`
}

func (h *Handler) sendMessage(r *http.Request, in *sendMessageInput) (*sendMessageOutput, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	return h.send(h.config(r, q), q.rec, senderID(r), in)
}

func senderID(r *http.Request) string {
	id := util.IdentityFrom(r)
	if id.AccessKeyID != "" {
		return id.AccessKeyID
	}
	return id.AccountID
}

var dedupIDRe = regexp.MustCompile(`^[\x21-\x7E]{1,128}$`)

// send validates one message against its queue and enqueues it. SNS
// delivery comes through here too.
func (h *Handler) send(cfg *queueConfig, rec *queueRecord, sender string, in *sendMessageInput) (*sendMessageOutput, *apiError) {
	if in.MessageBody == "" {
		return nil, errMissing("MessageBody")
	}
	if !validBody(in.MessageBody) {
		return nil, newErr("InvalidMessageContents", "", "Invalid characters found. Valid unicode characters are #x9 | #xA | #xD | #x20 to #xD7FF | #xE000 to #xFFFD | #x10000 to #x10FFFF")
	}
	if aerr := validateAttributes(in.MessageAttributes); aerr != nil {
		return nil, aerr
	}
	maxSize := atoiDefault(effectiveAttrs(rec)["MaximumMessageSize"], 262144)
	if size := len(in.MessageBody) + attributesSize(in.MessageAttributes); size > maxSize {
		return nil, errInvalidParam("One or more parameters are invalid. Reason: Message must be shorter than %d bytes.", maxSize)
	}
	if in.DelaySeconds != nil && (*in.DelaySeconds < 0 || *in.DelaySeconds > 900) {
		return nil, errInvalidParam("Value %d for parameter DelaySeconds is invalid. Reason: DelaySeconds must be >= 0 and <= 900.", *in.DelaySeconds)
	}
	if cfg.fifo {
		if in.MessageGroupId == "" {
			return nil, errMissing("MessageGroupId")
		}
		if in.DelaySeconds != nil && *in.DelaySeconds != 0 {
			return nil, errInvalidParam("Value %d for parameter DelaySeconds is invalid. Reason: The request include parameter that is not valid for this queue type.", *in.DelaySeconds)
		}
		if in.MessageDeduplicationId == "" && !cfg.contentDedup {
			return nil, errInvalidParam("The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
		}
		if in.MessageDeduplicationId != "" && !dedupIDRe.MatchString(in.MessageDeduplicationId) {
			return nil, errInvalidParam("Value %s for parameter MessageDeduplicationId is invalid.", in.MessageDeduplicationId)
		}
		if !dedupIDRe.MatchString(in.MessageGroupId) {
			return nil, errInvalidParam("Value %s for parameter MessageGroupId is invalid.", in.MessageGroupId)
		}
	} else if in.MessageDeduplicationId != "" {
		return nil, errInvalidParam("Value %s for parameter MessageDeduplicationId is invalid. Reason: The request includes a parameter that is not valid for this queue type.", in.MessageDeduplicationId)
	}

	res := h.Broker.Send(cfg, sendInput{
		body:     in.MessageBody,
		attrs:    in.MessageAttributes,
		delay:    in.DelaySeconds,
		groupID:  in.MessageGroupId,
		dedupID:  in.MessageDeduplicationId,
		senderID: sender,
	})
	return &sendMessageOutput{MessageId: res.id, MD5OfMessageBody: res.md5Body, MD5OfMessageAttributes: res.md5Attrs, SequenceNumber: res.seq}, nil
}

type batchError struct {
	Id          string `json:"Id"`
	SenderFault bool   `json:"SenderFault"`
	Code        string `json:"Code"`
	Message     string `json:"Message,omitempty"`
}

func batchErr(id string, e *apiError) batchError {
	return batchError{Id: id, SenderFault: e.Sender, Code: e.QueryCode, Message: e.Message}
}

var batchIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// checkBatch applies the batch-level rules every *Batch action shares.
func checkBatch(ids []string) *apiError {
	if len(ids) == 0 {
		return newErr("EmptyBatchRequest", "AWS.SimpleQueueService.EmptyBatchRequest", "There should be at least one SendMessageBatchRequestEntry in the request.")
	}
	if len(ids) > 10 {
		return newErr("TooManyEntriesInBatchRequest", "AWS.SimpleQueueService.TooManyEntriesInBatchRequest", "Maximum number of entries per request are 10. You have sent %d.", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !batchIDRe.MatchString(id) {
			return newErr("InvalidBatchEntryId", "AWS.SimpleQueueService.InvalidBatchEntryId", "A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long.")
		}
		if seen[id] {
			return newErr("BatchEntryIdsNotDistinct", "AWS.SimpleQueueService.BatchEntryIdsNotDistinct", "Id %s repeated.", id)
		}
		seen[id] = true
	}
	return nil
}

type sendBatchEntry struct {
	Id string `json:"Id"`
	sendMessageInput
}

type sendMessageBatchInput struct {
	QueueUrl string           `json:"QueueUrl"`
	Entries  []sendBatchEntry `json:"Entries"`
}

type sendBatchResultEntry struct {
	Id string `json:"Id"`
	sendMessageOutput
}

type sendMessageBatchOutput struct {
	Successful []sendBatchResultEntry `json:"Successful"`
	Failed     []batchError           `json:"Failed"`
}

func (h *Handler) sendMessageBatch(r *http.Request, in *sendMessageBatchInput) (*sendMessageBatchOutput, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	ids := make([]string, len(in.Entries))
	total := 0
	for i, e := range in.Entries {
		ids[i] = e.Id
		total += len(e.MessageBody) + attributesSize(e.MessageAttributes)
	}
	if aerr := checkBatch(ids); aerr != nil {
		return nil, aerr
	}
	if max := atoiDefault(effectiveAttrs(q.rec)["MaximumMessageSize"], 262144); total > max {
		return nil, newErr("BatchRequestTooLong", "AWS.SimpleQueueService.BatchRequestTooLong", "Batch requests cannot be longer than %d bytes. You have sent %d bytes.", max, total)
	}
	cfg := h.config(r, q)
	out := &sendMessageBatchOutput{Successful: []sendBatchResultEntry{}, Failed: []batchError{}}
	for _, e := range in.Entries {
		res, aerr := h.send(cfg, q.rec, senderID(r), &e.sendMessageInput)
		if aerr != nil {
			out.Failed = append(out.Failed, batchErr(e.Id, aerr))
			continue
		}
		out.Successful = append(out.Successful, sendBatchResultEntry{Id: e.Id, sendMessageOutput: *res})
	}
	return out, nil
}

type receiveMessageInput struct {
	QueueUrl                    string   `json:"QueueUrl"`
	AttributeNames              []string `json:"AttributeNames"`
	MessageSystemAttributeNames []string `json:"MessageSystemAttributeNames"`
	MessageAttributeNames       []string `json:"MessageAttributeNames"`
	MaxNumberOfMessages         *int     `json:"MaxNumberOfMessages"`
	VisibilityTimeout           *int     `json:"VisibilityTimeout"`
	WaitTimeSeconds             *int     `json:"WaitTimeSeconds"`
	ReceiveRequestAttemptId     string   `json:"ReceiveRequestAttemptId"`
}

type receivedMessage struct {
	MessageId              string                           `json:"MessageId"`
	ReceiptHandle          string                           `json:"ReceiptHandle"`
	MD5OfBody              string                           `json:"MD5OfBody"`
	Body                   string                           `json:"Body"`
	Attributes             map[string]string                `json:"Attributes,omitempty"`
	MD5OfMessageAttributes string                           `json:"MD5OfMessageAttributes,omitempty"`
	MessageAttributes      map[string]MessageAttributeValue `json:"MessageAttributes,omitempty"`
}

type receiveMessageOutput struct {
	Messages []receivedMessage `json:"Messages,omitempty"`
}

func (h *Handler) receiveMessage(r *http.Request, in *receiveMessageInput) (*receiveMessageOutput, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	max := 1
	if in.MaxNumberOfMessages != nil {
		max = *in.MaxNumberOfMessages
		if max < 1 || max > 10 {
			return nil, errInvalidParam("Value %d for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.", max)
		}
	}
	cfg := h.config(r, q)
	visibility := cfg.visibility
	if in.VisibilityTimeout != nil {
		if *in.VisibilityTimeout < 0 || *in.VisibilityTimeout > 43200 {
			return nil, errInvalidParam("Value %d for parameter VisibilityTimeout is invalid. Reason: Must be between 0 and 43200, if provided.", *in.VisibilityTimeout)
		}
		visibility = time.Duration(*in.VisibilityTimeout) * time.Second
	}
	wait := atoiDefault(effectiveAttrs(q.rec)["ReceiveMessageWaitTimeSeconds"], 0)
	if in.WaitTimeSeconds != nil {
		wait = *in.WaitTimeSeconds
		if wait < 0 || wait > 20 {
			return nil, errInvalidParam("Value %d for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.", wait)
		}
	}

	msgs := h.receiveWait(r.Context(), cfg, max, visibility, time.Duration(wait)*time.Second)

	sysNames := append(append([]string{}, in.AttributeNames...), in.MessageSystemAttributeNames...)
	out := &receiveMessageOutput{}
	for i := range msgs {
		out.Messages = append(out.Messages, renderMessage(&msgs[i], cfg.fifo, sysNames, in.MessageAttributeNames))
	}
	return out, nil
}

// receiveWait long-polls: it returns as soon as any message is available, or
// empty-handed when the wait time runs out or the client goes away.
func (h *Handler) receiveWait(ctx context.Context, cfg *queueConfig, max int, visibility, wait time.Duration) []message {
	deadline := time.Now().Add(wait)
	for {
		ch := h.Broker.waitChan(cfg.key)
		if msgs := h.Broker.Receive(cfg, max, visibility); len(msgs) > 0 {
			return msgs
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		// Messages also become visible by the clock (delays, visibility
		// timeouts), which nothing signals, so poll at least every 100ms.
		tick := min(left, 100*time.Millisecond)
		select {
		case <-ch:
		case <-time.After(tick):
		case <-ctx.Done():
			return nil
		}
	}
}

func wantsName(names []string, name string) bool {
	for _, n := range names {
		if n == "All" || n == name {
			return true
		}
	}
	return false
}

func wantsAttr(names []string, name string) bool {
	for _, n := range names {
		if n == "All" || n == ".*" || n == name {
			return true
		}
		if prefix, ok := strings.CutSuffix(n, ".*"); ok && strings.HasPrefix(name, prefix+".") {
			return true
		}
	}
	return false
}

func renderMessage(m *message, fifo bool, sysNames, attrNames []string) receivedMessage {
	out := receivedMessage{MessageId: m.id, ReceiptHandle: m.receiptHandle, MD5OfBody: m.md5Body, Body: m.body}
	sys := map[string]string{
		"SenderId":                         m.senderID,
		"SentTimestamp":                    strconv.FormatInt(m.sent.UnixMilli(), 10),
		"ApproximateReceiveCount":          strconv.Itoa(m.receiveCount),
		"ApproximateFirstReceiveTimestamp": strconv.FormatInt(m.firstReceive.UnixMilli(), 10),
	}
	if fifo {
		sys["MessageGroupId"] = m.groupID
		sys["MessageDeduplicationId"] = m.dedupID
		sys["SequenceNumber"] = m.seq
	}
	if m.dlqSourceArn != "" {
		sys["DeadLetterQueueSourceArn"] = m.dlqSourceArn
	}
	for k, v := range sys {
		if wantsName(sysNames, k) {
			if out.Attributes == nil {
				out.Attributes = map[string]string{}
			}
			out.Attributes[k] = v
		}
	}
	for k, v := range m.attrs {
		if wantsAttr(attrNames, k) {
			if out.MessageAttributes == nil {
				out.MessageAttributes = map[string]MessageAttributeValue{}
			}
			out.MessageAttributes[k] = v
		}
	}
	out.MD5OfMessageAttributes = md5OfAttributes(out.MessageAttributes)
	return out
}

type deleteMessageInput struct {
	QueueUrl      string `json:"QueueUrl"`
	ReceiptHandle string `json:"ReceiptHandle"`
}

func (h *Handler) deleteMessage(r *http.Request, in *deleteMessageInput) (*struct{}, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	if in.ReceiptHandle == "" {
		return nil, errMissing("ReceiptHandle")
	}
	if aerr := h.Broker.Delete(q.key(), in.ReceiptHandle); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

type handleBatchEntry struct {
	Id                string `json:"Id"`
	ReceiptHandle     string `json:"ReceiptHandle"`
	VisibilityTimeout *int   `json:"VisibilityTimeout"`
}

type handleBatchInput struct {
	QueueUrl string             `json:"QueueUrl"`
	Entries  []handleBatchEntry `json:"Entries"`
}

type batchOK struct {
	Id string `json:"Id"`
}

type handleBatchOutput struct {
	Successful []batchOK    `json:"Successful"`
	Failed     []batchError `json:"Failed"`
}

func (h *Handler) handleBatch(r *http.Request, in *handleBatchInput, each func(q *queue, e handleBatchEntry) *apiError) (*handleBatchOutput, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	ids := make([]string, len(in.Entries))
	for i, e := range in.Entries {
		ids[i] = e.Id
	}
	if aerr := checkBatch(ids); aerr != nil {
		return nil, aerr
	}
	out := &handleBatchOutput{Successful: []batchOK{}, Failed: []batchError{}}
	for _, e := range in.Entries {
		if aerr := each(q, e); aerr != nil {
			out.Failed = append(out.Failed, batchErr(e.Id, aerr))
			continue
		}
		out.Successful = append(out.Successful, batchOK{Id: e.Id})
	}
	return out, nil
}

func (h *Handler) deleteMessageBatch(r *http.Request, in *handleBatchInput) (*handleBatchOutput, *apiError) {
	return h.handleBatch(r, in, func(q *queue, e handleBatchEntry) *apiError {
		if e.ReceiptHandle == "" {
			return errMissing("ReceiptHandle")
		}
		return h.Broker.Delete(q.key(), e.ReceiptHandle)
	})
}

type changeVisibilityInput struct {
	QueueUrl          string `json:"QueueUrl"`
	ReceiptHandle     string `json:"ReceiptHandle"`
	VisibilityTimeout *int   `json:"VisibilityTimeout"`
}

func (h *Handler) changeVisibility(q *queue, handle string, timeout *int) *apiError {
	if handle == "" {
		return errMissing("ReceiptHandle")
	}
	if timeout == nil {
		return errMissing("VisibilityTimeout")
	}
	if *timeout < 0 || *timeout > 43200 {
		return errInvalidParam("Value %d for parameter VisibilityTimeout is invalid. Reason: Must be between 0 and 43200.", *timeout)
	}
	return h.Broker.ChangeVisibility(q.key(), handle, time.Duration(*timeout)*time.Second)
}

func (h *Handler) changeMessageVisibility(r *http.Request, in *changeVisibilityInput) (*struct{}, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.changeVisibility(q, in.ReceiptHandle, in.VisibilityTimeout); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) changeMessageVisibilityBatch(r *http.Request, in *handleBatchInput) (*handleBatchOutput, *apiError) {
	return h.handleBatch(r, in, func(q *queue, e handleBatchEntry) *apiError {
		return h.changeVisibility(q, e.ReceiptHandle, e.VisibilityTimeout)
	})
}

func (h *Handler) purgeQueue(r *http.Request, in *queueURLInput) (*struct{}, *apiError) {
	q, aerr := h.queueFromURL(r, in.QueueUrl)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.Broker.Purge(h.config(r, q)); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}
