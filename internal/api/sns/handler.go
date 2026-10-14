// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sns

import (
	"encoding/json"
	"encoding/xml"
	"maps"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

const APIVersion = "2010-03-31"

type Handler struct {
	Store resource.Store
	// deliverers hand published messages to subscription endpoints, keyed
	// by subscription protocol ("sqs", later "lambda").
	deliverers map[string]Deliverer
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store, deliverers: map[string]Deliverer{}}
}

// RegisterDeliverer makes Publish deliver to subscriptions of a protocol.
func (h *Handler) RegisterDeliverer(protocol string, d Deliverer) {
	h.deliverers[protocol] = d
}

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f := form{r.Form}
	action := f.get("Action")

	var aerr *apiError
	switch action {
	case "CreateTopic":
		aerr = h.createTopic(w, r, f)
	case "ListTopics":
		aerr = h.listTopics(w, r, f)
	case "GetTopicAttributes":
		aerr = h.getTopicAttributes(w, r, f)
	case "SetTopicAttributes":
		aerr = h.setTopicAttributes(w, r, f)
	case "DeleteTopic":
		aerr = h.deleteTopic(w, r, f)
	case "Publish":
		aerr = h.publish(w, r, f)
	case "PublishBatch":
		aerr = h.publishBatch(w, r, f)
	case "Subscribe":
		aerr = h.subscribe(w, r, f)
	case "Unsubscribe":
		aerr = h.unsubscribe(w, r, f)
	case "GetSubscriptionAttributes":
		aerr = h.getSubscriptionAttributes(w, r, f)
	case "SetSubscriptionAttributes":
		aerr = h.setSubscriptionAttributes(w, r, f)
	case "ListSubscriptions":
		aerr = h.listSubscriptions(w, r, f, "")
	case "ListSubscriptionsByTopic":
		if f.get("TopicArn") == "" {
			aerr = errInvalidParam("TopicArn")
		} else {
			aerr = h.listSubscriptions(w, r, f, f.get("TopicArn"))
		}
	case "TagResource":
		aerr = h.tagResource(w, r, f)
	case "UntagResource":
		aerr = h.untagResource(w, r, f)
	case "ListTagsForResource":
		aerr = h.listTagsForResource(w, r, f)
	default:
		aerr = &apiError{http.StatusBadRequest, "InvalidAction", "The action " + action + " is not valid for this endpoint."}
	}
	if aerr != nil {
		writeError(w, aerr)
	}
}

// ── ARNs and scopes ───────────────────────────────────────────

type snsArn struct {
	region, account, topic, subID string
}

// parseArn splits arn:aws:sns:{region}:{account}:{topic}[:{subscription id}].
func parseArn(arn string) (snsArn, bool) {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 || len(parts) > 7 || parts[0] != "arn" || parts[2] != "sns" || parts[5] == "" {
		return snsArn{}, false
	}
	a := snsArn{region: parts[3], account: parts[4], topic: parts[5]}
	if len(parts) == 7 {
		a.subID = parts[6]
	}
	return a, true
}

func topicArn(region, account, name string) string {
	return "arn:aws:sns:" + region + ":" + account + ":" + name
}

// scopeFor is the store scope of an SNS ARN: the caller's tenant in the
// ARN's region.
func scopeFor(r *http.Request, a snsArn) string {
	return util.RegionalScope(util.IdentityFrom(r).Namespace, a.region)
}

// ── topics ───────────────────────────────────────────────────

type topicRecord struct {
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

func (t *topicRecord) fifo() bool { return t.Attributes["FifoTopic"] == "true" }

type topic struct {
	res   *resource.Resource
	rec   *topicRecord
	arn   string
	scope string
}

func decodeTopic(res *resource.Resource) *topicRecord {
	rec := &topicRecord{}
	_ = json.Unmarshal(res.Attributes, rec)
	if rec.Name == "" {
		rec.Name = res.ID
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]string{}
	}
	if rec.Tags == nil {
		rec.Tags = map[string]string{}
	}
	return rec
}

func (h *Handler) findTopic(r *http.Request, arn string) (*topic, *apiError) {
	a, ok := parseArn(arn)
	if !ok || a.subID != "" {
		return nil, errInvalidParam("TopicArn")
	}
	scope := scopeFor(r, a)
	res, err := h.Store.Get(a.topic, "sns", "topic", scope)
	if err != nil || res == nil {
		return nil, errNotFound("Topic does not exist")
	}
	return &topic{res: res, rec: decodeTopic(res), arn: topicArn(a.region, a.account, a.topic), scope: scope}, nil
}

func (h *Handler) saveTopic(t *topic) *apiError {
	buf, _ := json.Marshal(t.rec)
	t.res.Attributes = buf
	if err := h.Store.Update(t.res); err != nil {
		return errInternal(err)
	}
	return nil
}

var topicAttrNames = map[string]bool{
	"DeliveryPolicy": true, "DisplayName": true, "FifoTopic": true, "Policy": true,
	"SignatureVersion": true, "TracingConfig": true, "KmsMasterKeyId": true,
	"ContentBasedDeduplication": true, "ArchivePolicy": true, "FifoThroughputScope": true,
	"DataProtectionPolicy":              true,
	"ApplicationSuccessFeedbackRoleArn": true, "ApplicationSuccessFeedbackSampleRate": true, "ApplicationFailureFeedbackRoleArn": true,
	"HTTPSuccessFeedbackRoleArn": true, "HTTPSuccessFeedbackSampleRate": true, "HTTPFailureFeedbackRoleArn": true,
	"LambdaSuccessFeedbackRoleArn": true, "LambdaSuccessFeedbackSampleRate": true, "LambdaFailureFeedbackRoleArn": true,
	"FirehoseSuccessFeedbackRoleArn": true, "FirehoseSuccessFeedbackSampleRate": true, "FirehoseFailureFeedbackRoleArn": true,
	"SQSSuccessFeedbackRoleArn": true, "SQSSuccessFeedbackSampleRate": true, "SQSFailureFeedbackRoleArn": true,
}

func validateTopicAttr(name, value string) *apiError {
	if !topicAttrNames[name] {
		return errInvalidParam("AttributeName")
	}
	switch name {
	case "Policy", "DeliveryPolicy", "ArchivePolicy", "DataProtectionPolicy":
		if value != "" && !json.Valid([]byte(value)) {
			return errInvalidParam(name + ": not valid JSON")
		}
	case "FifoTopic", "ContentBasedDeduplication":
		if value != "true" && value != "false" {
			return errInvalidParam(name + ": must be true or false")
		}
	case "SignatureVersion":
		if value != "" && value != "1" && value != "2" {
			return errInvalidParam("SignatureVersion")
		}
	case "TracingConfig":
		if value != "" && value != "PassThrough" && value != "Active" {
			return errInvalidParam("TracingConfig")
		}
	}
	return nil
}

var topicNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func (h *Handler) createTopic(w http.ResponseWriter, r *http.Request, f form) *apiError {
	name := f.get("Name")
	if name == "" {
		return errInvalidParam("Name")
	}
	attrs := f.entries("Attributes", "key", "value")
	for k, v := range attrs {
		if aerr := validateTopicAttr(k, v); aerr != nil {
			return aerr
		}
	}
	fifo := attrs["FifoTopic"] == "true"
	if !topicNameRe.MatchString(strings.TrimSuffix(name, ".fifo")) || strings.HasSuffix(name, ".fifo") != fifo {
		return errInvalidParam("Topic Name")
	}
	if !fifo {
		delete(attrs, "FifoTopic")
		if attrs["ContentBasedDeduplication"] == "true" {
			return errInvalidParam("Attributes Reason: ContentBasedDeduplication is only valid for FIFO topics")
		}
	}
	tags := f.tags("Tags")

	scope := util.NamespaceFromHeader(r)
	arn := topicArn(util.Region(r), util.AccountID(r), name)
	result := struct {
		XMLName  xml.Name `xml:"CreateTopicResult"`
		TopicArn string   `xml:"TopicArn"`
	}{TopicArn: arn}

	if res, err := h.Store.Get(name, "sns", "topic", scope); err == nil && res != nil {
		rec := decodeTopic(res)
		if len(tags) > 0 && !maps.Equal(tags, rec.Tags) {
			return errInvalidParam("Tags Reason: Topic already exists with different tags")
		}
		writeQuery(w, "CreateTopic", result)
		return nil
	}

	rec := &topicRecord{Name: name, Attributes: map[string]string{}, Tags: map[string]string{}}
	for k, v := range attrs {
		if v != "" {
			rec.Attributes[k] = v
		}
	}
	maps.Copy(rec.Tags, tags)
	buf, _ := json.Marshal(rec)
	if err := h.Store.Create(&resource.Resource{ID: name, Namespace: scope, Service: "sns", Type: "topic", Attributes: buf}); err != nil {
		return errInternal(err)
	}
	writeQuery(w, "CreateTopic", result)
	return nil
}

func (h *Handler) listTopics(w http.ResponseWriter, r *http.Request, f form) *apiError {
	items, err := h.Store.List("sns", "topic", util.NamespaceFromHeader(r))
	if err != nil {
		return errInternal(err)
	}
	type member struct {
		TopicArn string `xml:"TopicArn"`
	}
	result := struct {
		XMLName xml.Name `xml:"ListTopicsResult"`
		Topics  []member `xml:"Topics>member"`
	}{Topics: []member{}}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	for _, it := range items {
		result.Topics = append(result.Topics, member{TopicArn: topicArn(util.Region(r), util.AccountID(r), it.ID)})
	}
	writeQuery(w, "ListTopics", result)
	return nil
}

func defaultTopicPolicy(arn, account string) string {
	return `{"Version":"2008-10-17","Id":"__default_policy_ID","Statement":[{"Sid":"__default_statement_ID","Effect":"Allow","Principal":{"AWS":"*"},"Action":["SNS:GetTopicAttributes","SNS:SetTopicAttributes","SNS:AddPermission","SNS:RemovePermission","SNS:DeleteTopic","SNS:Subscribe","SNS:ListSubscriptionsByTopic","SNS:Publish"],"Resource":"` + arn + `","Condition":{"StringEquals":{"AWS:SourceOwner":"` + account + `"}}}]}`
}

const defaultEffectiveDeliveryPolicy = `{"http":{"defaultHealthyRetryPolicy":{"minDelayTarget":20,"maxDelayTarget":20,"numRetries":3,"numMaxDelayRetries":0,"numNoDelayRetries":0,"numMinDelayRetries":0,"backoffFunction":"linear"},"disableSubscriptionOverrides":false,"defaultRequestPolicy":{"headerContentType":"text/plain; charset=UTF-8"}}}`

func (h *Handler) getTopicAttributes(w http.ResponseWriter, r *http.Request, f form) *apiError {
	t, aerr := h.findTopic(r, f.get("TopicArn"))
	if aerr != nil {
		return aerr
	}
	a, _ := parseArn(t.arn)
	subs, err := h.subscriptionsOf(t)
	if err != nil {
		return errInternal(err)
	}
	confirmed, pending := 0, 0
	for _, s := range subs {
		if s.rec.pending() {
			pending++
		} else {
			confirmed++
		}
	}
	attrs := map[string]string{
		"TopicArn":                t.arn,
		"Owner":                   a.account,
		"Policy":                  defaultTopicPolicy(t.arn, a.account),
		"DisplayName":             "",
		"SubscriptionsConfirmed":  strconv.Itoa(confirmed),
		"SubscriptionsPending":    strconv.Itoa(pending),
		"SubscriptionsDeleted":    "0",
		"EffectiveDeliveryPolicy": defaultEffectiveDeliveryPolicy,
	}
	if t.rec.fifo() {
		attrs["FifoTopic"] = "true"
		attrs["ContentBasedDeduplication"] = "false"
		attrs["FifoThroughputScope"] = "Topic"
	}
	maps.Copy(attrs, t.rec.Attributes)
	if dp := t.rec.Attributes["DeliveryPolicy"]; dp != "" {
		attrs["EffectiveDeliveryPolicy"] = dp
	}
	writeQuery(w, "GetTopicAttributes", struct {
		XMLName xml.Name `xml:"GetTopicAttributesResult"`
		xmlAttributes
	}{xmlAttributes: xmlAttributes{Entry: xmlEntries(attrs)}})
	return nil
}

func (h *Handler) setTopicAttributes(w http.ResponseWriter, r *http.Request, f form) *apiError {
	t, aerr := h.findTopic(r, f.get("TopicArn"))
	if aerr != nil {
		return aerr
	}
	name, value := f.get("AttributeName"), f.get("AttributeValue")
	if aerr := validateTopicAttr(name, value); aerr != nil {
		return aerr
	}
	switch {
	case name == "FifoTopic":
		if value != strconv.FormatBool(t.rec.fifo()) {
			return errInvalidParam("AttributeName: FifoTopic can't be changed")
		}
	case name == "ContentBasedDeduplication" && !t.rec.fifo():
		return errInvalidParam("AttributeName: ContentBasedDeduplication is only valid for FIFO topics")
	case value == "":
		delete(t.rec.Attributes, name)
	default:
		t.rec.Attributes[name] = value
	}
	if aerr := h.saveTopic(t); aerr != nil {
		return aerr
	}
	writeQuery(w, "SetTopicAttributes", nil)
	return nil
}

func (h *Handler) deleteTopic(w http.ResponseWriter, r *http.Request, f form) *apiError {
	a, ok := parseArn(f.get("TopicArn"))
	if !ok || a.subID != "" {
		return errInvalidParam("TopicArn")
	}
	if t, aerr := h.findTopic(r, f.get("TopicArn")); aerr == nil {
		// Deleting a topic deletes its subscriptions.
		subs, _ := h.subscriptionsOf(t)
		for _, s := range subs {
			_ = h.Store.Delete(s.res.ID, "sns", "subscription", s.res.Namespace)
		}
		if err := h.Store.Delete(a.topic, "sns", "topic", t.scope); err != nil {
			return errInternal(err)
		}
	}
	writeQuery(w, "DeleteTopic", nil)
	return nil
}

// ── tags ─────────────────────────────────────────────────────

func (h *Handler) taggedTopic(r *http.Request, f form) (*topic, *apiError) {
	arn := f.get("ResourceArn")
	if arn == "" {
		return nil, errInvalidParam("ResourceArn")
	}
	t, aerr := h.findTopic(r, arn)
	if aerr != nil && aerr.Code == "NotFound" {
		return nil, &apiError{http.StatusNotFound, "ResourceNotFound", "Resource does not exist"}
	}
	return t, aerr
}

func (h *Handler) tagResource(w http.ResponseWriter, r *http.Request, f form) *apiError {
	t, aerr := h.taggedTopic(r, f)
	if aerr != nil {
		return aerr
	}
	tags := f.tags("Tags")
	if len(tags) == 0 {
		return errInvalidParam("Tags")
	}
	for k, v := range tags {
		if k == "" || len(k) > 128 || len(v) > 256 {
			return errInvalidParam("Tags")
		}
		t.rec.Tags[k] = v
	}
	if len(t.rec.Tags) > 50 {
		return &apiError{http.StatusBadRequest, "TagLimitExceeded", "Could not complete request: tag quota of per resource exceeded"}
	}
	if aerr := h.saveTopic(t); aerr != nil {
		return aerr
	}
	writeQuery(w, "TagResource", struct {
		XMLName xml.Name `xml:"TagResourceResult"`
	}{})
	return nil
}

func (h *Handler) untagResource(w http.ResponseWriter, r *http.Request, f form) *apiError {
	t, aerr := h.taggedTopic(r, f)
	if aerr != nil {
		return aerr
	}
	for _, k := range f.members("TagKeys") {
		delete(t.rec.Tags, k)
	}
	if aerr := h.saveTopic(t); aerr != nil {
		return aerr
	}
	writeQuery(w, "UntagResource", struct {
		XMLName xml.Name `xml:"UntagResourceResult"`
	}{})
	return nil
}

func (h *Handler) listTagsForResource(w http.ResponseWriter, r *http.Request, f form) *apiError {
	t, aerr := h.taggedTopic(r, f)
	if aerr != nil {
		return aerr
	}
	tags := []xmlTag{}
	for _, e := range xmlEntries(t.rec.Tags) {
		tags = append(tags, xmlTag{Key: e.Key, Value: e.Value})
	}
	writeQuery(w, "ListTagsForResource", struct {
		XMLName xml.Name `xml:"ListTagsForResourceResult"`
		Tags    []xmlTag `xml:"Tags>member"`
	}{Tags: tags})
	return nil
}

// ── subscriptions ────────────────────────────────────────────

type subscriptionRecord struct {
	TopicArn   string            `json:"topic_arn"`
	Protocol   string            `json:"protocol"`
	Endpoint   string            `json:"endpoint"`
	Owner      string            `json:"owner"`
	Pending    bool              `json:"pending,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

func (s *subscriptionRecord) pending() bool { return s.Pending }

type subscription struct {
	res *resource.Resource
	rec *subscriptionRecord
	arn string
}

func decodeSubscription(res *resource.Resource) *subscription {
	rec := &subscriptionRecord{}
	_ = json.Unmarshal(res.Attributes, rec)
	if rec.Attributes == nil {
		rec.Attributes = map[string]string{}
	}
	return &subscription{res: res, rec: rec, arn: rec.TopicArn + ":" + res.ID}
}

func (h *Handler) subscriptionsOf(t *topic) ([]*subscription, error) {
	items, err := h.Store.List("sns", "subscription", t.scope)
	if err != nil {
		return nil, err
	}
	var out []*subscription
	for i := range items {
		s := decodeSubscription(&items[i])
		if s.rec.TopicArn == t.arn {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].arn < out[j].arn })
	return out, nil
}

func (h *Handler) findSubscription(r *http.Request, arn string) (*subscription, *apiError) {
	a, ok := parseArn(arn)
	if !ok || a.subID == "" {
		return nil, errInvalidParam("SubscriptionArn")
	}
	res, err := h.Store.Get(a.subID, "sns", "subscription", scopeFor(r, a))
	if err != nil || res == nil {
		return nil, errNotFound("Subscription does not exist")
	}
	return decodeSubscription(res), nil
}

func (h *Handler) saveSubscription(s *subscription) *apiError {
	buf, _ := json.Marshal(s.rec)
	s.res.Attributes = buf
	if err := h.Store.Update(s.res); err != nil {
		return errInternal(err)
	}
	return nil
}

var subscriptionProtocols = map[string]bool{
	"http": true, "https": true, "email": true, "email-json": true, "sms": true,
	"sqs": true, "application": true, "lambda": true, "firehose": true,
}

// needsConfirmation lists the protocols whose endpoint must confirm the
// subscription; opensnack can't reach those endpoints, so they stay pending.
var needsConfirmation = map[string]bool{"http": true, "https": true, "email": true, "email-json": true}

var subscriptionAttrNames = map[string]bool{
	"DeliveryPolicy": true, "FilterPolicy": true, "FilterPolicyScope": true,
	"RawMessageDelivery": true, "RedrivePolicy": true, "SubscriptionRoleArn": true,
	"ReplayPolicy": true,
}

// setSubscriptionAttr validates one attribute and applies it to attrs.
func setSubscriptionAttr(attrs map[string]string, protocol, name, value string) *apiError {
	if !subscriptionAttrNames[name] {
		return errInvalidParam("AttributeName")
	}
	switch name {
	case "RawMessageDelivery":
		v := strings.ToLower(value)
		if v != "true" && v != "false" {
			return errInvalidParam("Attributes Reason: RawMessageDelivery: Invalid value [" + value + "]. Must be true or false.")
		}
		switch protocol {
		case "sqs", "http", "https", "firehose":
		default:
			return errInvalidParam("Attributes Reason: Delivery protocol [" + protocol + "] does not support raw message delivery.")
		}
		attrs[name] = v
		return nil
	case "FilterPolicyScope":
		if value != "MessageAttributes" && value != "MessageBody" {
			return errInvalidParam("Attributes Reason: FilterPolicyScope: Invalid value [" + value + "]. Please use either MessageBody or MessageAttributes")
		}
	case "FilterPolicy":
		if value == "" || value == "{}" {
			delete(attrs, name)
			return nil
		}
		if err := validateFilterPolicy(value); err != nil {
			return errInvalidParam("Filter policy: " + err.Error())
		}
	case "DeliveryPolicy", "RedrivePolicy", "ReplayPolicy":
		if value != "" && !json.Valid([]byte(value)) {
			return errInvalidParam(name + ": not valid JSON")
		}
	}
	if value == "" {
		delete(attrs, name)
	} else {
		attrs[name] = value
	}
	return nil
}

func (h *Handler) subscribe(w http.ResponseWriter, r *http.Request, f form) *apiError {
	protocol, endpoint := f.get("Protocol"), f.get("Endpoint")
	if !subscriptionProtocols[protocol] {
		return errInvalidParam("Invalid protocol type: " + protocol)
	}
	if endpoint == "" {
		return errInvalidParam("Endpoint")
	}
	if protocol == "sqs" && !strings.HasPrefix(endpoint, "arn:aws:sqs:") {
		return errInvalidParam("SQS endpoint ARN")
	}
	t, aerr := h.findTopic(r, f.get("TopicArn"))
	if aerr != nil {
		return aerr
	}
	attrs := map[string]string{}
	for k, v := range f.entries("Attributes", "key", "value") {
		if aerr := setSubscriptionAttr(attrs, protocol, k, v); aerr != nil {
			return aerr
		}
	}

	subs, err := h.subscriptionsOf(t)
	if err != nil {
		return errInternal(err)
	}
	var sub *subscription
	for _, s := range subs {
		if s.rec.Protocol == protocol && s.rec.Endpoint == endpoint {
			sub = s
			break
		}
	}
	if sub == nil {
		id := uuid.NewString()
		rec := &subscriptionRecord{
			TopicArn: t.arn, Protocol: protocol, Endpoint: endpoint,
			Owner: util.AccountID(r), Pending: needsConfirmation[protocol], Attributes: attrs,
		}
		buf, _ := json.Marshal(rec)
		res := &resource.Resource{ID: id, Namespace: t.scope, Service: "sns", Type: "subscription", Attributes: buf}
		if err := h.Store.Create(res); err != nil {
			return errInternal(err)
		}
		sub = &subscription{res: res, rec: rec, arn: t.arn + ":" + id}
	}

	arn := sub.arn
	if sub.rec.pending() && f.get("ReturnSubscriptionArn") != "true" {
		arn = "pending confirmation"
	}
	writeQuery(w, "Subscribe", struct {
		XMLName         xml.Name `xml:"SubscribeResult"`
		SubscriptionArn string   `xml:"SubscriptionArn"`
	}{SubscriptionArn: arn})
	return nil
}

func (h *Handler) unsubscribe(w http.ResponseWriter, r *http.Request, f form) *apiError {
	s, aerr := h.findSubscription(r, f.get("SubscriptionArn"))
	if aerr != nil {
		return aerr
	}
	if err := h.Store.Delete(s.res.ID, "sns", "subscription", s.res.Namespace); err != nil {
		return errInternal(err)
	}
	writeQuery(w, "Unsubscribe", nil)
	return nil
}

func (h *Handler) getSubscriptionAttributes(w http.ResponseWriter, r *http.Request, f form) *apiError {
	s, aerr := h.findSubscription(r, f.get("SubscriptionArn"))
	if aerr != nil {
		return aerr
	}
	attrs := map[string]string{
		"SubscriptionArn":              s.arn,
		"TopicArn":                     s.rec.TopicArn,
		"Protocol":                     s.rec.Protocol,
		"Endpoint":                     s.rec.Endpoint,
		"Owner":                        s.rec.Owner,
		"PendingConfirmation":          strconv.FormatBool(s.rec.pending()),
		"ConfirmationWasAuthenticated": strconv.FormatBool(!s.rec.pending()),
		"RawMessageDelivery":           "false",
		"SubscriptionPrincipal":        "arn:aws:iam::" + s.rec.Owner + ":root",
	}
	maps.Copy(attrs, s.rec.Attributes)
	if attrs["FilterPolicy"] != "" && attrs["FilterPolicyScope"] == "" {
		attrs["FilterPolicyScope"] = "MessageAttributes"
	}
	writeQuery(w, "GetSubscriptionAttributes", struct {
		XMLName xml.Name `xml:"GetSubscriptionAttributesResult"`
		xmlAttributes
	}{xmlAttributes: xmlAttributes{Entry: xmlEntries(attrs)}})
	return nil
}

func (h *Handler) setSubscriptionAttributes(w http.ResponseWriter, r *http.Request, f form) *apiError {
	s, aerr := h.findSubscription(r, f.get("SubscriptionArn"))
	if aerr != nil {
		return aerr
	}
	if aerr := setSubscriptionAttr(s.rec.Attributes, s.rec.Protocol, f.get("AttributeName"), f.get("AttributeValue")); aerr != nil {
		return aerr
	}
	if aerr := h.saveSubscription(s); aerr != nil {
		return aerr
	}
	writeQuery(w, "SetSubscriptionAttributes", nil)
	return nil
}

func (h *Handler) listSubscriptions(w http.ResponseWriter, r *http.Request, f form, topicArn string) *apiError {
	var subs []*subscription
	action := "ListSubscriptions"
	if topicArn != "" {
		action = "ListSubscriptionsByTopic"
		t, aerr := h.findTopic(r, topicArn)
		if aerr != nil {
			return aerr
		}
		var err error
		if subs, err = h.subscriptionsOf(t); err != nil {
			return errInternal(err)
		}
	} else {
		items, err := h.Store.List("sns", "subscription", util.NamespaceFromHeader(r))
		if err != nil {
			return errInternal(err)
		}
		for i := range items {
			subs = append(subs, decodeSubscription(&items[i]))
		}
		sort.Slice(subs, func(i, j int) bool { return subs[i].arn < subs[j].arn })
	}
	members := []xmlSubscription{}
	for _, s := range subs {
		arn := s.arn
		if s.rec.pending() {
			arn = "PendingConfirmation"
		}
		members = append(members, xmlSubscription{
			SubscriptionArn: arn, Owner: s.rec.Owner, Protocol: s.rec.Protocol,
			Endpoint: s.rec.Endpoint, TopicArn: s.rec.TopicArn,
		})
	}
	writeQuery(w, action, struct {
		XMLName       xml.Name
		Subscriptions []xmlSubscription `xml:"Subscriptions>member"`
	}{XMLName: xml.Name{Local: action + "Result"}, Subscriptions: members})
	return nil
}
