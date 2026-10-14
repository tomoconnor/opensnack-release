// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudwatch

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"opensnack/internal/resource"
)

// Metric data lives in memory, per store scope: a series per (namespace,
// metric, dimensions) holding at most maxPoints points from the last
// retention period. It is lost on restart, which suits tests that put
// data and read it (or watch an alarm react) within a session.
const (
	maxPoints       = 20000
	metricRetention = 15 * 24 * time.Hour
)

// Datum is one metric data point, from PutMetricData or a log metric filter.
type Datum struct {
	Namespace  string
	MetricName string
	Dimensions map[string]string
	Timestamp  time.Time
	Value      float64
	Unit       string
	// Stats, when set, is a pre-aggregated StatisticSet and Value is unused.
	Stats *StatSet
	// Values/Counts carry the PutMetricData Values array.
	Values []float64
	Counts []float64
}

type StatSet struct {
	SampleCount, Sum, Min, Max float64
}

type point struct {
	t     time.Time
	unit  string
	stats StatSet
	vals  []float64 // raw values, for percentiles
}

type series struct {
	namespace, name string
	dims            map[string]string
	points          []point
}

// Notifier delivers alarm actions to SNS. The router adapts the SNS
// handler's Publish to it, so this package doesn't depend on SNS.
type Notifier interface {
	Publish(scope, accountID, topicArn, subject, message string) error
}

// Engine holds metric data and evaluates alarms for one resource store.
type Engine struct {
	store resource.Store

	// mu serialises alarm writes and evaluation.
	mu sync.Mutex

	dataMu sync.RWMutex
	series map[string]map[string]*series // scope -> series key -> series

	notifyMu sync.RWMutex
	notifier Notifier
}

var engines sync.Map // resource.Store -> *Engine

// EngineFor returns the engine for a store, creating it on first use.
func EngineFor(store resource.Store) *Engine {
	if e, ok := engines.Load(store); ok {
		return e.(*Engine)
	}
	e, _ := engines.LoadOrStore(store, &Engine{store: store, series: map[string]map[string]*series{}})
	return e.(*Engine)
}

func (e *Engine) SetNotifier(n Notifier) {
	e.notifyMu.Lock()
	e.notifier = n
	e.notifyMu.Unlock()
}

// Record stores data points and evaluates the alarms watching them. Other
// packages (CloudWatch Logs metric filters) call it directly.
func Record(store resource.Store, scope string, data []Datum) {
	EngineFor(store).Record(scope, data)
}

func (e *Engine) Record(scope string, data []Datum) {
	e.dataMu.Lock()
	byKey := e.series[scope]
	if byKey == nil {
		byKey = map[string]*series{}
		e.series[scope] = byKey
	}
	cutoff := time.Now().Add(-metricRetention)
	touched := map[string]bool{}
	for _, d := range data {
		key := seriesKey(d.Namespace, d.MetricName, d.Dimensions)
		s := byKey[key]
		if s == nil {
			s = &series{namespace: d.Namespace, name: d.MetricName, dims: d.Dimensions}
			byKey[key] = s
		}
		s.points = append(s.points, pointFrom(d))
		touched[key] = true
	}
	for key := range touched {
		s := byKey[key]
		sort.SliceStable(s.points, func(i, j int) bool { return s.points[i].t.Before(s.points[j].t) })
		i := 0
		for i < len(s.points) && s.points[i].t.Before(cutoff) {
			i++
		}
		if n := len(s.points) - i; n > maxPoints {
			i = len(s.points) - maxPoints
		}
		if i > 0 {
			s.points = append([]point(nil), s.points[i:]...)
		}
	}
	e.dataMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range listAlarms(e.store, scope) {
		if a.Kind != kindMetric || str(a.Attrs, "MetricName") == "" {
			continue
		}
		if touched[seriesKey(str(a.Attrs, "Namespace"), str(a.Attrs, "MetricName"), dimensionsFrom(a.Attrs))] {
			e.evaluateLocked(scope, a)
		}
	}
}

func pointFrom(d Datum) point {
	p := point{t: d.Timestamp, unit: d.Unit}
	switch {
	case d.Stats != nil:
		p.stats = *d.Stats
	case len(d.Values) > 0:
		p.stats = StatSet{Min: math.Inf(1), Max: math.Inf(-1)}
		for i, v := range d.Values {
			c := 1.0
			if i < len(d.Counts) {
				c = d.Counts[i]
			}
			p.stats.SampleCount += c
			p.stats.Sum += v * c
			p.stats.Min = math.Min(p.stats.Min, v)
			p.stats.Max = math.Max(p.stats.Max, v)
			for k := 0; k < int(c) && k < 1000; k++ {
				p.vals = append(p.vals, v)
			}
		}
	default:
		p.stats = StatSet{SampleCount: 1, Sum: d.Value, Min: d.Value, Max: d.Value}
		p.vals = []float64{d.Value}
	}
	return p
}

func seriesKey(namespace, name string, dims map[string]string) string {
	return namespace + "\x00" + name + "\x00" + dimensionsKey(dims)
}

func dimensionsKey(dims map[string]string) string {
	keys := make([]string, 0, len(dims))
	for k := range dims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + dims[k] + "\x01")
	}
	return b.String()
}

// dimensionsFrom reads [{Name, Value}] from a request or alarm.
func dimensionsFrom(m map[string]any) map[string]string {
	out := map[string]string{}
	for _, d := range list(m, "Dimensions") {
		dm := obj(d)
		out[str(dm, "Name")] = str(dm, "Value")
	}
	return out
}

func dimensionsOut(dims map[string]string) []any {
	keys := make([]string, 0, len(dims))
	for k := range dims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = map[string]any{"Name": k, "Value": dims[k]}
	}
	return out
}

// aggregate computes stat over the points of a series in [start, end).
func (e *Engine) aggregate(scope, namespace, name string, dims map[string]string, unit string, start, end time.Time) (StatSet, []float64, bool) {
	e.dataMu.RLock()
	defer e.dataMu.RUnlock()
	s := e.series[scope][seriesKey(namespace, name, dims)]
	if s == nil {
		return StatSet{}, nil, false
	}
	agg := StatSet{Min: math.Inf(1), Max: math.Inf(-1)}
	var vals []float64
	found := false
	lo := sort.Search(len(s.points), func(i int) bool { return !s.points[i].t.Before(start) })
	for _, p := range s.points[lo:] {
		if !p.t.Before(end) {
			break
		}
		if unit != "" && p.unit != unit && !(unit == "None" && p.unit == "") {
			continue
		}
		found = true
		agg.SampleCount += p.stats.SampleCount
		agg.Sum += p.stats.Sum
		agg.Min = math.Min(agg.Min, p.stats.Min)
		agg.Max = math.Max(agg.Max, p.stats.Max)
		vals = append(vals, p.vals...)
	}
	return agg, vals, found
}

// statValue picks a statistic (Average, p90, ...) from an aggregate.
func statValue(stat string, agg StatSet, vals []float64) (float64, bool) {
	switch stat {
	case "SampleCount":
		return agg.SampleCount, true
	case "Sum":
		return agg.Sum, true
	case "Minimum":
		return agg.Min, true
	case "Maximum":
		return agg.Max, true
	case "Average":
		if agg.SampleCount == 0 {
			return 0, false
		}
		return agg.Sum / agg.SampleCount, true
	}
	if strings.HasPrefix(stat, "p") && len(vals) > 0 {
		var pct float64
		if _, err := fmt.Sscanf(stat[1:], "%g", &pct); err != nil || pct < 0 || pct > 100 {
			return 0, false
		}
		s := append([]float64(nil), vals...)
		sort.Float64s(s)
		idx := int(math.Ceil(pct/100*float64(len(s)))) - 1
		if idx < 0 {
			idx = 0
		}
		return s[idx], true
	}
	return 0, false
}

//
// Alarm evaluation
//

// evaluateLocked works out a metric alarm's state from the data in its
// evaluation range. Windows are aligned to the period and the newest one is
// the current, still-open period, so an alarm reacts as soon as data
// arrives rather than a period later. Metric-math alarms are left alone.
func (e *Engine) evaluateLocked(scope string, a *alarm) {
	if a.Kind == kindComposite {
		e.evaluateCompositeLocked(scope, a)
		return
	}
	attrs := a.Attrs
	name := str(attrs, "MetricName")
	if name == "" {
		return
	}
	periodF, _ := num(attrs, "Period")
	nF, _ := num(attrs, "EvaluationPeriods")
	period := time.Duration(periodF) * time.Second
	n := int(nF)
	if period <= 0 || n <= 0 {
		return
	}
	m := n
	if v, ok := num(attrs, "DatapointsToAlarm"); ok {
		m = int(v)
	}
	threshold, _ := num(attrs, "Threshold")
	stat := str(attrs, "Statistic")
	if stat == "" {
		stat = str(attrs, "ExtendedStatistic")
	}
	op := str(attrs, "ComparisonOperator")
	treat := str(attrs, "TreatMissingData")
	if treat == "" {
		treat = "missing"
	}

	end := time.Now().Truncate(period).Add(period)
	breaching, present := 0, 0
	var last float64
	haveLast := false
	for i := 0; i < n; i++ {
		wEnd := end.Add(-time.Duration(i) * period)
		agg, vals, ok := e.aggregate(scope, str(attrs, "Namespace"), name, dimensionsFrom(attrs), str(attrs, "Unit"), wEnd.Add(-period), wEnd)
		var v float64
		if ok {
			v, ok = statValue(stat, agg, vals)
		}
		if !ok {
			switch treat {
			case "breaching":
				present++
				breaching++
			case "notBreaching":
				present++
			}
			continue
		}
		if !haveLast {
			last, haveLast = v, true
		}
		present++
		if compare(op, v, threshold) {
			breaching++
		}
	}

	var state, reason string
	switch {
	case present == 0 && treat == "ignore":
		return
	case present == 0:
		state = stateInsufficient
		reason = fmt.Sprintf("Insufficient Data: %d datapoints were unknown.", n)
	case breaching >= m:
		state = stateAlarm
		reason = fmt.Sprintf("Threshold Crossed: %d out of the last %d datapoints were %s the threshold (%g). The most recent datapoint: %g.",
			breaching, n, opWords(op), threshold, last)
	default:
		state = stateOK
		reason = fmt.Sprintf("Threshold Crossed: %d out of the last %d datapoints were not %s the threshold (%g). The most recent datapoint: %g.",
			present-breaching, n, opWords(op), threshold, last)
	}
	_, res := getAlarm(e.store, scope, a.name())
	if res == nil {
		return
	}
	e.transitionLocked(scope, a, res, state, reason, "")
}

func compare(op string, v, threshold float64) bool {
	switch op {
	case "GreaterThanOrEqualToThreshold":
		return v >= threshold
	case "GreaterThanThreshold":
		return v > threshold
	case "LessThanThreshold":
		return v < threshold
	case "LessThanOrEqualToThreshold":
		return v <= threshold
	}
	return false
}

func opWords(op string) string {
	switch op {
	case "GreaterThanOrEqualToThreshold":
		return "greater than or equal to"
	case "GreaterThanThreshold":
		return "greater than"
	case "LessThanThreshold":
		return "less than"
	case "LessThanOrEqualToThreshold":
		return "less than or equal to"
	}
	return op
}

// transitionLocked moves an alarm to a new state, records history, runs
// its actions and re-evaluates composite alarms that depend on it. Setting
// the state it is already in only refreshes the reason.
func (e *Engine) transitionLocked(scope string, a *alarm, res *resource.Resource, state, reason, reasonData string) {
	old := a.state()
	now := time.Now().UnixMilli()
	if old == state && str(a.Attrs, "StateReason") == reason {
		return
	}
	a.Attrs["StateValue"] = state
	a.Attrs["StateReason"] = reason
	if reasonData != "" {
		a.Attrs["StateReasonData"] = reasonData
	} else {
		delete(a.Attrs, "StateReasonData")
	}
	a.Attrs["StateUpdatedTimestamp"] = now
	if old == state {
		saveAlarm(e.store, scope, a, res)
		return
	}
	a.Attrs["StateTransitionedTimestamp"] = now
	a.addHistory("StateUpdate", fmt.Sprintf("Alarm updated from %s to %s", old, state), "")
	saveAlarm(e.store, scope, a, res)

	e.runActions(scope, a, old)

	for _, parent := range listAlarms(e.store, scope) {
		if parent.Kind != kindComposite {
			continue
		}
		if rule, err := parseRule(str(parent.Attrs, "AlarmRule")); err == nil && contains(rule.names(), a.name()) {
			e.evaluateCompositeLocked(scope, parent)
		}
	}
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func (e *Engine) runActions(scope string, a *alarm, oldState string) {
	if enabled, ok := a.Attrs["ActionsEnabled"].(bool); ok && !enabled {
		return
	}
	key := map[string]string{stateAlarm: "AlarmActions", stateOK: "OKActions", stateInsufficient: "InsufficientDataActions"}[a.state()]
	actions := strList(a.Attrs, key)
	if len(actions) == 0 {
		return
	}
	e.notifyMu.RLock()
	n := e.notifier
	e.notifyMu.RUnlock()

	arn := str(a.Attrs, "AlarmArn")
	parts := strings.SplitN(arn, ":", 6)
	region, account := "", ""
	if len(parts) == 6 {
		region, account = parts[3], parts[4]
	}
	subject := fmt.Sprintf("%s: %q in %s", a.state(), a.name(), region)
	if len(subject) > 100 {
		subject = subject[:100]
	}
	msg := notification(a, oldState, region, account)
	for _, act := range actions {
		if !strings.HasPrefix(act, "arn:aws:sns:") {
			continue
		}
		summary := "Published a notification to " + act
		if n == nil {
			summary = "Failed to publish to " + act + ": no SNS notifier"
		} else if err := n.Publish(scope, account, act, subject, msg); err != nil {
			summary = "Failed to publish to " + act + ": " + err.Error()
			zap.L().Warn("cloudwatch: alarm action failed", zap.String("alarm", a.name()), zap.Error(err))
		}
		a.addHistory("Action", summary, "")
	}
	if _, res := getAlarm(e.store, scope, a.name()); res != nil {
		saveAlarm(e.store, scope, a, res)
	}
}

// notification is the JSON message AWS sends to SNS for a state change.
func notification(a *alarm, oldState, region, account string) string {
	ts := func(k string) string {
		ms, _ := toNum(a.Attrs[k])
		return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000-0700")
	}
	msg := map[string]any{
		"AlarmName":                          a.name(),
		"AlarmDescription":                   a.Attrs["AlarmDescription"],
		"AWSAccountId":                       account,
		"AlarmConfigurationUpdatedTimestamp": ts("AlarmConfigurationUpdatedTimestamp"),
		"NewStateValue":                      a.state(),
		"NewStateReason":                     a.Attrs["StateReason"],
		"StateChangeTime":                    ts("StateUpdatedTimestamp"),
		"Region":                             region,
		"AlarmArn":                           a.Attrs["AlarmArn"],
		"OldStateValue":                      oldState,
		"OKActions":                          strList(a.Attrs, "OKActions"),
		"AlarmActions":                       strList(a.Attrs, "AlarmActions"),
		"InsufficientDataActions":            strList(a.Attrs, "InsufficientDataActions"),
	}
	if a.Kind == kindComposite {
		msg["AlarmRule"] = a.Attrs["AlarmRule"]
	} else {
		dims := []map[string]string{}
		for _, d := range dimensionsOut(dimensionsFrom(a.Attrs)) {
			dm := d.(map[string]any)
			dims = append(dims, map[string]string{"value": dm["Value"].(string), "name": dm["Name"].(string)})
		}
		trigger := map[string]any{
			"MetricName":         a.Attrs["MetricName"],
			"Namespace":          a.Attrs["Namespace"],
			"StatisticType":      "Statistic",
			"Statistic":          strings.ToUpper(str(a.Attrs, "Statistic")),
			"Unit":               a.Attrs["Unit"],
			"Dimensions":         dims,
			"Period":             a.Attrs["Period"],
			"EvaluationPeriods":  a.Attrs["EvaluationPeriods"],
			"ComparisonOperator": a.Attrs["ComparisonOperator"],
			"Threshold":          a.Attrs["Threshold"],
			"TreatMissingData":   a.Attrs["TreatMissingData"],
		}
		if ext := str(a.Attrs, "ExtendedStatistic"); ext != "" {
			trigger["StatisticType"] = "ExtendedStatistic"
			trigger["ExtendedStatistic"] = ext
			delete(trigger, "Statistic")
		}
		msg["Trigger"] = trigger
	}
	b, _ := json.Marshal(msg)
	return string(b)
}

//
// Metric operations
//

func (h *Handler) putMetricData(c *call) (map[string]any, *apiError) {
	ns := str(c.in, "Namespace")
	if ns == "" {
		return nil, errMissing("Namespace")
	}
	if strings.HasPrefix(ns, "AWS/") {
		return nil, errInvalid("The value AWS/ for parameter Namespace is invalid.")
	}
	items := list(c.in, "MetricData")
	if len(items) == 0 {
		return nil, errMissing("MetricData")
	}
	if len(items) > 1000 {
		return nil, errInvalid("MetricData must contain at most 1000 items.")
	}
	now := time.Now()
	var data []Datum
	for _, it := range items {
		m := obj(it)
		d := Datum{Namespace: ns, MetricName: str(m, "MetricName"), Dimensions: dimensionsFrom(m), Unit: str(m, "Unit"), Timestamp: now}
		if d.MetricName == "" {
			return nil, errMissing("MetricData.member.N.MetricName")
		}
		if t, ok := timestamp(m, "Timestamp"); ok {
			d.Timestamp = t
		}
		if now.Sub(d.Timestamp) > 14*24*time.Hour || d.Timestamp.Sub(now) > 2*time.Hour {
			return nil, errInvalid("The parameter MetricData.member.N.Timestamp must specify a time no more than two weeks in the past or two hours in the future.")
		}
		v, hasValue := num(m, "Value")
		sv := obj(m["StatisticValues"])
		vals := list(m, "Values")
		switch {
		case sv != nil:
			if hasValue {
				return nil, errCombination("The parameters MetricData.member.N.Value and MetricData.member.N.StatisticValues are mutually exclusive.")
			}
			st := &StatSet{}
			st.SampleCount, _ = num(sv, "SampleCount")
			st.Sum, _ = num(sv, "Sum")
			st.Min, _ = num(sv, "Minimum")
			st.Max, _ = num(sv, "Maximum")
			d.Stats = st
		case len(vals) > 0:
			for _, x := range vals {
				f, _ := toNum(x)
				d.Values = append(d.Values, f)
			}
			for _, x := range list(m, "Counts") {
				f, _ := toNum(x)
				d.Counts = append(d.Counts, f)
			}
		case hasValue:
			d.Value = v
		default:
			return nil, errMissing("MetricData.member.N.Value")
		}
		data = append(data, d)
	}
	h.engine.Record(c.scope, data)
	return nil, nil
}

// buckets aggregates a metric into period-aligned buckets over [start, end).
func (h *Handler) buckets(scope, ns, name string, dims map[string]string, unit string, start, end time.Time, period time.Duration) []bucket {
	var out []bucket
	for t := start.Truncate(period); t.Before(end); t = t.Add(period) {
		from := t
		if from.Before(start) {
			from = start
		}
		agg, vals, ok := h.engine.aggregate(scope, ns, name, dims, unit, from, t.Add(period))
		if ok {
			out = append(out, bucket{t, agg, vals})
		}
	}
	return out
}

type bucket struct {
	t    time.Time
	agg  StatSet
	vals []float64
}

func (h *Handler) getMetricStatistics(c *call) (map[string]any, *apiError) {
	ns, name := str(c.in, "Namespace"), str(c.in, "MetricName")
	if ns == "" || name == "" {
		return nil, errMissing("Namespace and MetricName")
	}
	start, ok1 := timestamp(c.in, "StartTime")
	end, ok2 := timestamp(c.in, "EndTime")
	if !ok1 || !ok2 {
		return nil, errMissing("StartTime and EndTime")
	}
	if !start.Before(end) {
		return nil, errInvalid("The parameter StartTime must be less than the parameter EndTime.")
	}
	p, ok := num(c.in, "Period")
	if !ok || p < 1 {
		return nil, errMissing("Period")
	}
	stats := strList(c.in, "Statistics")
	ext := strList(c.in, "ExtendedStatistics")
	if len(stats) == 0 && len(ext) == 0 {
		return nil, errMissing("Statistics or ExtendedStatistics")
	}
	unit := str(c.in, "Unit")
	points := []any{}
	for _, b := range h.buckets(c.scope, ns, name, dimensionsFrom(c.in), unit, start, end, time.Duration(p)*time.Second) {
		dp := map[string]any{"Timestamp": b.t}
		if unit != "" {
			dp["Unit"] = unit
		} else {
			dp["Unit"] = "None"
		}
		for _, s := range stats {
			if v, ok := statValue(s, b.agg, b.vals); ok {
				dp[s] = v
			}
		}
		if len(ext) > 0 {
			em := map[string]any{}
			for _, s := range ext {
				if v, ok := statValue(s, b.agg, b.vals); ok {
					em[s] = v
				}
			}
			dp["ExtendedStatistics"] = em
		}
		points = append(points, dp)
	}
	return map[string]any{"Label": name, "Datapoints": points}, nil
}

func (h *Handler) getMetricData(c *call) (map[string]any, *apiError) {
	start, ok1 := timestamp(c.in, "StartTime")
	end, ok2 := timestamp(c.in, "EndTime")
	if !ok1 || !ok2 {
		return nil, errMissing("StartTime and EndTime")
	}
	queries := list(c.in, "MetricDataQueries")
	if len(queries) == 0 {
		return nil, errMissing("MetricDataQueries")
	}
	asc := str(c.in, "ScanBy") == "TimestampAscending"
	results := []any{}
	messages := []any{}
	for _, q := range queries {
		qm := obj(q)
		id := str(qm, "Id")
		if rd, ok := boolean(qm, "ReturnData"); ok && !rd {
			continue
		}
		ms := obj(qm["MetricStat"])
		if ms == nil {
			messages = append(messages, map[string]any{"Code": "Unsupported", "Value": "opensnack evaluates MetricStat queries only, not expressions (" + id + ")"})
			results = append(results, map[string]any{"Id": id, "Label": str(qm, "Label"), "Timestamps": []any{}, "Values": []any{}, "StatusCode": "InternalError"})
			continue
		}
		metric := obj(ms["Metric"])
		period, _ := num(ms, "Period")
		if p, ok := num(qm, "Period"); ok {
			period = p
		}
		if period < 1 {
			return nil, errMissing("MetricDataQueries.member.N.MetricStat.Period")
		}
		label := str(qm, "Label")
		if label == "" {
			label = str(metric, "MetricName")
		}
		bs := h.buckets(c.scope, str(metric, "Namespace"), str(metric, "MetricName"), dimensionsFrom(metric), str(ms, "Unit"), start, end, time.Duration(period)*time.Second)
		if !asc {
			for i, j := 0, len(bs)-1; i < j; i, j = i+1, j-1 {
				bs[i], bs[j] = bs[j], bs[i]
			}
		}
		ts, vs := []any{}, []any{}
		for _, b := range bs {
			if v, ok := statValue(str(ms, "Stat"), b.agg, b.vals); ok {
				ts = append(ts, b.t)
				vs = append(vs, v)
			}
		}
		results = append(results, map[string]any{"Id": id, "Label": label, "Timestamps": ts, "Values": vs, "StatusCode": "Complete"})
	}
	return map[string]any{"MetricDataResults": results, "Messages": messages}, nil
}

func (h *Handler) listMetrics(c *call) (map[string]any, *apiError) {
	ns, name := str(c.in, "Namespace"), str(c.in, "MetricName")
	type dimFilter struct {
		name, value string
		hasValue    bool
	}
	var filters []dimFilter
	for _, d := range list(c.in, "Dimensions") {
		dm := obj(d)
		_, has := dm["Value"]
		filters = append(filters, dimFilter{str(dm, "Name"), str(dm, "Value"), has})
	}
	recent := str(c.in, "RecentlyActive") == "PT3H"

	h.engine.dataMu.RLock()
	var keys []string
	for k := range h.engine.series[c.scope] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []any{}
	for _, k := range keys {
		s := h.engine.series[c.scope][k]
		if (ns != "" && s.namespace != ns) || (name != "" && s.name != name) || len(s.points) == 0 {
			continue
		}
		if recent && time.Since(s.points[len(s.points)-1].t) > 3*time.Hour {
			continue
		}
		match := true
		for _, f := range filters {
			v, ok := s.dims[f.name]
			if !ok || (f.hasValue && v != f.value) {
				match = false
			}
		}
		if !match {
			continue
		}
		out = append(out, map[string]any{"Namespace": s.namespace, "MetricName": s.name, "Dimensions": dimensionsOut(s.dims)})
		if len(out) == 500 {
			break
		}
	}
	h.engine.dataMu.RUnlock()
	return map[string]any{"Metrics": out}, nil
}

// DeleteNamespace drops a tenant's metric data in every region, for the
// inspection API's namespace delete.
func (e *Engine) DeleteNamespace(ns string) error {
	e.dataMu.Lock()
	defer e.dataMu.Unlock()
	for scope := range e.series {
		if n, _ := resource.SplitScope(scope); n == ns {
			delete(e.series, scope)
		}
	}
	return nil
}
