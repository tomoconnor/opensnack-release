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

	"opensnack/internal/resource"
	"opensnack/internal/util"
)

//
// Metric filters
//

func (h *Handler) metricFilters(scope, group string) []MetricFilter {
	items, _ := h.Store.List(service, typeMetricFilter, scope)
	var out []MetricFilter
	for _, it := range items {
		var mf MetricFilter
		if json.Unmarshal(it.Attributes, &mf) == nil && (group == "" || mf.LogGroupName == group) {
			out = append(out, mf)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LogGroupName != out[j].LogGroupName {
			return out[i].LogGroupName < out[j].LogGroupName
		}
		return out[i].FilterName < out[j].FilterName
	})
	return out
}

func (h *Handler) PutMetricFilter(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req MetricFilter
	if !decode(w, r, &req) {
		return
	}
	if req.FilterName == "" || strings.ContainsAny(req.FilterName, ":*") {
		writeErr(w, "InvalidParameterException", "Invalid filterName: "+req.FilterName)
		return
	}
	if len(req.MetricTransformations) != 1 {
		writeErr(w, "InvalidParameterException", "1 validation error detected: Value at 'metricTransformations' failed to satisfy constraint: Member must have length 1")
		return
	}
	for _, mt := range req.MetricTransformations {
		if mt.MetricName == "" || mt.MetricNamespace == "" || mt.MetricValue == "" {
			writeErr(w, "InvalidParameterException", "metricName, metricNamespace and metricValue are required")
			return
		}
		if mt.DefaultValue != nil && len(mt.Dimensions) > 0 {
			writeErr(w, "InvalidParameterException", "A metric transformation cannot have both a default value and dimensions.")
			return
		}
	}
	if _, err := ParsePattern(req.FilterPattern); err != nil {
		writeErr(w, "InvalidParameterException", "Invalid metric filter pattern: "+err.Error())
		return
	}
	if _, _, err := h.getGroup(scope, req.LogGroupName); err != nil {
		groupNotFound(w)
		return
	}
	id := filterID(req.LogGroupName, req.FilterName)
	existing, err := h.Store.Get(id, service, typeMetricFilter, scope)
	if err == nil {
		var old MetricFilter
		json.Unmarshal(existing.Attributes, &old)
		req.CreationTime = old.CreationTime
		buf, _ := json.Marshal(req)
		existing.Attributes = buf
		err = h.Store.Update(existing)
	} else {
		if len(h.metricFilters(scope, req.LogGroupName)) >= 100 {
			writeErr(w, "LimitExceededException", "Resource limit exceeded.")
			return
		}
		req.CreationTime = time.Now().UnixMilli()
		buf, _ := json.Marshal(req)
		err = h.Store.Create(&resource.Resource{ID: id, Namespace: scope, Service: service, Type: typeMetricFilter, Attributes: buf})
	}
	if err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) DescribeMetricFilters(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName     string `json:"logGroupName"`
		FilterNamePrefix string `json:"filterNamePrefix"`
		MetricName       string `json:"metricName"`
		MetricNamespace  string `json:"metricNamespace"`
		Limit            int    `json:"limit"`
		NextToken        string `json:"nextToken"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.FilterNamePrefix != "" && req.LogGroupName == "" {
		writeErr(w, "InvalidParameterException", "logGroupName is required with filterNamePrefix")
		return
	}
	if req.LogGroupName != "" {
		if _, _, err := h.getGroup(scope, req.LogGroupName); err != nil {
			groupNotFound(w)
			return
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	out := []MetricFilter{}
	next := ""
	for _, mf := range h.metricFilters(scope, req.LogGroupName) {
		key := filterID(mf.LogGroupName, mf.FilterName)
		if req.NextToken != "" && key <= req.NextToken {
			continue
		}
		if !strings.HasPrefix(mf.FilterName, req.FilterNamePrefix) {
			continue
		}
		if req.MetricName != "" || req.MetricNamespace != "" {
			hit := false
			for _, mt := range mf.MetricTransformations {
				if (req.MetricName == "" || mt.MetricName == req.MetricName) && (req.MetricNamespace == "" || mt.MetricNamespace == req.MetricNamespace) {
					hit = true
				}
			}
			if !hit {
				continue
			}
		}
		if len(out) == limit {
			last := out[len(out)-1]
			next = filterID(last.LogGroupName, last.FilterName)
			break
		}
		out = append(out, mf)
	}
	resp := map[string]any{"metricFilters": out}
	if next != "" {
		resp["nextToken"] = next
	}
	writeOK(w, resp)
}

func (h *Handler) DeleteMetricFilter(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName string `json:"logGroupName"`
		FilterName   string `json:"filterName"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, _, err := h.getGroup(scope, req.LogGroupName); err != nil {
		groupNotFound(w)
		return
	}
	id := filterID(req.LogGroupName, req.FilterName)
	if _, err := h.Store.Get(id, service, typeMetricFilter, scope); err != nil {
		writeErr(w, "ResourceNotFoundException", "The specified metric filter does not exist.")
		return
	}
	h.Store.Delete(id, service, typeMetricFilter, scope)
	writeOK(w, nil)
}

// TestMetricFilter runs a pattern over sample messages, which makes it a
// handy way to debug a pattern without writing any events.
func (h *Handler) TestMetricFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FilterPattern    string   `json:"filterPattern"`
		LogEventMessages []string `json:"logEventMessages"`
	}
	if !decode(w, r, &req) {
		return
	}
	pat, err := ParsePattern(req.FilterPattern)
	if err != nil {
		writeErr(w, "InvalidParameterException", "Invalid metric filter pattern: "+err.Error())
		return
	}
	matches := []map[string]any{}
	for i, msg := range req.LogEventMessages {
		ok, ex := pat.Match(msg)
		if !ok {
			continue
		}
		extracted := map[string]string{}
		for k, v := range ex.named {
			extracted["$"+k] = v
		}
		matches = append(matches, map[string]any{
			"eventNumber":     i + 1,
			"eventMessage":    msg,
			"extractedValues": extracted,
		})
	}
	writeOK(w, map[string]any{"matches": matches})
}

//
// Subscription filters (stored; nothing is delivered to the destination)
//

func (h *Handler) subscriptionFilters(scope, group string) []SubscriptionFilter {
	items, _ := h.Store.List(service, typeSubscriptionFil, scope)
	var out []SubscriptionFilter
	for _, it := range items {
		var sf SubscriptionFilter
		if json.Unmarshal(it.Attributes, &sf) == nil && sf.LogGroupName == group {
			out = append(out, sf)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FilterName < out[j].FilterName })
	return out
}

func (h *Handler) PutSubscriptionFilter(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req SubscriptionFilter
	if !decode(w, r, &req) {
		return
	}
	if req.FilterName == "" || req.DestinationArn == "" {
		writeErr(w, "InvalidParameterException", "filterName and destinationArn are required")
		return
	}
	if _, err := ParsePattern(req.FilterPattern); err != nil {
		writeErr(w, "InvalidParameterException", "Invalid subscription filter pattern: "+err.Error())
		return
	}
	if req.Distribution == "" {
		req.Distribution = "ByLogStream"
	}
	if req.Distribution != "ByLogStream" && req.Distribution != "Random" {
		writeErr(w, "InvalidParameterException", "Invalid distribution: "+req.Distribution)
		return
	}
	if _, _, err := h.getGroup(scope, req.LogGroupName); err != nil {
		groupNotFound(w)
		return
	}
	id := filterID(req.LogGroupName, req.FilterName)
	existing, err := h.Store.Get(id, service, typeSubscriptionFil, scope)
	if err == nil {
		var old SubscriptionFilter
		json.Unmarshal(existing.Attributes, &old)
		req.CreationTime = old.CreationTime
		buf, _ := json.Marshal(req)
		existing.Attributes = buf
		err = h.Store.Update(existing)
	} else {
		if len(h.subscriptionFilters(scope, req.LogGroupName)) >= 2 {
			writeErr(w, "LimitExceededException", "Resource limit exceeded.")
			return
		}
		req.CreationTime = time.Now().UnixMilli()
		buf, _ := json.Marshal(req)
		err = h.Store.Create(&resource.Resource{ID: id, Namespace: scope, Service: service, Type: typeSubscriptionFil, Attributes: buf})
	}
	if err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) DescribeSubscriptionFilters(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName     string `json:"logGroupName"`
		FilterNamePrefix string `json:"filterNamePrefix"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, _, err := h.getGroup(scope, req.LogGroupName); err != nil {
		groupNotFound(w)
		return
	}
	out := []SubscriptionFilter{}
	for _, sf := range h.subscriptionFilters(scope, req.LogGroupName) {
		if strings.HasPrefix(sf.FilterName, req.FilterNamePrefix) {
			out = append(out, sf)
		}
	}
	writeOK(w, map[string]any{"subscriptionFilters": out})
}

func (h *Handler) DeleteSubscriptionFilter(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName string `json:"logGroupName"`
		FilterName   string `json:"filterName"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, _, err := h.getGroup(scope, req.LogGroupName); err != nil {
		groupNotFound(w)
		return
	}
	id := filterID(req.LogGroupName, req.FilterName)
	if _, err := h.Store.Get(id, service, typeSubscriptionFil, scope); err != nil {
		writeErr(w, "ResourceNotFoundException", "The specified subscription filter does not exist.")
		return
	}
	h.Store.Delete(id, service, typeSubscriptionFil, scope)
	writeOK(w, nil)
}
