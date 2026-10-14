// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cloudwatch emulates CloudWatch metrics and alarms.
//
// Alarms are resources (type "alarm", keyed by name) whose attributes are
// the AWS MetricAlarm / CompositeAlarm shape, stored as the client sent it so
// reads return exactly what was written. Metric data is kept in memory (see
// metrics.go). Metric alarms are evaluated when data for their metric
// arrives, when they are created or updated, and composite alarms when a
// child changes state; a state change runs the alarm's SNS actions through
// the Notifier the router wires in.
package cloudwatch

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	service   = "cloudwatch"
	typeAlarm = "alarm"

	kindMetric    = "MetricAlarm"
	kindComposite = "CompositeAlarm"

	stateOK           = "OK"
	stateAlarm        = "ALARM"
	stateInsufficient = "INSUFFICIENT_DATA"
)

type Handler struct {
	Store  resource.Store
	engine *Engine
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store, engine: EngineFor(store)}
}

// SetNotifier wires alarm actions to SNS.
func (h *Handler) SetNotifier(n Notifier) { h.engine.SetNotifier(n) }

type call struct {
	r     *http.Request
	in    map[string]any
	scope string
}

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	req, aerr := parseRequest(r)
	if aerr != nil {
		writeError(w, req.proto, aerr)
		return
	}
	ops := map[string]func(*call) (map[string]any, *apiError){
		"PutMetricAlarm":          h.putMetricAlarm,
		"PutCompositeAlarm":       h.putCompositeAlarm,
		"DescribeAlarms":          h.describeAlarms,
		"DescribeAlarmsForMetric": h.describeAlarmsForMetric,
		"DescribeAlarmHistory":    h.describeAlarmHistory,
		"DeleteAlarms":            h.deleteAlarms,
		"SetAlarmState":           h.setAlarmState,
		"EnableAlarmActions":      h.enableAlarmActions,
		"DisableAlarmActions":     h.disableAlarmActions,
		"TagResource":             h.tagResource,
		"UntagResource":           h.untagResource,
		"ListTagsForResource":     h.listTagsForResource,
		"PutMetricData":           h.putMetricData,
		"GetMetricStatistics":     h.getMetricStatistics,
		"GetMetricData":           h.getMetricData,
		"ListMetrics":             h.listMetrics,
	}
	op, ok := ops[req.op]
	if !ok {
		writeError(w, req.proto, &apiError{http.StatusBadRequest, "InvalidAction",
			"opensnack does not implement the CloudWatch operation " + req.op})
		return
	}
	out, aerr := op(&call{r: r, in: req.in, scope: util.NamespaceFromHeader(r)})
	if aerr != nil {
		writeError(w, req.proto, aerr)
		return
	}
	writeResult(w, req, out)
}

//
// Alarm storage
//

// alarm is a stored alarm: the AWS shape in Attrs, plus bookkeeping.
type alarm struct {
	Kind    string         `json:"kind"`
	Attrs   map[string]any `json:"alarm"`
	Tags    []tag          `json:"tags"`
	History []historyItem  `json:"history,omitempty"`
}

type tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type historyItem struct {
	Timestamp int64  `json:"Timestamp"`
	Type      string `json:"HistoryItemType"`
	Summary   string `json:"HistorySummary"`
	Data      string `json:"HistoryData"`
}

const maxHistory = 100

func (a *alarm) name() string { return a.Attrs["AlarmName"].(string) }

func (a *alarm) state() string { s, _ := a.Attrs["StateValue"].(string); return s }

func (a *alarm) addHistory(typ, summary, data string) {
	a.History = append(a.History, historyItem{time.Now().UnixMilli(), typ, summary, data})
	if len(a.History) > maxHistory {
		a.History = a.History[len(a.History)-maxHistory:]
	}
}

// Timestamps are stored as epoch milliseconds and turned back into times on
// the way out.
var timestampKeys = []string{"AlarmConfigurationUpdatedTimestamp", "StateUpdatedTimestamp", "StateTransitionedTimestamp"}

func (a *alarm) output() map[string]any {
	out := make(map[string]any, len(a.Attrs))
	for k, v := range a.Attrs {
		out[k] = v
	}
	for _, k := range timestampKeys {
		if ms, ok := toNum(out[k]); ok {
			out[k] = time.UnixMilli(int64(ms))
		}
	}
	return out
}

func (h *Handler) getAlarm(scope, name string) (*alarm, *resource.Resource) {
	return getAlarm(h.Store, scope, name)
}

func getAlarm(store resource.Store, scope, name string) (*alarm, *resource.Resource) {
	res, err := store.Get(name, service, typeAlarm, scope)
	if err != nil {
		return nil, nil
	}
	var a alarm
	if json.Unmarshal(res.Attributes, &a) != nil || a.Attrs == nil {
		return nil, nil
	}
	return &a, res
}

func listAlarms(store resource.Store, scope string) []*alarm {
	items, _ := store.List(service, typeAlarm, scope)
	out := make([]*alarm, 0, len(items))
	for _, it := range items {
		var a alarm
		if json.Unmarshal(it.Attributes, &a) == nil && a.Attrs != nil {
			out = append(out, &a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name() < out[j].name() })
	return out
}

func saveAlarm(store resource.Store, scope string, a *alarm, res *resource.Resource) error {
	buf, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if res == nil {
		return store.Create(&resource.Resource{ID: a.name(), Namespace: scope, Service: service, Type: typeAlarm, Attributes: buf})
	}
	res.Attributes = buf
	return store.Update(res)
}

func alarmArn(c *call, name string) string {
	return "arn:aws:cloudwatch:" + util.Region(c.r) + ":" + util.AccountID(c.r) + ":alarm:" + name
}

// Numeric and boolean members, so alarms written over Query (where every
// value is a string) read back with the right types over CBOR and JSON.
var numericKeys = map[string]bool{
	"Threshold": true, "EvaluationPeriods": true, "DatapointsToAlarm": true, "Period": true,
	"EvaluationInterval": true, "PendingPeriod": true, "RecoveryPeriod": true,
	"WarmUpPeriodDurationInMinutes": true, "ActionsSuppressorWaitPeriod": true,
	"ActionsSuppressorExtensionPeriod": true,
}

var boolKeys = map[string]bool{
	"ActionsEnabled": true, "ReturnData": true, "OnlyStartEvaluatingAfterWarmUpPeriodEnds": true,
}

func normalize(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = normalize(e, k)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = normalize(e, "")
		}
		return l
	case string:
		if numericKeys[key] {
			if f, ok := toNum(x); ok {
				return f
			}
		}
		if boolKeys[key] {
			if b, ok := boolean(map[string]any{key: x}, key); ok {
				return b
			}
		}
	}
	return v
}

func tagsFrom(in map[string]any) ([]tag, *apiError) {
	var out []tag
	for _, t := range list(in, "Tags") {
		m := obj(t)
		k := str(m, "Key")
		if k == "" {
			return nil, errInvalid("Tag keys must not be empty.")
		}
		out = append(out, tag{Key: k, Value: str(m, "Value")})
	}
	return out, nil
}

var comparisonOperators = map[string]bool{
	"GreaterThanOrEqualToThreshold": true, "GreaterThanThreshold": true,
	"LessThanThreshold": true, "LessThanOrEqualToThreshold": true,
	"LessThanLowerOrGreaterThanUpperThreshold": true, "LessThanLowerThreshold": true,
	"GreaterThanUpperThreshold": true,
}

var statistics = map[string]bool{"SampleCount": true, "Average": true, "Sum": true, "Minimum": true, "Maximum": true}

var missingData = map[string]bool{"breaching": true, "notBreaching": true, "ignore": true, "missing": true}

func validateMetricAlarm(in map[string]any) *apiError {
	if _, ok := in["EvaluationCriteria"]; ok {
		return nil
	}
	if str(in, "ComparisonOperator") == "" {
		return errMissing("ComparisonOperator")
	}
	if !comparisonOperators[str(in, "ComparisonOperator")] {
		return errInvalid("Invalid ComparisonOperator: %s", str(in, "ComparisonOperator"))
	}
	if n, ok := num(in, "EvaluationPeriods"); !ok || n < 1 {
		return errMissing("EvaluationPeriods")
	}
	if m, ok := num(in, "DatapointsToAlarm"); ok {
		n, _ := num(in, "EvaluationPeriods")
		if m < 1 || m > n {
			return errInvalid("DatapointsToAlarm must be between 1 and EvaluationPeriods.")
		}
	}
	metricName := str(in, "MetricName")
	_, hasMetrics := in["Metrics"]
	if metricName == "" && !hasMetrics {
		return errMissing("MetricName")
	}
	if metricName != "" && hasMetrics {
		return errCombination("MetricName and Metrics are mutually exclusive.")
	}
	if metricName != "" {
		if str(in, "Namespace") == "" {
			return errMissing("Namespace")
		}
		st, ext := str(in, "Statistic"), str(in, "ExtendedStatistic")
		if (st == "") == (ext == "") {
			return errCombination("Exactly one of Statistic and ExtendedStatistic must be specified.")
		}
		if st != "" && !statistics[st] {
			return errInvalid("Invalid Statistic: %s", st)
		}
		p, ok := num(in, "Period")
		if !ok {
			return errMissing("Period")
		}
		if !(p == 10 || p == 20 || p == 30 || (p >= 60 && int(p)%60 == 0)) {
			return errInvalid("The parameter Period must be 10, 20, 30 or a multiple of 60.")
		}
	}
	_, hasThreshold := num(in, "Threshold")
	if !hasThreshold && str(in, "ThresholdMetricId") == "" {
		return errMissing("Threshold")
	}
	if tmd := str(in, "TreatMissingData"); tmd != "" && !missingData[tmd] {
		return errInvalid("Invalid TreatMissingData: %s", tmd)
	}
	return nil
}

func (h *Handler) putMetricAlarm(c *call) (map[string]any, *apiError) {
	return h.putAlarm(c, kindMetric)
}

func (h *Handler) putCompositeAlarm(c *call) (map[string]any, *apiError) {
	return h.putAlarm(c, kindComposite)
}

func (h *Handler) putAlarm(c *call, kind string) (map[string]any, *apiError) {
	in := normalize(c.in, "").(map[string]any)
	name := str(in, "AlarmName")
	if name == "" {
		return nil, errMissing("AlarmName")
	}
	if len(name) > 255 {
		return nil, errInvalid("AlarmName must be at most 255 characters.")
	}
	switch kind {
	case kindMetric:
		if err := validateMetricAlarm(in); err != nil {
			return nil, err
		}
	case kindComposite:
		rule := str(in, "AlarmRule")
		if rule == "" {
			return nil, errMissing("AlarmRule")
		}
		if _, err := parseRule(rule); err != nil {
			return nil, errInvalid("Invalid AlarmRule: %v", err)
		}
	}
	tags, aerr := tagsFrom(in)
	if aerr != nil {
		return nil, aerr
	}

	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()

	existing, res := h.getAlarm(c.scope, name)
	if existing != nil && existing.Kind != kind {
		return nil, errInvalid("An alarm named %s of another type already exists.", name)
	}
	now := time.Now().UnixMilli()

	attrs := map[string]any{}
	for k, v := range in {
		if k != "Tags" {
			attrs[k] = v
		}
	}
	attrs["AlarmArn"] = alarmArn(c, name)
	if _, ok := attrs["ActionsEnabled"]; !ok {
		attrs["ActionsEnabled"] = true
	}
	for _, k := range []string{"OKActions", "AlarmActions", "InsufficientDataActions"} {
		if _, ok := attrs[k]; !ok {
			attrs[k] = []any{}
		}
	}
	if kind == kindMetric {
		if _, ok := attrs["Dimensions"]; !ok && str(attrs, "MetricName") != "" {
			attrs["Dimensions"] = []any{}
		}
	}
	attrs["AlarmConfigurationUpdatedTimestamp"] = now

	a := &alarm{Kind: kind, Attrs: attrs, Tags: tags}
	if existing != nil {
		// Tags in PutMetricAlarm only apply on create.
		a.Tags = existing.Tags
		a.History = existing.History
		for _, k := range []string{"StateValue", "StateReason", "StateReasonData", "StateUpdatedTimestamp", "StateTransitionedTimestamp"} {
			if v, ok := existing.Attrs[k]; ok {
				attrs[k] = v
			}
		}
		a.addHistory("ConfigurationUpdate", "Alarm \""+name+"\" updated", "")
	} else {
		attrs["StateValue"] = stateInsufficient
		attrs["StateReason"] = "Unchecked: Initial alarm creation"
		attrs["StateUpdatedTimestamp"] = now
		attrs["StateTransitionedTimestamp"] = now
		a.addHistory("ConfigurationUpdate", "Alarm \""+name+"\" created", "")
	}
	if err := saveAlarm(h.Store, c.scope, a, res); err != nil {
		return nil, &apiError{http.StatusInternalServerError, "InternalServiceError", err.Error()}
	}
	h.engine.evaluateLocked(c.scope, a)
	return nil, nil
}

func (h *Handler) describeAlarms(c *call) (map[string]any, *apiError) {
	names := strList(c.in, "AlarmNames")
	prefix := str(c.in, "AlarmNamePrefix")
	if len(names) > 0 && prefix != "" {
		return nil, errCombination("AlarmNames and AlarmNamePrefix are mutually exclusive.")
	}
	types := map[string]bool{}
	for _, t := range strList(c.in, "AlarmTypes") {
		types[t] = true
	}
	if len(types) == 0 {
		types[kindMetric] = true
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	stateValue := str(c.in, "StateValue")
	actionPrefix := str(c.in, "ActionPrefix")
	max := 50
	if m, ok := num(c.in, "MaxRecords"); ok && m >= 1 && m <= 100 {
		max = int(m)
	}
	nextToken := str(c.in, "NextToken")
	children, parents := str(c.in, "ChildrenOfAlarmName"), str(c.in, "ParentsOfAlarmName")

	all := listAlarms(h.Store, c.scope)
	var related map[string]bool
	if children != "" || parents != "" {
		related = map[string]bool{}
		for _, a := range all {
			if a.Kind == kindComposite && a.name() == children {
				if rule, err := parseRule(str(a.Attrs, "AlarmRule")); err == nil {
					for _, n := range rule.names() {
						related[n] = true
					}
				}
			}
			if a.Kind == kindComposite && parents != "" {
				if rule, err := parseRule(str(a.Attrs, "AlarmRule")); err == nil {
					for _, n := range rule.names() {
						if n == parents {
							related[a.name()] = true
						}
					}
				}
			}
		}
	}

	metric, composite := []any{}, []any{}
	count := 0
	next, lastName := "", ""
	for _, a := range all {
		n := a.name()
		if nextToken != "" && n <= nextToken {
			continue
		}
		if !types[a.Kind] || (len(want) > 0 && !want[n]) || !strings.HasPrefix(n, prefix) {
			continue
		}
		if related != nil && !related[n] {
			continue
		}
		if stateValue != "" && a.state() != stateValue {
			continue
		}
		if actionPrefix != "" && !hasActionPrefix(a, actionPrefix) {
			continue
		}
		if count == max {
			next = lastName
			break
		}
		lastName = n
		count++
		if a.Kind == kindMetric {
			metric = append(metric, a.output())
		} else {
			composite = append(composite, a.output())
		}
	}
	out := map[string]any{"MetricAlarms": metric, "CompositeAlarms": composite}
	if next != "" {
		out["NextToken"] = next
	}
	return out, nil
}

func hasActionPrefix(a *alarm, prefix string) bool {
	for _, k := range []string{"OKActions", "AlarmActions", "InsufficientDataActions"} {
		for _, act := range strList(a.Attrs, k) {
			if strings.HasPrefix(act, prefix) {
				return true
			}
		}
	}
	return false
}

func (h *Handler) describeAlarmsForMetric(c *call) (map[string]any, *apiError) {
	ns, name := str(c.in, "Namespace"), str(c.in, "MetricName")
	if ns == "" || name == "" {
		return nil, errMissing("Namespace and MetricName")
	}
	dims := dimensionsKey(dimensionsFrom(c.in))
	out := []any{}
	for _, a := range listAlarms(h.Store, c.scope) {
		if a.Kind != kindMetric || str(a.Attrs, "Namespace") != ns || str(a.Attrs, "MetricName") != name {
			continue
		}
		if _, ok := c.in["Dimensions"]; ok && dimensionsKey(dimensionsFrom(a.Attrs)) != dims {
			continue
		}
		if !sameOptional(c.in, a.Attrs, "Statistic", "ExtendedStatistic", "Unit") {
			continue
		}
		if p, ok := num(c.in, "Period"); ok {
			if ap, _ := num(a.Attrs, "Period"); ap != p {
				continue
			}
		}
		out = append(out, a.output())
	}
	return map[string]any{"MetricAlarms": out}, nil
}

// sameOptional reports whether every key set in the query matches the alarm.
func sameOptional(query, attrs map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v := str(query, k); v != "" && str(attrs, k) != v {
			return false
		}
	}
	return true
}

func (h *Handler) describeAlarmHistory(c *call) (map[string]any, *apiError) {
	name := str(c.in, "AlarmName")
	typeFilter := str(c.in, "HistoryItemType")
	start, hasStart := timestamp(c.in, "StartDate")
	end, hasEnd := timestamp(c.in, "EndDate")
	var items []map[string]any
	for _, a := range listAlarms(h.Store, c.scope) {
		if name != "" && a.name() != name {
			continue
		}
		for _, hi := range a.History {
			ts := time.UnixMilli(hi.Timestamp)
			if (typeFilter != "" && hi.Type != typeFilter) || (hasStart && ts.Before(start)) || (hasEnd && ts.After(end)) {
				continue
			}
			items = append(items, map[string]any{
				"AlarmName": a.name(), "AlarmType": a.Kind, "Timestamp": ts,
				"HistoryItemType": hi.Type, "HistorySummary": hi.Summary, "HistoryData": hi.Data,
			})
		}
	}
	asc := str(c.in, "ScanBy") == "TimestampAscending"
	sort.SliceStable(items, func(i, j int) bool {
		ti, tj := items[i]["Timestamp"].(time.Time), items[j]["Timestamp"].(time.Time)
		if asc {
			return ti.Before(tj)
		}
		return ti.After(tj)
	})
	if m, ok := num(c.in, "MaxRecords"); ok && m >= 1 && int(m) < len(items) {
		items = items[:int(m)]
	}
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"AlarmHistoryItems": items}, nil
}

func (h *Handler) deleteAlarms(c *call) (map[string]any, *apiError) {
	names := strList(c.in, "AlarmNames")
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	for _, n := range names {
		if a, _ := h.getAlarm(c.scope, n); a == nil {
			return nil, errNotFound("ResourceNotFound", "The following alarms could not be found: "+n)
		}
	}
	for _, n := range names {
		h.Store.Delete(n, service, typeAlarm, c.scope)
	}
	return nil, nil
}

func (h *Handler) setAlarmState(c *call) (map[string]any, *apiError) {
	name := str(c.in, "AlarmName")
	state := str(c.in, "StateValue")
	reason := str(c.in, "StateReason")
	if name == "" {
		return nil, errMissing("AlarmName")
	}
	if state != stateOK && state != stateAlarm && state != stateInsufficient {
		return nil, errInvalid("Invalid StateValue: %s", state)
	}
	if reason == "" {
		return nil, errMissing("StateReason")
	}
	if data := str(c.in, "StateReasonData"); data != "" && !json.Valid([]byte(data)) {
		return nil, &apiError{http.StatusBadRequest, "InvalidFormat", "StateReasonData is not valid JSON."}
	}
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	a, res := h.getAlarm(c.scope, name)
	if a == nil {
		return nil, errNotFound("ResourceNotFound", "The alarm "+name+" does not exist.")
	}
	h.engine.transitionLocked(c.scope, a, res, state, reason, str(c.in, "StateReasonData"))
	return nil, nil
}

func (h *Handler) enableAlarmActions(c *call) (map[string]any, *apiError) {
	return h.setActionsEnabled(c, true)
}

func (h *Handler) disableAlarmActions(c *call) (map[string]any, *apiError) {
	return h.setActionsEnabled(c, false)
}

func (h *Handler) setActionsEnabled(c *call, enabled bool) (map[string]any, *apiError) {
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	for _, n := range strList(c.in, "AlarmNames") {
		a, res := h.getAlarm(c.scope, n)
		if a == nil {
			continue
		}
		a.Attrs["ActionsEnabled"] = enabled
		saveAlarm(h.Store, c.scope, a, res)
	}
	return nil, nil
}

//
// Tags
//

// tagTarget resolves arn:aws:cloudwatch:{region}:{account}:alarm:{name}.
// Insight rules and metric streams aren't modelled.
func (h *Handler) tagTarget(c *call) (*alarm, *resource.Resource, *apiError) {
	arn := str(c.in, "ResourceARN")
	if arn == "" {
		return nil, nil, errMissing("ResourceARN")
	}
	i := strings.Index(arn, ":alarm:")
	if !strings.HasPrefix(arn, "arn:") || i < 0 {
		return nil, nil, errNotFound("ResourceNotFoundException", "Resource not found: "+arn)
	}
	a, res := h.getAlarm(c.scope, arn[i+len(":alarm:"):])
	if a == nil || str(a.Attrs, "AlarmArn") != arn {
		return nil, nil, errNotFound("ResourceNotFoundException", "Resource not found: "+arn)
	}
	return a, res, nil
}

func (h *Handler) listTagsForResource(c *call) (map[string]any, *apiError) {
	a, _, aerr := h.tagTarget(c)
	if aerr != nil {
		return nil, aerr
	}
	tags := make([]any, len(a.Tags))
	for i, t := range a.Tags {
		tags[i] = map[string]any{"Key": t.Key, "Value": t.Value}
	}
	return map[string]any{"Tags": tags}, nil
}

func (h *Handler) tagResource(c *call) (map[string]any, *apiError) {
	add, aerr := tagsFrom(c.in)
	if aerr != nil {
		return nil, aerr
	}
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	a, res, aerr := h.tagTarget(c)
	if aerr != nil {
		return nil, aerr
	}
	for _, t := range add {
		replaced := false
		for i := range a.Tags {
			if a.Tags[i].Key == t.Key {
				a.Tags[i].Value = t.Value
				replaced = true
			}
		}
		if !replaced {
			a.Tags = append(a.Tags, t)
		}
	}
	if len(a.Tags) > 50 {
		return nil, &apiError{http.StatusBadRequest, "LimitExceededException", "A resource can have no more than 50 tags."}
	}
	if err := saveAlarm(h.Store, c.scope, a, res); err != nil {
		return nil, &apiError{http.StatusInternalServerError, "InternalServiceError", err.Error()}
	}
	return nil, nil
}

func (h *Handler) untagResource(c *call) (map[string]any, *apiError) {
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	a, res, aerr := h.tagTarget(c)
	if aerr != nil {
		return nil, aerr
	}
	drop := map[string]bool{}
	for _, k := range strList(c.in, "TagKeys") {
		drop[k] = true
	}
	kept := a.Tags[:0]
	for _, t := range a.Tags {
		if !drop[t.Key] {
			kept = append(kept, t)
		}
	}
	a.Tags = kept
	if err := saveAlarm(h.Store, c.scope, a, res); err != nil {
		return nil, &apiError{http.StatusInternalServerError, "InternalServiceError", err.Error()}
	}
	return nil, nil
}
