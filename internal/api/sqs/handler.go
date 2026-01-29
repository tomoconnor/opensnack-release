// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	APIVersion = "2012-11-05"
	// We’ll build the host dynamically, but keep a fallback.
	sqsBaseURL = "http://localhost:4566/000000000000/"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// Utility: build canonical SQS QueueUrl
func buildQueueURL(name string) string {
	return sqsBaseURL + name
}

// ─────────────────────────────────────────────────────────────
// Main entry point for SQS API
// Supports both Query API (XML) and JSON API formats
// ─────────────────────────────────────────────────────────────
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	// Check if this is a JSON API request (X-Amz-Target header)
	target := r.Header.Get("X-Amz-Target")
	if target != "" {
		h.dispatchJSONAPI(w, r, target)
		return
	}

	// Otherwise, it's a Query API request (Action parameter)
	r.ParseForm()
	action := r.FormValue("Action")
	if action == "" {
		action = r.URL.Query().Get("Action")
	}

	switch action {
	case "CreateQueue":
		h.CreateQueue(w, r)
	case "ListQueues":
		h.ListQueues(w, r)
	case "GetQueueUrl":
		h.GetQueueUrl(w, r)
	case "DeleteQueue":
		h.DeleteQueue(w, r)
	default:
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidAction",
			"Unknown SQS Action",
			action,
		)
	}
}

// dispatchJSONAPI handles JSON API format requests (X-Amz-Target header)
func (h *Handler) dispatchJSONAPI(w http.ResponseWriter, r *http.Request, target string) {
	switch target {
	case "AmazonSQS.CreateQueue":
		h.CreateQueueJSON(w, r)
	case "AmazonSQS.ListQueues":
		h.ListQueuesJSON(w, r)
	case "AmazonSQS.GetQueueUrl":
		h.GetQueueUrlJSON(w, r)
	case "AmazonSQS.GetQueueAttributes":
		h.GetQueueAttributesJSON(w, r)
	case "AmazonSQS.SetQueueAttributes":
		h.SetQueueAttributesJSON(w, r)
	case "AmazonSQS.ListQueueTags":
		h.ListQueueTagsJSON(w, r)
	case "AmazonSQS.DeleteQueue":
		h.DeleteQueueJSON(w, r)
	default:
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Unknown SQS operation: " + target,
		})
	}
}

// ─────────────────────────────────────────────────────────────
// CreateQueue
// ─────────────────────────────────────────────────────────────
func (h *Handler) CreateQueue(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	queueName := r.FormValue("QueueName")

	if queueName == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"QueueName is required",
			"",
		)
	}

	// Check if queue exists
	_, err := h.Store.Get(queueName, "sqs", "queue", ns)
	if err == nil {
		// AWS allows CreateQueue to be idempotent and return existing queue
		queueURL := buildQueueURL(queueName)

		resp := CreateQueueResponse{
			CreateQueueResult: CreateQueueResult{QueueUrl: queueURL},
			ResponseMetadata:  ResponseMetadata{RequestId: awsresponses.NextRequestID()},
		}
		awsresponses.WriteXML(w, resp)
	}

	// Create new queue in store
	entry := map[string]interface{}{
		"name":       queueName,
		"created_at": time.Now().UTC(),
	}

	buf, _ := json.Marshal(entry)

	res := &resource.Resource{
		ID:         queueName,
		Namespace:  ns,
		Service:    "sqs",
		Type:       "queue",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		awsresponses.WriteErrorXML(
			w,
			http.StatusInternalServerError,
			"InternalError",
			"Failed to create queue: "+err.Error(),
			"",
		)
	}

	queueURL := buildQueueURL(queueName)

	resp := CreateQueueResponse{
		CreateQueueResult: CreateQueueResult{QueueUrl: queueURL},
		ResponseMetadata:  ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

// ─────────────────────────────────────────────────────────────
// ListQueues
// ─────────────────────────────────────────────────────────────
func (h *Handler) ListQueues(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	items, err := h.Store.List("sqs", "queue", ns)
	if err != nil {
		awsresponses.WriteErrorXML(
			w,
			http.StatusInternalServerError,
			"InternalError",
			"Failed to list queues: "+err.Error(),
			"",
		)
	}

	var urls []string
	for _, item := range items {
		urls = append(urls, buildQueueURL(item.ID))
	}

	resp := ListQueuesResponse{
		ListQueuesResult: ListQueuesResult{
			QueueUrls: urls,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// ─────────────────────────────────────────────────────────────
// GetQueueUrl
// ─────────────────────────────────────────────────────────────
func (h *Handler) GetQueueUrl(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	qname := r.URL.Query().Get("QueueName")

	if qname == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"QueueName is required",
			"",
		)
	}

	_, err := h.Store.Get(qname, "sqs", "queue", ns)
	if err != nil {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"AWS.SimpleQueueService.NonExistentQueue",
			"The specified queue does not exist.",
			qname,
		)
	}

	queueURL := buildQueueURL(qname)

	resp := GetQueueUrlResponse{
		GetQueueUrlResult: GetQueueUrlResult{QueueUrl: queueURL},
		ResponseMetadata:  ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

// ─────────────────────────────────────────────────────────────
// DeleteQueue
// ─────────────────────────────────────────────────────────────
func (h *Handler) DeleteQueue(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	queueURL := r.URL.Query().Get("QueueUrl")

	u, err := url.Parse(queueURL)
	if err != nil || u.Path == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidParameterValue",
			"QueueUrl is invalid",
			queueURL,
		)
	}

	parts := strings.Split(u.Path, "/")
	queueName := parts[len(parts)-1]

	// AWS allows idempotent delete
	_ = h.Store.Delete(queueName, "sqs", "queue", ns)

	awsresponses.WriteEmpty200(w, nil)
	return
}

// ─────────────────────────────────────────────────────────────
// JSON API Handlers (X-Amz-Target format)
// ─────────────────────────────────────────────────────────────

func (h *Handler) CreateQueueJSON(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req CreateQueueJSONRequest
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.QueueName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingParameter",
			"message": "QueueName is required",
		})
	}

	// Check if queue exists (idempotent)
	_, err := h.Store.Get(req.QueueName, "sqs", "queue", ns)
	if err == nil {
		// Queue already exists, return existing queue URL
		queueURL := buildQueueURL(req.QueueName)
		awsresponses.WriteJSON(w, http.StatusOK, CreateQueueJSONResponse{
			QueueUrl: queueURL,
		})
		return
	}

	// Create new queue in store
	entry := map[string]interface{}{
		"name":       req.QueueName,
		"created_at": time.Now().UTC(),
	}

	// Store attributes if provided
	if req.Attributes != nil {
		entry["attributes"] = req.Attributes
	}

	buf, _ := json.Marshal(entry)

	res := &resource.Resource{
		ID:         req.QueueName,
		Namespace:  ns,
		Service:    "sqs",
		Type:       "queue",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalError",
			"message": "Failed to create queue: " + err.Error(),
		})
	}

	queueURL := buildQueueURL(req.QueueName)
	awsresponses.WriteJSON(w, http.StatusOK, CreateQueueJSONResponse{
		QueueUrl: queueURL,
	})
	return
}

func (h *Handler) ListQueuesJSON(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	items, err := h.Store.List("sqs", "queue", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalError",
			"message": "Failed to list queues: " + err.Error(),
		})
	}

	var urls []string
	for _, item := range items {
		urls = append(urls, buildQueueURL(item.ID))
	}

	awsresponses.WriteJSON(w, http.StatusOK, map[string]any{
		"QueueUrls": urls,
	})
	return
}

func (h *Handler) GetQueueUrlJSON(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		QueueName string `json:"QueueName"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.QueueName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingParameter",
			"message": "QueueName is required",
		})
	}

	_, err := h.Store.Get(req.QueueName, "sqs", "queue", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "AWS.SimpleQueueService.NonExistentQueue",
			"message": "The specified queue does not exist.",
		})
	}

	queueURL := buildQueueURL(req.QueueName)
	awsresponses.WriteJSON(w, http.StatusOK, map[string]any{
		"QueueUrl": queueURL,
	})
	return
}

func (h *Handler) GetQueueAttributesJSON(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		AttributeNames []string `json:"AttributeNames"`
		QueueUrl       string   `json:"QueueUrl"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.QueueUrl == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingParameter",
			"message": "QueueUrl is required",
		})
	}

	// Extract queue name from QueueUrl
	u, err := url.Parse(req.QueueUrl)
	if err != nil || u.Path == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "QueueUrl is invalid",
		})
	}

	parts := strings.Split(u.Path, "/")
	queueName := parts[len(parts)-1]

	// Get queue from store
	queue, err := h.Store.Get(queueName, "sqs", "queue", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "AWS.SimpleQueueService.NonExistentQueue",
			"message": "The specified queue does not exist.",
		})
	}

	// Parse stored attributes
	var storedAttrs map[string]interface{}
	if err := json.Unmarshal(queue.Attributes, &storedAttrs); err != nil {
		storedAttrs = make(map[string]interface{})
	}

	// Get stored queue attributes if they exist
	var queueAttrs map[string]string
	if attrs, ok := storedAttrs["attributes"].(map[string]interface{}); ok {
		queueAttrs = make(map[string]string)
		for k, v := range attrs {
			if str, ok := v.(string); ok {
				queueAttrs[k] = str
			}
		}
	} else if attrs, ok := storedAttrs["attributes"].(map[string]string); ok {
		queueAttrs = attrs
	} else {
		queueAttrs = make(map[string]string)
	}

	// Build response attributes
	responseAttrs := make(map[string]string)

	// Check if "All" is requested or if specific attributes are requested
	requestAll := false
	for _, name := range req.AttributeNames {
		if name == "All" {
			requestAll = true
			break
		}
	}

	// Helper to get attribute value with default
	getAttr := func(name, defaultValue string) string {
		if queueAttrs != nil {
			if val, ok := queueAttrs[name]; ok {
				return val
			}
		}
		return defaultValue
	}

	// Get created timestamp from stored attributes
	var createdTimestamp int64
	if createdAt, ok := storedAttrs["created_at"]; ok {
		switch v := createdAt.(type) {
		case string:
			// Try RFC3339 format first
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				createdTimestamp = t.Unix()
			} else if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				createdTimestamp = t.Unix()
			}
		case float64:
			// Handle Unix timestamp as number
			createdTimestamp = int64(v)
		}
	}
	if createdTimestamp == 0 {
		// Fallback to current time if we can't parse stored timestamp
		createdTimestamp = time.Now().Unix()
	}

	// Add requested attributes
	if requestAll {
		// Return all standard attributes
		responseAttrs["QueueArn"] = "arn:aws:sqs:us-east-1:000000000000:" + queueName
		responseAttrs["ApproximateNumberOfMessages"] = "0"
		responseAttrs["ApproximateNumberOfMessagesDelayed"] = "0"
		responseAttrs["ApproximateNumberOfMessagesNotVisible"] = "0"
		responseAttrs["CreatedTimestamp"] = fmt.Sprintf("%d", createdTimestamp)
		responseAttrs["LastModifiedTimestamp"] = fmt.Sprintf("%d", createdTimestamp)
		responseAttrs["VisibilityTimeout"] = getAttr("VisibilityTimeout", "30")
		responseAttrs["MaximumMessageSize"] = getAttr("MaximumMessageSize", "262144")
		responseAttrs["MessageRetentionPeriod"] = getAttr("MessageRetentionPeriod", "345600")
		responseAttrs["DelaySeconds"] = getAttr("DelaySeconds", "0")
		responseAttrs["ReceiveMessageWaitTimeSeconds"] = getAttr("ReceiveMessageWaitTimeSeconds", "0")

		// Include all custom attributes (Policy, RedriveAllowPolicy, etc.)
		// Only include attributes that are not standard SQS attributes and have non-empty values
		standardAttrs := map[string]bool{
			"QueueArn": true, "ApproximateNumberOfMessages": true, "ApproximateNumberOfMessagesDelayed": true,
			"ApproximateNumberOfMessagesNotVisible": true, "CreatedTimestamp": true, "LastModifiedTimestamp": true,
			"VisibilityTimeout": true, "MaximumMessageSize": true, "MessageRetentionPeriod": true,
			"DelaySeconds": true, "ReceiveMessageWaitTimeSeconds": true,
		}
		for k, v := range queueAttrs {
			// Only add if not already set (avoid overwriting standard attributes)
			// and if it's not a standard attribute
			// AWS SQS doesn't return attributes with empty string values
			if _, exists := responseAttrs[k]; !exists && !standardAttrs[k] && v != "" {
				responseAttrs[k] = v
			}
		}
	} else {
		// Return only requested attributes
		for _, name := range req.AttributeNames {
			switch name {
			case "QueueArn":
				responseAttrs["QueueArn"] = "arn:aws:sqs:us-east-1:000000000000:" + queueName
			case "ApproximateNumberOfMessages":
				responseAttrs["ApproximateNumberOfMessages"] = "0"
			case "ApproximateNumberOfMessagesDelayed":
				responseAttrs["ApproximateNumberOfMessagesDelayed"] = "0"
			case "ApproximateNumberOfMessagesNotVisible":
				responseAttrs["ApproximateNumberOfMessagesNotVisible"] = "0"
			case "CreatedTimestamp":
				responseAttrs["CreatedTimestamp"] = fmt.Sprintf("%d", createdTimestamp)
			case "LastModifiedTimestamp":
				responseAttrs["LastModifiedTimestamp"] = fmt.Sprintf("%d", createdTimestamp)
			case "VisibilityTimeout":
				responseAttrs["VisibilityTimeout"] = getAttr("VisibilityTimeout", "30")
			case "MaximumMessageSize":
				responseAttrs["MaximumMessageSize"] = getAttr("MaximumMessageSize", "262144")
			case "MessageRetentionPeriod":
				responseAttrs["MessageRetentionPeriod"] = getAttr("MessageRetentionPeriod", "345600")
			case "DelaySeconds":
				responseAttrs["DelaySeconds"] = getAttr("DelaySeconds", "0")
			case "ReceiveMessageWaitTimeSeconds":
				responseAttrs["ReceiveMessageWaitTimeSeconds"] = getAttr("ReceiveMessageWaitTimeSeconds", "0")
			default:
				// For any other attribute name (like Policy, RedriveAllowPolicy, etc.),
				// check if it exists in stored attributes
				// AWS SQS doesn't return attributes with empty string values
				if val, ok := queueAttrs[name]; ok && val != "" {
					responseAttrs[name] = val
				}
			}
		}
	}

	awsresponses.WriteJSON(w, http.StatusOK, map[string]any{
		"Attributes": responseAttrs,
	})
	return
}

func (h *Handler) SetQueueAttributesJSON(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		QueueUrl   string            `json:"QueueUrl"`
		Attributes map[string]string `json:"Attributes"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.QueueUrl == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingParameter",
			"message": "QueueUrl is required",
		})
	}

	if len(req.Attributes) == 0 {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingParameter",
			"message": "Attributes is required",
		})
	}

	// Extract queue name from QueueUrl
	u, err := url.Parse(req.QueueUrl)
	if err != nil || u.Path == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "QueueUrl is invalid",
		})
	}

	parts := strings.Split(u.Path, "/")
	queueName := parts[len(parts)-1]

	// Get queue from store
	queue, err := h.Store.Get(queueName, "sqs", "queue", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "AWS.SimpleQueueService.NonExistentQueue",
			"message": "The specified queue does not exist.",
		})
	}

	// Parse existing attributes
	var storedAttrs map[string]interface{}
	if err := json.Unmarshal(queue.Attributes, &storedAttrs); err != nil {
		storedAttrs = make(map[string]interface{})
	}

	// Get existing queue attributes (if any)
	var queueAttrs map[string]string
	if attrs, ok := storedAttrs["attributes"].(map[string]interface{}); ok {
		queueAttrs = make(map[string]string)
		for k, v := range attrs {
			if str, ok := v.(string); ok {
				queueAttrs[k] = str
			}
		}
	} else if attrs, ok := storedAttrs["attributes"].(map[string]string); ok {
		queueAttrs = attrs
	} else {
		queueAttrs = make(map[string]string)
	}

	// Merge new attributes with existing ones
	// If an attribute is set to empty string, remove it (AWS SQS behavior)
	for k, v := range req.Attributes {
		if v == "" {
			// Remove attribute if set to empty string
			delete(queueAttrs, k)
		} else {
			queueAttrs[k] = v
		}
	}

	// Update stored attributes
	storedAttrs["attributes"] = queueAttrs

	// Preserve other fields like name and created_at
	if _, ok := storedAttrs["name"]; !ok {
		storedAttrs["name"] = queueName
	}
	// Preserve created_at if it exists, otherwise set it
	if _, ok := storedAttrs["created_at"]; !ok {
		storedAttrs["created_at"] = time.Now().UTC()
	}

	// Marshal updated attributes
	buf, err := json.Marshal(storedAttrs)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalError",
			"message": "Failed to update queue attributes: " + err.Error(),
		})
	}

	// Update queue in store
	queue.Attributes = buf
	if err := h.Store.Update(queue); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalError",
			"message": "Failed to update queue: " + err.Error(),
		})
	}

	// AWS returns empty JSON object {} for SetQueueAttributes in JSON API format
	awsresponses.WriteJSON(w, http.StatusOK, struct{}{})
	return
}

func (h *Handler) ListQueueTagsJSON(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		QueueUrl string `json:"QueueUrl"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.QueueUrl == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingParameter",
			"message": "QueueUrl is required",
		})
	}

	// Extract queue name from QueueUrl
	u, err := url.Parse(req.QueueUrl)
	if err != nil || u.Path == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "QueueUrl is invalid",
		})
	}

	parts := strings.Split(u.Path, "/")
	queueName := parts[len(parts)-1]

	// Get queue from store
	queue, err := h.Store.Get(queueName, "sqs", "queue", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "AWS.SimpleQueueService.NonExistentQueue",
			"message": "The specified queue does not exist.",
		})
	}

	// Parse stored attributes
	var storedAttrs map[string]interface{}
	if err := json.Unmarshal(queue.Attributes, &storedAttrs); err != nil {
		storedAttrs = make(map[string]interface{})
	}

	// Extract tags from attributes
	tags := make(map[string]string)
	if tagMap, ok := storedAttrs["tags"].(map[string]interface{}); ok {
		for k, v := range tagMap {
			if str, ok := v.(string); ok {
				tags[k] = str
			} else {
				// Convert non-string values to string
				tags[k] = fmt.Sprintf("%v", v)
			}
		}
	}

	// AWS returns empty Tags object if no tags exist
	awsresponses.WriteJSON(w, http.StatusOK, map[string]any{
		"Tags": tags,
	})
	return
}

func (h *Handler) DeleteQueueJSON(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		QueueUrl string `json:"QueueUrl"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	u, err := url.Parse(req.QueueUrl)
	if err != nil || u.Path == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterValue",
			"message": "QueueUrl is invalid",
		})
	}

	parts := strings.Split(u.Path, "/")
	queueName := parts[len(parts)-1]

	// AWS allows idempotent delete
	_ = h.Store.Delete(queueName, "sqs", "queue", ns)

	// AWS returns empty JSON object {} for DeleteQueue in JSON API format
	awsresponses.WriteJSON(w, http.StatusOK, struct{}{})
	return
}
