// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"opensnack/internal/api/cloudwatch"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// Target names the stream other services write to.
type Target struct {
	// Scope is the regional store scope, util.NamespaceFromHeader(r).
	Scope     string
	AccountID string
	Group     string
	Stream    string
}

// TargetFor builds a Target in the caller's namespace, account and region.
func TargetFor(r *http.Request, group, stream string) Target {
	return Target{Scope: util.NamespaceFromHeader(r), AccountID: util.AccountID(r), Group: group, Stream: stream}
}

// InputEvent is one event to append. A zero Timestamp means now.
type InputEvent struct {
	Timestamp int64 // ms since the epoch
	Message   string
}

// Append writes events to a log stream the way a service such as Lambda
// does, creating the group and stream if they don't exist yet. Unlike
// PutLogEvents it doesn't reject old or out-of-order events; it sorts them.
// Metric filters on the group run as usual.
func Append(store resource.Store, t Target, events []InputEvent) error {
	if t.Group == "" || t.Stream == "" {
		return fmt.Errorf("logs.Append: group and stream are required")
	}
	h := &Handler{Store: store}
	_, region := resource.SplitScope(t.Scope)
	g, _, err := h.getGroup(t.Scope, t.Group)
	if err != nil {
		g = &logGroup{
			Name:      t.Group,
			Arn:       groupArn(region, t.AccountID, t.Group),
			CreatedAt: time.Now().UnixMilli(),
			Tags:      map[string]string{},
			Class:     "STANDARD",
		}
		if err := h.createGroup(t.Scope, g); err != nil {
			// Lost a race with another writer; use theirs.
			if g, _, err = h.getGroup(t.Scope, t.Group); err != nil {
				return err
			}
		}
	}
	if _, err := h.getStream(t.Scope, t.Group, t.Stream); err != nil {
		s := &logStream{
			Group:     t.Group,
			Stream:    t.Stream,
			Arn:       streamArn(region, t.AccountID, t.Group, t.Stream),
			CreatedAt: time.Now().UnixMilli(),
		}
		if err := h.createStream(t.Scope, s); err != nil {
			if _, err := h.getStream(t.Scope, t.Group, t.Stream); err != nil {
				return err
			}
		}
	}
	now := time.Now().UnixMilli()
	evs := make([]Event, len(events))
	for i, e := range events {
		ts := e.Timestamp
		if ts == 0 {
			ts = now
		}
		evs[i] = Event{Stream: t.Stream, Timestamp: ts, IngestionTime: now, Message: e.Message}
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Timestamp < evs[j].Timestamp })
	return h.ingest(t.Scope, g, evs)
}

var (
	pruneMu   sync.Mutex
	lastPrune = map[string]time.Time{}
)

// ingest stores accepted events and runs the group's metric filters.
func (h *Handler) ingest(scope string, g *logGroup, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	if err := h.events().Put(scope, g.Name, evs); err != nil {
		return err
	}
	if g.Retention != nil {
		key := scope + "\x00" + g.Name
		pruneMu.Lock()
		due := time.Since(lastPrune[key]) > time.Minute
		if due {
			lastPrune[key] = time.Now()
		}
		pruneMu.Unlock()
		if due {
			cutoff := time.Now().Add(-time.Duration(*g.Retention) * 24 * time.Hour).UnixMilli()
			h.events().Prune(scope, g.Name, cutoff)
		}
	}
	h.applyMetricFilters(scope, g.Name, evs)
	return nil
}

func (h *Handler) applyMetricFilters(scope, group string, evs []Event) {
	filters := h.metricFilters(scope, group)
	if len(filters) == 0 {
		return
	}
	var data []cloudwatch.Datum
	for _, f := range filters {
		pat, err := ParsePattern(f.FilterPattern)
		if err != nil {
			continue
		}
		matched := false
		for _, e := range evs {
			ok, ex := pat.Match(e.Message)
			if !ok {
				continue
			}
			matched = true
			for _, mt := range f.MetricTransformations {
				v, ok := extractNumber(ex, mt.MetricValue)
				if !ok {
					continue
				}
				dims := map[string]string{}
				for name, sel := range mt.Dimensions {
					dv, ok := ex.Lookup(sel)
					if !ok || dv == nil {
						continue
					}
					dims[name] = fmt.Sprint(dv)
				}
				data = append(data, cloudwatch.Datum{
					Namespace:  mt.MetricNamespace,
					MetricName: mt.MetricName,
					Dimensions: dims,
					Timestamp:  time.UnixMilli(e.Timestamp),
					Value:      v,
					Unit:       mt.Unit,
				})
			}
		}
		if matched {
			continue
		}
		// No match in this batch: emit the default, if the filter has one.
		for _, mt := range f.MetricTransformations {
			if mt.DefaultValue == nil || len(mt.Dimensions) > 0 {
				continue
			}
			data = append(data, cloudwatch.Datum{
				Namespace:  mt.MetricNamespace,
				MetricName: mt.MetricName,
				Timestamp:  time.UnixMilli(evs[len(evs)-1].Timestamp),
				Value:      *mt.DefaultValue,
				Unit:       mt.Unit,
			})
		}
	}
	if len(data) > 0 {
		cloudwatch.Record(h.Store, scope, data)
	}
}

func extractNumber(ex Extracted, metricValue string) (float64, bool) {
	if len(metricValue) > 0 && metricValue[0] == '$' {
		v, ok := ex.Lookup(metricValue)
		if !ok {
			return 0, false
		}
		switch x := v.(type) {
		case float64:
			return x, true
		case string:
			f, err := strconv.ParseFloat(x, 64)
			return f, err == nil
		case bool:
			if x {
				return 1, true
			}
			return 0, true
		}
		return 0, false
	}
	f, err := strconv.ParseFloat(metricValue, 64)
	return f, err == nil
}

//
// PutLogEvents / GetLogEvents / FilterLogEvents
//

const (
	maxEventAge    = 14 * 24 * time.Hour
	maxEventFuture = 2 * time.Hour
)

func (h *Handler) PutLogEvents(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req PutLogEventsRequest
	if !decode(w, r, &req) {
		return
	}
	g, _, err := h.getGroup(scope, req.LogGroupName)
	if err != nil {
		groupNotFound(w)
		return
	}
	if _, err := h.getStream(scope, req.LogGroupName, req.LogStreamName); err != nil {
		writeErr(w, "ResourceNotFoundException", "The specified log stream does not exist.")
		return
	}
	n := len(req.LogEvents)
	if n == 0 || n > 10000 {
		writeErr(w, "InvalidParameterException", "1 validation error detected: Value at 'logEvents' failed to satisfy constraint: Member must have length between 1 and 10000")
		return
	}
	for i := 1; i < n; i++ {
		if req.LogEvents[i].Timestamp < req.LogEvents[i-1].Timestamp {
			writeErr(w, "InvalidParameterException", "Log events in a single PutLogEvents request must be in chronological order.")
			return
		}
	}
	if req.LogEvents[n-1].Timestamp-req.LogEvents[0].Timestamp > 24*time.Hour.Milliseconds() {
		writeErr(w, "InvalidParameterException", "The batch of log events in a single PutLogEvents request cannot span more than 24 hours.")
		return
	}

	now := time.Now()
	tooOld := now.Add(-maxEventAge).UnixMilli()
	tooNew := now.Add(maxEventFuture).UnixMilli()
	expired := int64(math.MinInt64)
	if g.Retention != nil {
		expired = now.Add(-time.Duration(*g.Retention) * 24 * time.Hour).UnixMilli()
	}
	rejected := map[string]any{}
	first, last := 0, n
	for first < n && (req.LogEvents[first].Timestamp < tooOld || req.LogEvents[first].Timestamp < expired) {
		first++
	}
	for i := 0; i < first; i++ {
		if req.LogEvents[i].Timestamp < expired {
			rejected["expiredLogEventEndIndex"] = i
		}
		if req.LogEvents[i].Timestamp < tooOld {
			rejected["tooOldLogEventEndIndex"] = i
		}
	}
	for last > first && req.LogEvents[last-1].Timestamp > tooNew {
		last--
	}
	if last < n {
		rejected["tooNewLogEventStartIndex"] = last
	}

	ingested := now.UnixMilli()
	evs := make([]Event, 0, last-first)
	for _, e := range req.LogEvents[first:last] {
		evs = append(evs, Event{Stream: req.LogStreamName, Timestamp: e.Timestamp, IngestionTime: ingested, Message: e.Message})
	}
	if err := h.ingest(scope, g, evs); err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	stats, _ := h.events().Stats(scope, g.Name)
	resp := map[string]any{"nextSequenceToken": sequenceToken(stats[req.LogStreamName].Count)}
	if len(rejected) > 0 {
		resp["rejectedLogEventsInfo"] = rejected
	}
	writeOK(w, resp)
}

// sequenceToken is informational: AWS stopped enforcing sequence tokens in
// 2023, but SDKs still read the field.
func sequenceToken(n int64) string {
	return fmt.Sprintf("%056d", n)
}

func formatCursor(prefix string, c cursor) string {
	return fmt.Sprintf("%s/%d/%d", prefix, c.ts, c.seq)
}

func parseCursor(tok string) (string, cursor, bool) {
	var c cursor
	if len(tok) < 2 || tok[1] != '/' {
		return "", c, false
	}
	if _, err := fmt.Sscanf(tok[2:], "%d/%d", &c.ts, &c.seq); err != nil {
		return "", c, false
	}
	return tok[:1], c, true
}

func optMillis(v *int64) *int64 {
	if v == nil || *v == 0 {
		return nil
	}
	return v
}

func (h *Handler) GetLogEvents(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName       string `json:"logGroupName"`
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		LogStreamName      string `json:"logStreamName"`
		StartTime          *int64 `json:"startTime"`
		EndTime            *int64 `json:"endTime"`
		NextToken          string `json:"nextToken"`
		Limit              int    `json:"limit"`
		StartFromHead      bool   `json:"startFromHead"`
	}
	if !decode(w, r, &req) {
		return
	}
	group := groupNameFrom(req.LogGroupName, req.LogGroupIdentifier)
	if _, _, err := h.getGroup(scope, group); err != nil {
		groupNotFound(w)
		return
	}
	if _, err := h.getStream(scope, group, req.LogStreamName); err != nil {
		writeErr(w, "ResourceNotFoundException", "The specified log stream does not exist.")
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	q := EventQuery{
		Scope: scope, Group: group, Streams: []string{req.LogStreamName},
		Start: optMillis(req.StartTime), End: optMillis(req.EndTime), Limit: limit,
	}
	dir := ""
	var at cursor
	if req.NextToken != "" {
		var ok bool
		dir, at, ok = parseCursor(req.NextToken)
		if !ok || (dir != "f" && dir != "b") {
			writeErr(w, "InvalidParameterException", "The specified nextToken is invalid.")
			return
		}
	}
	backward := dir == "b" || (dir == "" && !req.StartFromHead)
	switch dir {
	case "f":
		q.After = &at
	case "b":
		q.Before = &at
	}
	q.Desc = backward
	evs, err := h.events().Query(q)
	if err != nil {
		writeErr(w, "ServiceUnavailableException", err.Error())
		return
	}
	if backward {
		for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
			evs[i], evs[j] = evs[j], evs[i]
		}
	}

	out := make([]map[string]any, len(evs))
	for i, e := range evs {
		out[i] = map[string]any{"timestamp": e.Timestamp, "message": e.Message, "ingestionTime": e.IngestionTime}
	}
	// With nothing returned, a token keeps pointing where it was, which is
	// how clients following a stream know they've reached the end.
	fwd, bwd := cursor{math.MinInt64, 0}, cursor{math.MaxInt64, 0}
	switch {
	case len(evs) > 0:
		fwd, bwd = evs[len(evs)-1].pos(), evs[0].pos()
	case dir == "f":
		fwd, bwd = at, cursor{at.ts, at.seq + 1}
	case dir == "b":
		fwd, bwd = cursor{at.ts, at.seq - 1}, at
	}
	writeOK(w, map[string]any{
		"events":            out,
		"nextForwardToken":  formatCursor("f", fwd),
		"nextBackwardToken": formatCursor("b", bwd),
	})
}

// filterScanCap bounds how many events one FilterLogEvents call reads, so a
// rare pattern over a huge group returns a nextToken instead of scanning it
// all in one request (AWS behaves the same way).
const filterScanCap = 100000

func (h *Handler) FilterLogEvents(w http.ResponseWriter, r *http.Request) {
	scope := util.NamespaceFromHeader(r)
	var req struct {
		LogGroupName        string   `json:"logGroupName"`
		LogGroupIdentifier  string   `json:"logGroupIdentifier"`
		LogStreamNames      []string `json:"logStreamNames"`
		LogStreamNamePrefix string   `json:"logStreamNamePrefix"`
		StartTime           *int64   `json:"startTime"`
		EndTime             *int64   `json:"endTime"`
		FilterPattern       string   `json:"filterPattern"`
		NextToken           string   `json:"nextToken"`
		Limit               int      `json:"limit"`
	}
	if !decode(w, r, &req) {
		return
	}
	group := groupNameFrom(req.LogGroupName, req.LogGroupIdentifier)
	if _, _, err := h.getGroup(scope, group); err != nil {
		groupNotFound(w)
		return
	}
	if len(req.LogStreamNames) > 0 && req.LogStreamNamePrefix != "" {
		writeErr(w, "InvalidParameterException", "logStreamNames and logStreamNamePrefix are mutually exclusive")
		return
	}
	pat, err := ParsePattern(req.FilterPattern)
	if err != nil {
		writeErr(w, "InvalidParameterException", err.Error())
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	q := EventQuery{
		Scope: scope, Group: group, Streams: req.LogStreamNames, StreamPrefix: req.LogStreamNamePrefix,
		Start: optMillis(req.StartTime), End: optMillis(req.EndTime), Limit: 1000,
	}
	if req.NextToken != "" {
		dir, c, ok := parseCursor(req.NextToken)
		if !ok || dir != "c" {
			writeErr(w, "InvalidParameterException", "The specified nextToken is invalid.")
			return
		}
		q.After = &c
	}

	out := []map[string]any{}
	next := ""
	scanned := 0
scan:
	for {
		batch, err := h.events().Query(q)
		if err != nil {
			writeErr(w, "ServiceUnavailableException", err.Error())
			return
		}
		for _, e := range batch {
			scanned++
			if ok, _ := pat.Match(e.Message); ok {
				out = append(out, map[string]any{
					"logStreamName": e.Stream,
					"timestamp":     e.Timestamp,
					"message":       e.Message,
					"ingestionTime": e.IngestionTime,
					"eventId":       fmt.Sprintf("%020d%020d", e.Timestamp, e.Seq),
				})
			}
			if len(out) == limit || scanned >= filterScanCap {
				next = formatCursor("c", e.pos())
				break scan
			}
		}
		if len(batch) < q.Limit {
			break
		}
		last := batch[len(batch)-1].pos()
		q.After = &last
	}

	searched := []map[string]any{}
	want := map[string]bool{}
	for _, s := range req.LogStreamNames {
		want[s] = true
	}
	for _, s := range h.listStreams(scope, group) {
		if (len(want) == 0 || want[s.Stream]) && (req.LogStreamNamePrefix == "" || len(s.Stream) >= len(req.LogStreamNamePrefix) && s.Stream[:len(req.LogStreamNamePrefix)] == req.LogStreamNamePrefix) {
			searched = append(searched, map[string]any{"logStreamName": s.Stream, "searchedCompletely": next == ""})
		}
	}
	resp := map[string]any{"events": out, "searchedLogStreams": searched}
	if next != "" {
		resp["nextToken"] = next
	}
	writeOK(w, resp)
}
