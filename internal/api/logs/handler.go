// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	logsRegion  = "us-east-1"
	logsAccount = "000000000000"
)

func LogGroupArn(name string) string {
	return "arn:aws:logs:" + logsRegion + ":" + logsAccount + ":log-group:" + name + ":*"
}

func LogStreamArn(group, stream string) string {
	return "arn:aws:logs:" + logsRegion + ":" + logsAccount + ":log-group:" + group + ":log-stream:" + stream
}

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

//
// Dispatcher: matches X-Amz-Target
//

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")

	switch target {
	case "Logs_20140328.CreateLogGroup":
		h.CreateLogGroup(w, r)
	case "Logs_20140328.DescribeLogGroups":
		h.DescribeLogGroups(w, r)
	case "Logs_20140328.DeleteLogGroup":
		h.DeleteLogGroup(w, r)
	case "Logs_20140328.CreateLogStream":
		h.CreateLogStream(w, r)
	case "Logs_20140328.DescribeLogStreams":
		h.DescribeLogStreams(w, r)
	case "Logs_20140328.DeleteLogStream":
		h.DeleteLogStream(w, r)
	case "Logs_20140328.PutLogEvents":
		h.PutLogEvents(w, r)
	case "Logs_20140328.ListTagsForResource":
		h.ListTagsForResource(w, r)
	case "Logs_20140328.TagResource":
		h.TagResource(w, r)
	case "Logs_20140328.UntagResource":
		h.UntagResource(w, r)

	default:
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "UnknownOperationException",
			"message": "Unknown CloudWatch Logs operation: " + target,
		})
	}
}

//
// CreateLogGroup
//

func (h *Handler) CreateLogGroup(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var reqBody map[string]any
	if err := util.DecodeAWSJSON(r, &reqBody); err != nil {
		awsresponses.WriteJSON(w, 400, err.Error())
		return
	}

	logGroupName, _ := reqBody["logGroupName"].(string)
	if logGroupName == "" {
		awsresponses.WriteJSON(w, 400, "logGroupName required")
		return
	}

	// Idempotent
	if _, err := h.Store.Get(logGroupName, "logs", "log_group", ns); err == nil {
		w.WriteHeader(200)
		return
	}

	// Extract tags if provided
	tags, _ := reqBody["tags"].(map[string]any)
	if tags == nil {
		tags = make(map[string]any)
	}

	entry := map[string]any{
		"group":      logGroupName,
		"arn":        LogGroupArn(logGroupName),
		"tags":       tags,
		"created_at": time.Now().UnixMilli(),
	}

	buf, _ := json.Marshal(entry)
	err := h.Store.Create(&resource.Resource{
		ID:         logGroupName,
		Namespace:  ns,
		Service:    "logs",
		Type:       "log_group",
		Attributes: buf,
	})
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	w.WriteHeader(200)
}

//
// DescribeLogGroups
//

func (h *Handler) DescribeLogGroups(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	items, _ := h.Store.List("logs", "log_group", ns)

	resp := DescribeLogGroupsResponse{LogGroups: []LogGroupElement{}}

	for _, it := range items {
		var attr map[string]any
		json.Unmarshal(it.Attributes, &attr)

		resp.LogGroups = append(resp.LogGroups, LogGroupElement{
			LogGroupName: attr["group"].(string),
			Arn:          attr["arn"].(string),
			CreationTime: int64(attr["created_at"].(float64)),
		})
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// CreateLogStream
//

func (h *Handler) CreateLogStream(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req CreateLogStreamRequest
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, err.Error())
		return
	}

	if req.LogGroupName == "" || req.LogStreamName == "" {
		awsresponses.WriteJSON(w, 400, "logGroupName and logStreamName required")
		return
	}

	id := req.LogGroupName + "/" + req.LogStreamName

	entry := map[string]any{
		"group":      req.LogGroupName,
		"stream":     req.LogStreamName,
		"arn":        LogStreamArn(req.LogGroupName, req.LogStreamName),
		"created_at": time.Now().UnixMilli(),
	}

	buf, _ := json.Marshal(entry)
	err := h.Store.Create(&resource.Resource{
		ID:         id,
		Namespace:  ns,
		Service:    "logs",
		Type:       "log_stream",
		Attributes: buf,
	})
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	w.WriteHeader(200)
}

//
// DescribeLogStreams
//

func (h *Handler) DescribeLogStreams(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	// AWS sends body: {"logGroupName":"X"}
	var body struct {
		LogGroupName string `json:"logGroupName"`
	}
	util.DecodeAWSJSON(r, &body)

	items, _ := h.Store.List("logs", "log_stream", ns)

	resp := DescribeLogStreamsResponse{LogStreams: []LogStreamElement{}}

	for _, it := range items {
		var attr map[string]any
		json.Unmarshal(it.Attributes, &attr)

		// filter by log group
		if attr["group"].(string) != body.LogGroupName {
			continue
		}

		resp.LogStreams = append(resp.LogStreams, LogStreamElement{
			LogStreamName: attr["stream"].(string),
			Arn:           attr["arn"].(string),
			CreationTime:  int64(attr["created_at"].(float64)),
		})
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// PutLogEvents (stub)
// Terraform never reads the logs, so we accept and ignore.
//

func (h *Handler) PutLogEvents(w http.ResponseWriter, r *http.Request) {
	var req PutLogEventsRequest
	util.DecodeAWSJSON(r, &req)

	// Respond with empty token to satisfy AWS SDKs
	resp := PutLogEventsResponse{
		NextSequenceToken: "0",
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// DeleteLogGroup
//

func (h *Handler) DeleteLogGroup(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		LogGroupName string `json:"logGroupName"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, err.Error())
		return
	}

	if req.LogGroupName == "" {
		awsresponses.WriteJSON(w, 400, "logGroupName required")
		return
	}

	_ = h.Store.Delete(req.LogGroupName, "logs", "log_group", ns)

	w.WriteHeader(200)
}

//
// DeleteLogStream
//

func (h *Handler) DeleteLogStream(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, err.Error())
		return
	}

	if req.LogGroupName == "" || req.LogStreamName == "" {
		awsresponses.WriteJSON(w, 400, "logGroupName and logStreamName required")
		return
	}

	id := req.LogGroupName + "/" + req.LogStreamName
	_ = h.Store.Delete(id, "logs", "log_stream", ns)

	w.WriteHeader(200)
}

//
// ListTagsForResource
//

func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, err.Error())
		return
	}

	if req.ResourceArn == "" {
		awsresponses.WriteJSON(w, 400, "resourceArn required")
		return
	}

	// Extract log group name from ARN
	// Log group: arn:aws:logs:region:account:log-group:name:*
	// Log stream: arn:aws:logs:region:account:log-group:group:log-stream:stream
	// Note: name/group can contain colons, so we need to parse carefully

	var resourceID string
	var resourceType string

	if strings.Contains(req.ResourceArn, ":log-stream:") {
		// Log stream ARN: arn:aws:logs:region:account:log-group:group:log-stream:stream
		resourceType = "log_stream"
		// Find the log-stream part
		logStreamIdx := strings.Index(req.ResourceArn, ":log-stream:")
		logGroupIdx := strings.Index(req.ResourceArn, ":log-group:")
		if logGroupIdx == -1 || logStreamIdx == -1 {
			awsresponses.WriteJSON(w, 400, "Invalid log stream ARN format")
			return
		}
		groupName := req.ResourceArn[logGroupIdx+len(":log-group:") : logStreamIdx]
		streamName := req.ResourceArn[logStreamIdx+len(":log-stream:"):]
		resourceID = groupName + "/" + streamName
	} else {
		// Log group ARN: arn:aws:logs:region:account:log-group:name:*
		resourceType = "log_group"
		logGroupIdx := strings.Index(req.ResourceArn, ":log-group:")
		if logGroupIdx == -1 {
			awsresponses.WriteJSON(w, 400, "Invalid log group ARN format")
			return
		}
		// Everything after :log-group: up to :* or end
		namePart := req.ResourceArn[logGroupIdx+len(":log-group:"):]
		// Remove trailing :* if present
		if strings.HasSuffix(namePart, ":*") {
			namePart = namePart[:len(namePart)-2]
		}
		resourceID = namePart
	}

	res, err := h.Store.Get(resourceID, "logs", resourceType, ns)
	if err != nil {
		// Return empty tags if resource doesn't exist
		awsresponses.WriteJSON(w, 200, map[string]any{
			"tags": map[string]string{},
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	tags, _ := attr["tags"].(map[string]any)
	if tags == nil {
		tags = map[string]any{}
	}

	// Convert to string map for response
	tagMap := make(map[string]string)
	for k, v := range tags {
		if str, ok := v.(string); ok {
			tagMap[k] = str
		} else {
			tagMap[k] = fmt.Sprintf("%v", v)
		}
	}

	awsresponses.WriteJSON(w, 200, map[string]any{
		"tags": tagMap,
	})
}

//
// TagResource
//

func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		ResourceArn string            `json:"resourceArn"`
		Tags        map[string]string `json:"tags"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, err.Error())
		return
	}

	if req.ResourceArn == "" {
		awsresponses.WriteJSON(w, 400, "resourceArn required")
		return
	}

	// Extract resource ID from ARN (same logic as ListTagsForResource)
	var resourceID string
	var resourceType string

	if strings.Contains(req.ResourceArn, ":log-stream:") {
		resourceType = "log_stream"
		logStreamIdx := strings.Index(req.ResourceArn, ":log-stream:")
		logGroupIdx := strings.Index(req.ResourceArn, ":log-group:")
		if logGroupIdx == -1 || logStreamIdx == -1 {
			awsresponses.WriteJSON(w, 400, "Invalid log stream ARN format")
			return
		}
		groupName := req.ResourceArn[logGroupIdx+len(":log-group:") : logStreamIdx]
		streamName := req.ResourceArn[logStreamIdx+len(":log-stream:"):]
		resourceID = groupName + "/" + streamName
	} else {
		resourceType = "log_group"
		logGroupIdx := strings.Index(req.ResourceArn, ":log-group:")
		if logGroupIdx == -1 {
			awsresponses.WriteJSON(w, 400, "Invalid log group ARN format")
			return
		}
		namePart := req.ResourceArn[logGroupIdx+len(":log-group:"):]
		if strings.HasSuffix(namePart, ":*") {
			namePart = namePart[:len(namePart)-2]
		}
		resourceID = namePart
	}

	res, err := h.Store.Get(resourceID, "logs", resourceType, ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, "Resource not found")
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	// Merge new tags with existing tags
	existingTags, _ := attr["tags"].(map[string]any)
	if existingTags == nil {
		existingTags = make(map[string]any)
	}

	for k, v := range req.Tags {
		existingTags[k] = v
	}

	attr["tags"] = existingTags

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	w.WriteHeader(200)
}

//
// UntagResource
//

func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, err.Error())
		return
	}

	if req.ResourceArn == "" {
		awsresponses.WriteJSON(w, 400, "resourceArn required")
		return
	}

	// Extract resource ID from ARN
	var resourceID string
	var resourceType string

	if strings.Contains(req.ResourceArn, ":log-stream:") {
		resourceType = "log_stream"
		logStreamIdx := strings.Index(req.ResourceArn, ":log-stream:")
		logGroupIdx := strings.Index(req.ResourceArn, ":log-group:")
		if logGroupIdx == -1 || logStreamIdx == -1 {
			awsresponses.WriteJSON(w, 400, "Invalid log stream ARN format")
			return
		}
		groupName := req.ResourceArn[logGroupIdx+len(":log-group:") : logStreamIdx]
		streamName := req.ResourceArn[logStreamIdx+len(":log-stream:"):]
		resourceID = groupName + "/" + streamName
	} else {
		resourceType = "log_group"
		logGroupIdx := strings.Index(req.ResourceArn, ":log-group:")
		if logGroupIdx == -1 {
			awsresponses.WriteJSON(w, 400, "Invalid log group ARN format")
			return
		}
		namePart := req.ResourceArn[logGroupIdx+len(":log-group:"):]
		if strings.HasSuffix(namePart, ":*") {
			namePart = namePart[:len(namePart)-2]
		}
		resourceID = namePart
	}

	res, err := h.Store.Get(resourceID, "logs", resourceType, ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, "Resource not found")
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	// Remove specified tag keys
	existingTags, _ := attr["tags"].(map[string]any)
	if existingTags != nil {
		for _, key := range req.TagKeys {
			delete(existingTags, key)
		}
		attr["tags"] = existingTags
	}

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	w.WriteHeader(200)
}
