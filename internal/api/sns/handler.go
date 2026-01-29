// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sns

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

const (
	APIVersion = "2010-03-31"
	snsRegion  = "us-east-1"
	snsAccount = "000000000000"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// Build SNS ARN
func topicArn(name string) string {
	return "arn:aws:sns:" + snsRegion + ":" + snsAccount + ":" + name
}

// Build SNS Subscription ARN
func subscriptionArn(subscriptionID string) string {
	return "arn:aws:sns:" + snsRegion + ":" + snsAccount + ":" + subscriptionID
}

// SNS Dispatcher
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	// AWS Query APIs send parameters in the form body.
	r.ParseForm()
	action := r.FormValue("Action")
	if action == "" {
		action = r.URL.Query().Get("Action")
	}

	switch action {
	case "CreateTopic":
		h.CreateTopic(w, r)
	case "ListTopics":
		h.ListTopics(w, r)
	case "GetTopicAttributes":
		h.GetTopicAttributes(w, r)
	case "SetTopicAttributes":
		h.SetTopicAttributes(w, r)
	case "DeleteTopic":
		h.DeleteTopic(w, r)
	case "Publish":
		h.Publish(w, r)
	case "ListTagsForResource":
		h.ListTagsForResource(w, r)
	case "Subscribe":
		h.Subscribe(w, r)
	case "GetSubscriptionAttributes":
		h.GetSubscriptionAttributes(w, r)
	case "ListSubscriptionsByTopic":
		h.ListSubscriptionsByTopic(w, r)
	case "Unsubscribe":
		h.Unsubscribe(w, r)
	default:
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidAction",
			"Unknown SNS Action",
			action,
		)
	}
}

// CreateTopic
func (h *Handler) CreateTopic(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	topicName := r.FormValue("Name")

	if topicName == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"Name is required",
			"",
		)
	}

	// Check if exists
	_, err := h.Store.Get(topicName, "sns", "topic", ns)
	if err == nil {
		// Return existing ARN
		resp := CreateTopicResponse{
			CreateTopicResult: CreateTopicResult{
				TopicArn: topicArn(topicName),
			},
			ResponseMetadata: ResponseMetadata{
				RequestId: awsresponses.NextRequestID(),
			},
		}
		awsresponses.WriteXML(w, resp)
	}

	// Create
	entry := map[string]any{
		"name":       topicName,
		"created_at": time.Now().UTC(),
	}

	buf, _ := json.Marshal(entry)
	res := &resource.Resource{
		ID:         topicName,
		Namespace:  ns,
		Service:    "sns",
		Type:       "topic",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
	}

	resp := CreateTopicResponse{
		CreateTopicResult: CreateTopicResult{
			TopicArn: topicArn(topicName),
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// ListTopics
func (h *Handler) ListTopics(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	items, err := h.Store.List("sns", "topic", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
	}

	members := []TopicArnMember{}

	for _, it := range items {
		members = append(members, TopicArnMember{
			TopicArn: topicArn(it.ID),
		})
	}

	resp := ListTopicsResponse{
		ListTopicsResult: ListTopicsResult{
			Topics: members,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// DeleteTopic
func (h *Handler) DeleteTopic(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	arn := r.FormValue("TopicArn")
	if arn == "" {
		arn = r.URL.Query().Get("TopicArn")
	}

	if arn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"TopicArn is required",
			"",
		)
	}

	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidParameter",
			"Invalid TopicArn format",
			arn,
		)
	}
	name := parts[len(parts)-1]

	// AWS allows idempotent delete - it's OK if the topic doesn't exist
	_ = h.Store.Delete(name, "sns", "topic", ns)

	resp := DeleteTopicResponse{
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// Publish (stub)
func (h *Handler) Publish(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	message := r.FormValue("Message")
	if message == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"Message is required",
			"",
		)
	}

	resp := PublishResponse{
		PublishResult: PublishResult{
			MessageId: uuid.NewString(),
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// GetTopicAttributes
func (h *Handler) GetTopicAttributes(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	arn := r.FormValue("TopicArn")
	if arn == "" {
		arn = r.URL.Query().Get("TopicArn")
	}

	if arn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"TopicArn is required",
			"",
		)
	}

	parts := strings.Split(arn, ":")
	topicName := parts[len(parts)-1]

	// Get topic from store
	topic, err := h.Store.Get(topicName, "sns", "topic", ns)
	if err != nil {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"NotFound",
			"Topic does not exist",
			arn,
		)
	}

	// Parse stored attributes
	var storedAttrs map[string]interface{}
	if err := json.Unmarshal(topic.Attributes, &storedAttrs); err != nil {
		storedAttrs = make(map[string]interface{})
	}

	// Get topic attributes if they exist
	var topicAttrs map[string]string
	if attrs, ok := storedAttrs["attributes"].(map[string]interface{}); ok {
		topicAttrs = make(map[string]string)
		for k, v := range attrs {
			if str, ok := v.(string); ok {
				topicAttrs[k] = str
			}
		}
	} else if attrs, ok := storedAttrs["attributes"].(map[string]string); ok {
		topicAttrs = attrs
	} else {
		topicAttrs = make(map[string]string)
	}

	// Build response attributes with defaults
	responseAttrs := make(map[string]string)
	responseAttrs["TopicArn"] = arn
	responseAttrs["Owner"] = "000000000000"

	// Include all stored attributes (excluding empty strings)
	// Special handling for Policy - AWS always returns it, defaulting to {} if not set
	hasPolicy := false
	for k, v := range topicAttrs {
		if v != "" {
			// For Policy attribute, ensure it's valid JSON before including it
			if k == "Policy" {
				// Try to parse as JSON to validate it
				var test interface{}
				if err := json.Unmarshal([]byte(v), &test); err == nil {
					responseAttrs[k] = v
					hasPolicy = true
				}
				// If it's not valid JSON, don't include it (will use default below)
			} else {
				responseAttrs[k] = v
			}
		}
	}

	// Always include Policy attribute - AWS SNS returns a default policy if no policy is set
	if !hasPolicy {
		responseAttrs["Policy"] = `{"Version":"2012-10-17","Statement":[{"Sid":"Statement1","Effect":"Allow","Principal":"*","Action":"sns:*","Resource":"*"}]}`
	}

	// Convert map to slice of AttributeEntry
	var entries []AttributeEntry
	for k, v := range responseAttrs {
		entries = append(entries, AttributeEntry{
			Key:   k,
			Value: v,
		})
	}

	resp := GetTopicAttributesResponse{
		GetTopicAttributesResult: GetTopicAttributesResult{
			Attributes: Attributes{
				Entries: entries,
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// SetTopicAttributes
func (h *Handler) SetTopicAttributes(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	arn := r.FormValue("TopicArn")
	if arn == "" {
		arn = r.URL.Query().Get("TopicArn")
	}
	r.ParseForm()
	attributeName := r.FormValue("AttributeName")
	if attributeName == "" {
		attributeName = r.URL.Query().Get("AttributeName")
	}
	attributeValue := r.FormValue("AttributeValue")
	if attributeValue == "" {
		attributeValue = r.URL.Query().Get("AttributeValue")
	}

	if arn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"TopicArn is required",
			"",
		)
	}

	if attributeName == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"AttributeName is required",
			"",
		)
	}

	parts := strings.Split(arn, ":")
	topicName := parts[len(parts)-1]

	// Get topic from store
	topic, err := h.Store.Get(topicName, "sns", "topic", ns)
	if err != nil {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"NotFound",
			"Topic does not exist",
			arn,
		)
	}

	// Parse existing attributes
	var storedAttrs map[string]interface{}
	if err := json.Unmarshal(topic.Attributes, &storedAttrs); err != nil {
		storedAttrs = make(map[string]interface{})
	}

	// Get existing topic attributes (if any)
	var topicAttrs map[string]string
	if attrs, ok := storedAttrs["attributes"].(map[string]interface{}); ok {
		topicAttrs = make(map[string]string)
		for k, v := range attrs {
			if str, ok := v.(string); ok {
				topicAttrs[k] = str
			}
		}
	} else if attrs, ok := storedAttrs["attributes"].(map[string]string); ok {
		topicAttrs = attrs
	} else {
		topicAttrs = make(map[string]string)
	}

	// Update attribute
	// If attribute value is empty string, remove it (AWS SNS behavior)
	if attributeValue == "" {
		delete(topicAttrs, attributeName)
	} else {
		topicAttrs[attributeName] = attributeValue
	}

	// Update stored attributes
	storedAttrs["attributes"] = topicAttrs

	// Preserve other fields like name and created_at
	if _, ok := storedAttrs["name"]; !ok {
		storedAttrs["name"] = topicName
	}
	if _, ok := storedAttrs["created_at"]; !ok {
		storedAttrs["created_at"] = time.Now().UTC()
	}

	// Marshal updated attributes
	buf, err := json.Marshal(storedAttrs)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, err.Error())
	}

	// Update topic in store
	topic.Attributes = buf
	if err := h.Store.Update(topic); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, err.Error())
	}

	resp := SetTopicAttributesResponse{
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// ListTagsForResource
func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	arn := r.FormValue("ResourceArn")
	if arn == "" {
		arn = r.URL.Query().Get("ResourceArn")
	}

	if arn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"ResourceArn is required",
			"",
		)
	}

	// Extract topic name from ARN
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidParameter",
			"Invalid ResourceArn format",
			arn,
		)
	}
	topicName := parts[len(parts)-1]

	// Get topic from store
	topic, err := h.Store.Get(topicName, "sns", "topic", ns)
	if err != nil {
		// AWS returns empty tags if resource doesn't exist
		resp := ListTagsForResourceResponse{
			ListTagsForResourceResult: ListTagsForResourceResult{
				Tags: Tags{
					Tags: []Tag{},
				},
			},
			ResponseMetadata: ResponseMetadata{
				RequestId: awsresponses.NextRequestID(),
			},
		}
		awsresponses.WriteXML(w, resp)
	}

	// Parse stored attributes
	var storedAttrs map[string]interface{}
	if err := json.Unmarshal(topic.Attributes, &storedAttrs); err != nil {
		storedAttrs = make(map[string]interface{})
	}

	// Extract tags from attributes
	var tags []Tag
	if tagMap, ok := storedAttrs["tags"].(map[string]interface{}); ok {
		for k, v := range tagMap {
			var value string
			if str, ok := v.(string); ok {
				value = str
			} else {
				// Convert non-string values to string
				value = fmt.Sprintf("%v", v)
			}
			tags = append(tags, Tag{
				Key:   k,
				Value: value,
			})
		}
	}

	resp := ListTagsForResourceResponse{
		ListTagsForResourceResult: ListTagsForResourceResult{
			Tags: Tags{
				Tags: tags,
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// Subscribe
func (h *Handler) Subscribe(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	topicArn := r.FormValue("TopicArn")
	if topicArn == "" {
		topicArn = r.URL.Query().Get("TopicArn")
	}
	protocol := r.FormValue("Protocol")
	if protocol == "" {
		protocol = r.URL.Query().Get("Protocol")
	}
	endpoint := r.FormValue("Endpoint")
	if endpoint == "" {
		endpoint = r.URL.Query().Get("Endpoint")
	}

	if topicArn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"TopicArn is required",
			"",
		)
	}

	if protocol == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"Protocol is required",
			"",
		)
	}

	if endpoint == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"Endpoint is required",
			"",
		)
	}

	// Extract topic name from ARN
	parts := strings.Split(topicArn, ":")
	if len(parts) < 6 {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidParameter",
			"Invalid TopicArn format",
			topicArn,
		)
	}
	topicName := parts[len(parts)-1]

	// Verify topic exists
	_, err := h.Store.Get(topicName, "sns", "topic", ns)
	if err != nil {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"NotFound",
			"Topic does not exist",
			topicArn,
		)
	}

	// Generate subscription ID (using UUID for uniqueness)
	subscriptionID := uuid.NewString()
	subscriptionArn := subscriptionArn(subscriptionID)

	// Create subscription entry
	entry := map[string]any{
		"topic_arn":  topicArn,
		"protocol":   protocol,
		"endpoint":   endpoint,
		"created_at": time.Now().UTC(),
	}

	buf, _ := json.Marshal(entry)
	res := &resource.Resource{
		ID:         subscriptionID,
		Namespace:  ns,
		Service:    "sns",
		Type:       "subscription",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, err.Error())
	}

	resp := SubscribeResponse{
		SubscribeResult: SubscribeResult{
			SubscriptionArn: subscriptionArn,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// GetSubscriptionAttributes
func (h *Handler) GetSubscriptionAttributes(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	subscriptionArn := r.FormValue("SubscriptionArn")
	if subscriptionArn == "" {
		subscriptionArn = r.URL.Query().Get("SubscriptionArn")
	}

	if subscriptionArn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"SubscriptionArn is required",
			"",
		)
	}

	// Extract subscription ID from ARN
	// Format: arn:aws:sns:region:account:subscription-id
	parts := strings.Split(subscriptionArn, ":")
	if len(parts) < 6 {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidParameter",
			"Invalid SubscriptionArn format",
			subscriptionArn,
		)
	}
	subscriptionID := parts[len(parts)-1]

	// Get subscription from store
	subscription, err := h.Store.Get(subscriptionID, "sns", "subscription", ns)
	if err != nil {
		awsresponses.WriteErrorXML(
			w,
			http.StatusNotFound,
			"NotFound",
			"Subscription does not exist",
			subscriptionArn,
		)
	}

	// Parse stored attributes
	var storedAttrs map[string]interface{}
	if err := json.Unmarshal(subscription.Attributes, &storedAttrs); err != nil {
		storedAttrs = make(map[string]interface{})
	}

	// Extract stored values
	topicArn := ""
	if ta, ok := storedAttrs["topic_arn"].(string); ok {
		topicArn = ta
	}
	protocol := ""
	if p, ok := storedAttrs["protocol"].(string); ok {
		protocol = p
	}
	endpoint := ""
	if e, ok := storedAttrs["endpoint"].(string); ok {
		endpoint = e
	}

	// Build response attributes
	responseAttrs := make(map[string]string)
	responseAttrs["SubscriptionArn"] = subscriptionArn
	responseAttrs["TopicArn"] = topicArn
	responseAttrs["Protocol"] = protocol
	responseAttrs["Endpoint"] = endpoint
	responseAttrs["Owner"] = snsAccount
	responseAttrs["ConfirmationWasAuthenticated"] = "true"
	responseAttrs["PendingConfirmation"] = "false"

	// Convert map to slice of AttributeEntry
	var entries []AttributeEntry
	for k, v := range responseAttrs {
		entries = append(entries, AttributeEntry{
			Key:   k,
			Value: v,
		})
	}

	resp := GetSubscriptionAttributesResponse{
		GetSubscriptionAttributesResult: GetSubscriptionAttributesResult{
			Attributes: Attributes{
				Entries: entries,
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// ListSubscriptionsByTopic
func (h *Handler) ListSubscriptionsByTopic(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	topicArn := r.FormValue("TopicArn")
	if topicArn == "" {
		topicArn = r.URL.Query().Get("TopicArn")
	}

	if topicArn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"TopicArn is required",
			"",
		)
	}

	// List all subscriptions in the namespace
	items, err := h.Store.List("sns", "subscription", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, err.Error())
	}

	// Filter subscriptions by topic_arn and build response
	var subscriptions []Subscription
	for _, item := range items {
		// Parse stored attributes
		var storedAttrs map[string]interface{}
		if err := json.Unmarshal(item.Attributes, &storedAttrs); err != nil {
			continue
		}

		// Check if this subscription belongs to the requested topic
		storedTopicArn := ""
		if ta, ok := storedAttrs["topic_arn"].(string); ok {
			storedTopicArn = ta
		}

		if storedTopicArn != topicArn {
			continue
		}

		// Extract subscription details
		protocol := ""
		if p, ok := storedAttrs["protocol"].(string); ok {
			protocol = p
		}
		endpoint := ""
		if e, ok := storedAttrs["endpoint"].(string); ok {
			endpoint = e
		}

		// Build subscription ARN
		subscriptionArn := subscriptionArn(item.ID)

		subscriptions = append(subscriptions, Subscription{
			SubscriptionArn: subscriptionArn,
			TopicArn:        topicArn,
			Protocol:        protocol,
			Endpoint:        endpoint,
			Owner:           snsAccount,
		})
	}

	resp := ListSubscriptionsByTopicResponse{
		ListSubscriptionsByTopicResult: ListSubscriptionsByTopicResult{
			Subscriptions: Subscriptions{
				Subscriptions: subscriptions,
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// Unsubscribe
func (h *Handler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	subscriptionArn := r.FormValue("SubscriptionArn")
	if subscriptionArn == "" {
		subscriptionArn = r.URL.Query().Get("SubscriptionArn")
	}

	if subscriptionArn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"SubscriptionArn is required",
			"",
		)
	}

	// Extract subscription ID from ARN
	// Format: arn:aws:sns:region:account:subscription-id
	parts := strings.Split(subscriptionArn, ":")
	if len(parts) < 6 {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidParameter",
			"Invalid SubscriptionArn format",
			subscriptionArn,
		)
	}
	subscriptionID := parts[len(parts)-1]

	// AWS allows idempotent unsubscribe - it's OK if the subscription doesn't exist
	_ = h.Store.Delete(subscriptionID, "sns", "subscription", ns)

	resp := UnsubscribeResponse{
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}
