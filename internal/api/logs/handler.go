// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	service             = "logs"
	typeGroup           = "log_group"
	typeStream          = "log_stream"
	typeMetricFilter    = "metric_filter"
	typeSubscriptionFil = "subscription_filter"
)

func LogGroupArn(r *http.Request, name string) string {
	return groupArn(util.Region(r), util.AccountID(r), name)
}

func LogStreamArn(r *http.Request, group, stream string) string {
	return streamArn(util.Region(r), util.AccountID(r), group, stream)
}

func groupArn(region, account, name string) string {
	return "arn:aws:logs:" + region + ":" + account + ":log-group:" + name + ":*"
}

func streamArn(region, account, group, stream string) string {
	return "arn:aws:logs:" + region + ":" + account + ":log-group:" + group + ":log-stream:" + stream
}

// streamID keys a stream in the store. Log group names can't contain ':',
// so the split is unambiguous.
func streamID(group, stream string) string { return group + ":" + stream }

func filterID(group, name string) string { return group + ":" + name }

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

func (h *Handler) events() EventStore { return EventStoreFor(h.Store) }

//
// Dispatcher: matches X-Amz-Target
//

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	op := strings.TrimPrefix(target, "Logs_20140328.")

	ops := map[string]http.HandlerFunc{
		"CreateLogGroup":                h.CreateLogGroup,
		"DescribeLogGroups":             h.DescribeLogGroups,
		"DeleteLogGroup":                h.DeleteLogGroup,
		"PutRetentionPolicy":            h.PutRetentionPolicy,
		"DeleteRetentionPolicy":         h.DeleteRetentionPolicy,
		"AssociateKmsKey":               h.AssociateKmsKey,
		"DisassociateKmsKey":            h.DisassociateKmsKey,
		"PutLogGroupDeletionProtection": h.PutLogGroupDeletionProtection,
		"CreateLogStream":               h.CreateLogStream,
		"DescribeLogStreams":            h.DescribeLogStreams,
		"DeleteLogStream":               h.DeleteLogStream,
		"PutLogEvents":                  h.PutLogEvents,
		"GetLogEvents":                  h.GetLogEvents,
		"FilterLogEvents":               h.FilterLogEvents,
		"ListTagsForResource":           h.ListTagsForResource,
		"TagResource":                   h.TagResource,
		"UntagResource":                 h.UntagResource,
		"ListTagsLogGroup":              h.ListTagsLogGroup,
		"TagLogGroup":                   h.TagLogGroup,
		"UntagLogGroup":                 h.UntagLogGroup,
		"PutMetricFilter":               h.PutMetricFilter,
		"DescribeMetricFilters":         h.DescribeMetricFilters,
		"DeleteMetricFilter":            h.DeleteMetricFilter,
		"TestMetricFilter":              h.TestMetricFilter,
		"PutSubscriptionFilter":         h.PutSubscriptionFilter,
		"DescribeSubscriptionFilters":   h.DescribeSubscriptionFilters,
		"DeleteSubscriptionFilter":      h.DeleteSubscriptionFilter,
	}
	if f, ok := ops[op]; ok && strings.HasPrefix(target, "Logs_20140328.") {
		f(w, r)
		return
	}
	writeErr(w, "UnknownOperationException", "Unknown CloudWatch Logs operation: "+target)
}

//
// Errors and request plumbing
//

func writeErr(w http.ResponseWriter, code, msg string) {
	awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{"__type": code, "message": msg})
}

func writeOK(w http.ResponseWriter, v any) {
	if v == nil {
		v = struct{}{}
	}
	awsresponses.WriteJSON(w, http.StatusOK, v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := util.DecodeAWSJSON(r, v); err != nil {
		writeErr(w, "SerializationException", err.Error())
		return false
	}
	return true
}

func groupNotFound(w http.ResponseWriter) {
	writeErr(w, "ResourceNotFoundException", "The specified log group does not exist.")
}

// groupNameFrom accepts a log group name or ARN (logGroupIdentifier).
func groupNameFrom(name, identifier string) string {
	if name != "" {
		return name
	}
	if i := strings.Index(identifier, ":log-group:"); i >= 0 {
		n := identifier[i+len(":log-group:"):]
		n = strings.TrimSuffix(n, ":*")
		if j := strings.Index(n, ":log-stream:"); j >= 0 {
			n = n[:j]
		}
		return n
	}
	return identifier
}

//
// Log groups
//

type logGroup struct {
	Name      string            `json:"group"`
	Arn       string            `json:"arn"`
	CreatedAt int64             `json:"created_at"`
	Tags      map[string]string `json:"tags"`
	Retention *int              `json:"retention_in_days,omitempty"`
	KmsKeyID  string            `json:"kms_key_id,omitempty"`
	Class     string            `json:"log_group_class,omitempty"`
	// DeletionProtection blocks DeleteLogGroup until it is turned off.
	DeletionProtection bool `json:"deletion_protection_enabled"`
}

func (h *Handler) getGroup(scope, name string) (*logGroup, *resource.Resource, error) {
	res, err := h.Store.Get(name, service, typeGroup, scope)
	if err != nil {
		return nil, nil, err
	}
	var g logGroup
	if err := json.Unmarshal(res.Attributes, &g); err != nil {
		return nil, nil, err
	}
	if g.Tags == nil {
		g.Tags = map[string]string{}
	}
	if g.Class == "" {
		g.Class = "STANDARD"
	}
	return &g, res, nil
}

func (h *Handler) saveGroup(res *resource.Resource, g *logGroup) error {
	buf, _ := json.Marshal(g)
	res.Attributes = buf
	return h.Store.Update(res)
}

func (h *Handler) createGroup(scope string, g *logGroup) error {
	buf, _ := json.Marshal(g)
	return h.Store.Create(&resource.Resource{
		ID: g.Name, Namespace: scope, Service: service, Type: typeGroup, Attributes: buf,
	})
}

var logGroupClasses = map[string]bool{"STANDARD": true, "INFREQUENT_ACCESS": true, "DELIVERY": true}

func (h *Handler) CreateLogGroup(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName              string            `json:"logGroupName"`
		KmsKeyID                  string            `json:"kmsKeyId"`
		Tags                      map[string]string `json:"tags"`
		LogGroupClass             string            `json:"logGroupClass"`
		DeletionProtectionEnabled bool              `json:"deletionProtectionEnabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.LogGroupName == "" {
		writeErr(w, "InvalidParameterException", "1 validation error detected: Value null at 'logGroupName' failed to satisfy constraint: Member must not be null")
		return
	}
	if strings.ContainsAny(req.LogGroupName, ":*") || len(req.LogGroupName) > 512 {
		writeErr(w, "InvalidParameterException", "Invalid log group name: "+req.LogGroupName)
		return
	}
	if req.LogGroupClass == "" {
		req.LogGroupClass = "STANDARD"
	}
	if !logGroupClasses[req.LogGroupClass] {
		writeErr(w, "InvalidParameterException", "Invalid logGroupClass: "+req.LogGroupClass)
		return
	}
	if _, _, err := h.getGroup(scope, req.LogGroupName); err == nil {
		writeErr(w, "ResourceAlreadyExistsException", "The specified log group already exists")
		return
	}
	if req.Tags == nil {
		req.Tags = map[string]string{}
	}
	g := &logGroup{
		Name:               req.LogGroupName,
		Arn:                LogGroupArn(r, req.LogGroupName),
		CreatedAt:          time.Now().UnixMilli(),
		Tags:               req.Tags,
		KmsKeyID:           req.KmsKeyID,
		Class:              req.LogGroupClass,
		DeletionProtection: req.DeletionProtectionEnabled,
	}
	if g.Class == "DELIVERY" {
		// AWS fixes delivery-class retention at two days.
		two := 2
		g.Retention = &two
	}
	if err := h.createGroup(scope, g); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) DescribeLogGroups(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupNamePrefix  string   `json:"logGroupNamePrefix"`
		LogGroupNamePattern string   `json:"logGroupNamePattern"`
		LogGroupIdentifiers []string `json:"logGroupIdentifiers"`
		LogGroupClass       string   `json:"logGroupClass"`
		Limit               int      `json:"limit"`
		NextToken           string   `json:"nextToken"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.LogGroupNamePrefix != "" && req.LogGroupNamePattern != "" {
		writeErr(w, "InvalidParameterException", "logGroupNamePrefix and logGroupNamePattern are mutually exclusive")
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	ids := map[string]bool{}
	for _, id := range req.LogGroupIdentifiers {
		ids[groupNameFrom("", id)] = true
	}

	items, _ := h.Store.List(service, typeGroup, scope)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })

	filters, _ := h.Store.List(service, typeMetricFilter, scope)
	filterCount := map[string]int{}
	for _, f := range filters {
		var mf struct {
			LogGroupName string `json:"logGroupName"`
		}
		json.Unmarshal(f.Attributes, &mf)
		filterCount[mf.LogGroupName]++
	}

	out := []map[string]any{}
	next := ""
	for _, it := range items {
		name := it.ID
		if req.NextToken != "" && name <= req.NextToken {
			continue
		}
		if req.LogGroupNamePrefix != "" && !strings.HasPrefix(name, req.LogGroupNamePrefix) {
			continue
		}
		if req.LogGroupNamePattern != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(req.LogGroupNamePattern)) {
			continue
		}
		if len(ids) > 0 && !ids[name] {
			continue
		}
		g, _, err := h.getGroup(scope, name)
		if err != nil {
			continue
		}
		if req.LogGroupClass != "" && g.Class != req.LogGroupClass {
			continue
		}
		if len(out) == limit {
			next = out[len(out)-1]["logGroupName"].(string)
			break
		}
		out = append(out, h.describeGroup(g, filterCount[name]))
	}
	resp := map[string]any{"logGroups": out}
	if next != "" {
		resp["nextToken"] = next
	}
	writeOK(w, resp)
}

func (h *Handler) describeGroup(g *logGroup, metricFilters int) map[string]any {
	m := map[string]any{
		"logGroupName":              g.Name,
		"creationTime":              g.CreatedAt,
		"metricFilterCount":         metricFilters,
		"arn":                       g.Arn,
		"logGroupArn":               strings.TrimSuffix(g.Arn, ":*"),
		"storedBytes":               0,
		"logGroupClass":             g.Class,
		"deletionProtectionEnabled": g.DeletionProtection,
	}
	if g.Retention != nil {
		m["retentionInDays"] = *g.Retention
	}
	if g.KmsKeyID != "" {
		m["kmsKeyId"] = g.KmsKeyID
	}
	return m
}

func (h *Handler) DeleteLogGroup(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName string `json:"logGroupName"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, _, err := h.getGroup(scope, req.LogGroupName)
	if err != nil {
		groupNotFound(w)
		return
	}
	if g.DeletionProtection {
		writeErr(w, "InvalidOperationException", "Log group "+g.Name+" has deletion protection enabled")
		return
	}
	h.deleteGroupChildren(scope, g.Name)
	if err := h.Store.Delete(g.Name, service, typeGroup, scope); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) deleteGroupChildren(scope, group string) {
	for _, typ := range []string{typeStream, typeMetricFilter, typeSubscriptionFil} {
		items, _ := h.Store.List(service, typ, scope)
		for _, it := range items {
			if strings.HasPrefix(it.ID, group+":") {
				h.Store.Delete(it.ID, service, typ, scope)
			}
		}
	}
	h.events().DeleteGroup(scope, group)
}

var retentionDays = map[int]bool{
	1: true, 3: true, 5: true, 7: true, 14: true, 30: true, 60: true, 90: true, 120: true,
	150: true, 180: true, 365: true, 400: true, 545: true, 731: true, 1096: true, 1827: true,
	2192: true, 2557: true, 2922: true, 3288: true, 3653: true,
}

func (h *Handler) PutRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName    string `json:"logGroupName"`
		RetentionInDays int    `json:"retentionInDays"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !retentionDays[req.RetentionInDays] {
		writeErr(w, "InvalidParameterException", "1 validation error detected: Value at 'retentionInDays' failed to satisfy constraint: Member must satisfy enum value set")
		return
	}
	g, res, err := h.getGroup(scope, req.LogGroupName)
	if err != nil {
		groupNotFound(w)
		return
	}
	if g.Class == "DELIVERY" {
		writeErr(w, "InvalidParameterException", "Retention policy cannot be changed for a DELIVERY log group")
		return
	}
	days := req.RetentionInDays
	g.Retention = &days
	if err := h.saveGroup(res, g); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) DeleteRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName string `json:"logGroupName"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, res, err := h.getGroup(scope, req.LogGroupName)
	if err != nil {
		groupNotFound(w)
		return
	}
	g.Retention = nil
	if err := h.saveGroup(res, g); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) AssociateKmsKey(w http.ResponseWriter, r *http.Request) {
	h.setKmsKey(w, r, true)
}

func (h *Handler) DisassociateKmsKey(w http.ResponseWriter, r *http.Request) {
	h.setKmsKey(w, r, false)
}

func (h *Handler) setKmsKey(w http.ResponseWriter, r *http.Request, associate bool) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName       string `json:"logGroupName"`
		KmsKeyID           string `json:"kmsKeyId"`
		ResourceIdentifier string `json:"resourceIdentifier"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.LogGroupName == "" && req.ResourceIdentifier != "" {
		if !strings.Contains(req.ResourceIdentifier, ":log-group:") {
			// Query-result associations aren't modelled; accept and ignore.
			writeOK(w, nil)
			return
		}
		req.LogGroupName = groupNameFrom("", req.ResourceIdentifier)
	}
	if associate && req.KmsKeyID == "" {
		writeErr(w, "InvalidParameterException", "kmsKeyId is required")
		return
	}
	g, res, err := h.getGroup(scope, req.LogGroupName)
	if err != nil {
		groupNotFound(w)
		return
	}
	g.KmsKeyID = ""
	if associate {
		g.KmsKeyID = req.KmsKeyID
	}
	if err := h.saveGroup(res, g); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) PutLogGroupDeletionProtection(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupIdentifier        string `json:"logGroupIdentifier"`
		DeletionProtectionEnabled bool   `json:"deletionProtectionEnabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, res, err := h.getGroup(scope, groupNameFrom("", req.LogGroupIdentifier))
	if err != nil {
		groupNotFound(w)
		return
	}
	g.DeletionProtection = req.DeletionProtectionEnabled
	if err := h.saveGroup(res, g); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

//
// Log streams
//

type logStream struct {
	Group     string `json:"group"`
	Stream    string `json:"stream"`
	Arn       string `json:"arn"`
	CreatedAt int64  `json:"created_at"`
}

func (h *Handler) getStream(scope, group, stream string) (*logStream, error) {
	res, err := h.Store.Get(streamID(group, stream), service, typeStream, scope)
	if err != nil {
		return nil, err
	}
	var s logStream
	return &s, json.Unmarshal(res.Attributes, &s)
}

func (h *Handler) createStream(scope string, s *logStream) error {
	buf, _ := json.Marshal(s)
	return h.Store.Create(&resource.Resource{
		ID: streamID(s.Group, s.Stream), Namespace: scope, Service: service, Type: typeStream, Attributes: buf,
	})
}

func (h *Handler) CreateLogStream(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req CreateLogStreamRequest
	if !decode(w, r, &req) {
		return
	}
	if req.LogStreamName == "" || strings.ContainsAny(req.LogStreamName, ":*") {
		writeErr(w, "InvalidParameterException", "Invalid log stream name: "+req.LogStreamName)
		return
	}
	if _, _, err := h.getGroup(scope, req.LogGroupName); err != nil {
		groupNotFound(w)
		return
	}
	if _, err := h.getStream(scope, req.LogGroupName, req.LogStreamName); err == nil {
		writeErr(w, "ResourceAlreadyExistsException", "The specified log stream already exists")
		return
	}
	s := &logStream{
		Group:     req.LogGroupName,
		Stream:    req.LogStreamName,
		Arn:       LogStreamArn(r, req.LogGroupName, req.LogStreamName),
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := h.createStream(scope, s); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) listStreams(scope, group string) []logStream {
	items, _ := h.Store.List(service, typeStream, scope)
	var out []logStream
	for _, it := range items {
		var s logStream
		if json.Unmarshal(it.Attributes, &s) == nil && s.Group == group {
			out = append(out, s)
		}
	}
	return out
}

func (h *Handler) DescribeLogStreams(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName        string `json:"logGroupName"`
		LogGroupIdentifier  string `json:"logGroupIdentifier"`
		LogStreamNamePrefix string `json:"logStreamNamePrefix"`
		OrderBy             string `json:"orderBy"`
		Descending          bool   `json:"descending"`
		Limit               int    `json:"limit"`
		NextToken           string `json:"nextToken"`
	}
	if !decode(w, r, &req) {
		return
	}
	group := groupNameFrom(req.LogGroupName, req.LogGroupIdentifier)
	if _, _, err := h.getGroup(scope, group); err != nil {
		groupNotFound(w)
		return
	}
	if req.OrderBy == "LastEventTime" && req.LogStreamNamePrefix != "" {
		writeErr(w, "InvalidParameterException", "Cannot order by LastEventTime with a logStreamNamePrefix.")
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	stats, _ := h.events().Stats(scope, group)

	var streams []logStream
	for _, s := range h.listStreams(scope, group) {
		if strings.HasPrefix(s.Stream, req.LogStreamNamePrefix) {
			streams = append(streams, s)
		}
	}
	sort.Slice(streams, func(i, j int) bool {
		a, b := streams[i], streams[j]
		var less bool
		if req.OrderBy == "LastEventTime" && stats[a.Stream].Last != stats[b.Stream].Last {
			less = stats[a.Stream].Last < stats[b.Stream].Last
		} else {
			less = a.Stream < b.Stream
		}
		if req.Descending {
			return !less
		}
		return less
	})

	start := 0
	if req.NextToken != "" {
		for i, s := range streams {
			if s.Stream == req.NextToken {
				start = i + 1
				break
			}
		}
	}
	out := []map[string]any{}
	next := ""
	for _, s := range streams[start:] {
		if len(out) == limit {
			next = out[len(out)-1]["logStreamName"].(string)
			break
		}
		m := map[string]any{
			"logStreamName": s.Stream,
			"creationTime":  s.CreatedAt,
			"arn":           s.Arn,
			"storedBytes":   0,
		}
		if st, ok := stats[s.Stream]; ok {
			m["firstEventTimestamp"] = st.First
			m["lastEventTimestamp"] = st.Last
			m["lastIngestionTime"] = st.LastIngestion
			m["uploadSequenceToken"] = sequenceToken(st.Count)
		}
		out = append(out, m)
	}
	resp := map[string]any{"logStreams": out}
	if next != "" {
		resp["nextToken"] = next
	}
	writeOK(w, resp)
}

func (h *Handler) DeleteLogStream(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, _, err := h.getGroup(scope, req.LogGroupName); err != nil {
		groupNotFound(w)
		return
	}
	if _, err := h.getStream(scope, req.LogGroupName, req.LogStreamName); err != nil {
		writeErr(w, "ResourceNotFoundException", "The specified log stream does not exist.")
		return
	}
	h.Store.Delete(streamID(req.LogGroupName, req.LogStreamName), service, typeStream, scope)
	h.events().DeleteStream(scope, req.LogGroupName, req.LogStreamName)
	writeOK(w, nil)
}

//
// Tags
//

// groupFromArn resolves a tagging ARN to a log group. Only log groups (and
// destinations, which opensnack doesn't model) are taggable.
func (h *Handler) tagTarget(w http.ResponseWriter, scope, arn string) (*logGroup, *resource.Resource, bool) {
	if arn == "" {
		writeErr(w, "InvalidParameterException", "resourceArn is required")
		return nil, nil, false
	}
	if !strings.Contains(arn, ":log-group:") || strings.Contains(arn, ":log-stream:") {
		writeErr(w, "InvalidParameterException", "Invalid resource ARN: "+arn)
		return nil, nil, false
	}
	g, res, err := h.getGroup(scope, groupNameFrom("", arn))
	if err != nil {
		writeErr(w, "ResourceNotFoundException", "The specified resource does not exist.")
		return nil, nil, false
	}
	return g, res, true
}

func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, _, ok := h.tagTarget(w, scope, req.ResourceArn)
	if !ok {
		return
	}
	writeOK(w, map[string]any{"tags": g.Tags})
}

func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		ResourceArn string            `json:"resourceArn"`
		Tags        map[string]string `json:"tags"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, res, ok := h.tagTarget(w, scope, req.ResourceArn)
	if !ok {
		return
	}
	h.applyTags(w, res, g, req.Tags, nil)
}

func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, res, ok := h.tagTarget(w, scope, req.ResourceArn)
	if !ok {
		return
	}
	h.applyTags(w, res, g, nil, req.TagKeys)
}

// The pre-2023 tagging API, addressed by log group name.

func (h *Handler) ListTagsLogGroup(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName string `json:"logGroupName"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, _, err := h.getGroup(scope, req.LogGroupName)
	if err != nil {
		groupNotFound(w)
		return
	}
	writeOK(w, map[string]any{"tags": g.Tags})
}

func (h *Handler) TagLogGroup(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName string            `json:"logGroupName"`
		Tags         map[string]string `json:"tags"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, res, err := h.getGroup(scope, req.LogGroupName)
	if err != nil {
		groupNotFound(w)
		return
	}
	h.applyTags(w, res, g, req.Tags, nil)
}

func (h *Handler) UntagLogGroup(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName string   `json:"logGroupName"`
		Tags         []string `json:"tags"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, res, err := h.getGroup(scope, req.LogGroupName)
	if err != nil {
		groupNotFound(w)
		return
	}
	h.applyTags(w, res, g, nil, req.Tags)
}

func (h *Handler) applyTags(w http.ResponseWriter, res *resource.Resource, g *logGroup, add map[string]string, remove []string) {
	for k, v := range add {
		g.Tags[k] = v
	}
	for _, k := range remove {
		delete(g.Tags, k)
	}
	if len(g.Tags) > 50 {
		writeErr(w, "TooManyTagsException", "A resource can have no more than 50 tags.")
		return
	}
	if err := h.saveGroup(res, g); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}
