// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package events emulates Amazon EventBridge (CloudWatch Events): event
// buses, rules with event patterns or schedules, targets, PutEvents with
// real pattern matching and delivery, and a scheduler for scheduled rules.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

const (
	service        = "events"
	typeBus        = "bus"
	typeRule       = "rule"
	defaultBus     = "default"
	maxTargets     = 5
	stateEnabled   = "ENABLED"
	stateDisabled  = "DISABLED"
	scheduleAttr   = "ScheduleExpression"
	targetPrefix   = "AWSEvents."
	contentTypeAWS = "application/x-amz-json-1.1"
)

type Handler struct {
	Store      resource.Store
	Deliverers *Registry
	Log        *DeliveryLog
	// Now is the clock for event times and the delivery log.
	Now func() time.Time

	// mu serialises read-modify-write of rule documents (targets, tags).
	mu sync.Mutex
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store, Deliverers: NewRegistry(), Log: NewDeliveryLog(200), Now: time.Now}
}

func (h *Handler) now() time.Time { return h.Now().UTC() }

// Target is one rule target. Raw keeps exactly what the client sent so
// ListTargetsByRule returns every block, including ones we don't act on.
type Target struct {
	Id               string            `json:"Id"`
	Arn              string            `json:"Arn"`
	RoleArn          *string           `json:"RoleArn,omitempty"`
	Input            *string           `json:"Input,omitempty"`
	InputPath        *string           `json:"InputPath,omitempty"`
	InputTransformer *InputTransformer `json:"InputTransformer,omitempty"`
	SqsParameters    *struct {
		MessageGroupId *string `json:"MessageGroupId,omitempty"`
	} `json:"SqsParameters,omitempty"`
	Raw json.RawMessage `json:"-"`
}

type busDoc struct {
	Name             string            `json:"Name"`
	Arn              string            `json:"Arn"`
	Description      *string           `json:"Description,omitempty"`
	KmsKeyIdentifier *string           `json:"KmsKeyIdentifier,omitempty"`
	EventSourceName  *string           `json:"EventSourceName,omitempty"`
	DeadLetterConfig json.RawMessage   `json:"DeadLetterConfig,omitempty"`
	LogConfig        json.RawMessage   `json:"LogConfig,omitempty"`
	Tags             map[string]string `json:"Tags,omitempty"`
	CreationTime     float64           `json:"CreationTime"`
	LastModifiedTime float64           `json:"LastModifiedTime"`
	// Implicit marks a stored row for the default bus, which exists only to
	// hold its tags.
	Implicit bool `json:"Implicit,omitempty"`
}

type ruleDoc struct {
	Name               string            `json:"Name"`
	Arn                string            `json:"Arn"`
	EventBusName       string            `json:"EventBusName"`
	EventPattern       *string           `json:"EventPattern,omitempty"`
	ScheduleExpression *string           `json:"ScheduleExpression,omitempty"`
	State              string            `json:"State"`
	Description        *string           `json:"Description,omitempty"`
	RoleArn            *string           `json:"RoleArn,omitempty"`
	CreatedBy          string            `json:"CreatedBy"`
	Tags               map[string]string `json:"Tags,omitempty"`
	Targets            []json.RawMessage `json:"Targets,omitempty"`
}

func ruleID(bus, name string) string { return bus + "/" + name }

// ─── plumbing ──────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	w.Header().Set("Content-Type", contentTypeAWS)
	w.Header().Set("X-Amzn-RequestId", awsresponses.NextRequestID())
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func writeErr(w http.ResponseWriter, code, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"__type": code, "message": msg})
}

func notFoundRule(w http.ResponseWriter, name, bus string) {
	writeErr(w, "ResourceNotFoundException", fmt.Sprintf("Rule %s does not exist on EventBus %s.", name, bus))
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(r.Body)
	if err == nil && len(bytes.TrimSpace(body)) > 0 {
		err = json.Unmarshal(body, v)
	}
	if err != nil {
		writeErr(w, "SerializationException", "Could not parse request body: "+err.Error())
		return false
	}
	return true
}

func epoch(t time.Time) float64 {
	return float64(t.UnixMilli()) / 1000
}

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), targetPrefix)
	ops := map[string]http.HandlerFunc{
		"CreateEventBus":        h.CreateEventBus,
		"DescribeEventBus":      h.DescribeEventBus,
		"UpdateEventBus":        h.UpdateEventBus,
		"ListEventBuses":        h.ListEventBuses,
		"DeleteEventBus":        h.DeleteEventBus,
		"PutRule":               h.PutRule,
		"DescribeRule":          h.DescribeRule,
		"ListRules":             h.ListRules,
		"DeleteRule":            h.DeleteRule,
		"EnableRule":            h.setState(stateEnabled),
		"DisableRule":           h.setState(stateDisabled),
		"PutTargets":            h.PutTargets,
		"RemoveTargets":         h.RemoveTargets,
		"ListTargetsByRule":     h.ListTargetsByRule,
		"ListRuleNamesByTarget": h.ListRuleNamesByTarget,
		"PutEvents":             h.PutEvents,
		"TestEventPattern":      h.TestEventPattern,
		"TagResource":           h.TagResource,
		"UntagResource":         h.UntagResource,
		"ListTagsForResource":   h.ListTagsForResource,
	}
	if f, ok := ops[op]; ok {
		f(w, r)
		return
	}
	writeErr(w, "UnknownOperationException", "opensnack does not implement EventBridge operation "+op)
}

// ─── event buses ───────────────────────────────────────────────────────

var busNameRE = regexp.MustCompile(`^[/\.\-_A-Za-z0-9]{1,256}$`)

// busName accepts a bus name or ARN.
func busName(s string) string {
	if s == "" {
		return defaultBus
	}
	if i := strings.Index(s, ":event-bus/"); i >= 0 && strings.HasPrefix(s, "arn:") {
		return s[i+len(":event-bus/"):]
	}
	return s
}

func busArn(r *http.Request, name string) string {
	return "arn:aws:events:" + util.Region(r) + ":" + util.AccountID(r) + ":event-bus/" + name
}

func (h *Handler) getBus(r *http.Request, name string) (*busDoc, bool) {
	res, err := h.Store.Get(name, service, typeBus, util.NamespaceFromHeader(r))
	if err == nil && res != nil {
		var b busDoc
		if json.Unmarshal(res.Attributes, &b) == nil {
			return &b, true
		}
	}
	if name == defaultBus {
		return &busDoc{Name: defaultBus, Arn: busArn(r, defaultBus), Implicit: true}, true
	}
	return nil, false
}

func (h *Handler) putBus(r *http.Request, b *busDoc, create bool) error {
	attrs, _ := json.Marshal(b)
	res := &resource.Resource{ID: b.Name, Namespace: util.NamespaceFromHeader(r), Service: service, Type: typeBus, Attributes: attrs}
	if create {
		return h.Store.Create(res)
	}
	return h.Store.Update(res)
}

func (h *Handler) busExists(r *http.Request, name string) bool {
	_, ok := h.getBus(r, name)
	return ok
}

func (h *Handler) CreateEventBus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name             string
		Description      *string
		KmsKeyIdentifier *string
		EventSourceName  *string
		DeadLetterConfig json.RawMessage
		LogConfig        json.RawMessage
		Tags             []tag
	}
	if !decode(w, r, &in) {
		return
	}
	if !busNameRE.MatchString(in.Name) || in.Name == defaultBus || strings.Contains(in.Name, "/") && in.EventSourceName == nil {
		writeErr(w, "ValidationException", "Event bus name is not valid.")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busExists(r, in.Name) {
		writeErr(w, "ResourceAlreadyExistsException", "Event bus "+in.Name+" already exists.")
		return
	}
	now := epoch(h.now())
	b := &busDoc{
		Name: in.Name, Arn: busArn(r, in.Name), Description: in.Description,
		KmsKeyIdentifier: in.KmsKeyIdentifier, EventSourceName: in.EventSourceName,
		DeadLetterConfig: in.DeadLetterConfig, LogConfig: in.LogConfig,
		Tags: tagMap(nil, in.Tags), CreationTime: now, LastModifiedTime: now,
	}
	if err := h.putBus(r, b, true); err != nil {
		writeErr(w, "InternalException", err.Error())
		return
	}
	out := map[string]any{"EventBusArn": b.Arn}
	if b.Description != nil {
		out["Description"] = *b.Description
	}
	if b.KmsKeyIdentifier != nil {
		out["KmsKeyIdentifier"] = *b.KmsKeyIdentifier
	}
	if len(b.DeadLetterConfig) > 0 {
		out["DeadLetterConfig"] = b.DeadLetterConfig
	}
	if len(b.LogConfig) > 0 {
		out["LogConfig"] = b.LogConfig
	}
	writeJSON(w, http.StatusOK, out)
}

func busOut(b *busDoc) map[string]any {
	out := map[string]any{"Name": b.Name, "Arn": b.Arn}
	if b.Description != nil {
		out["Description"] = *b.Description
	}
	if b.KmsKeyIdentifier != nil {
		out["KmsKeyIdentifier"] = *b.KmsKeyIdentifier
	}
	if len(b.DeadLetterConfig) > 0 {
		out["DeadLetterConfig"] = b.DeadLetterConfig
	}
	if len(b.LogConfig) > 0 {
		out["LogConfig"] = b.LogConfig
	}
	if b.CreationTime != 0 {
		out["CreationTime"] = b.CreationTime
		out["LastModifiedTime"] = b.LastModifiedTime
	}
	return out
}

func (h *Handler) DescribeEventBus(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if !decode(w, r, &in) {
		return
	}
	name := busName(in.Name)
	b, ok := h.getBus(r, name)
	if !ok {
		writeErr(w, "ResourceNotFoundException", "Event bus "+name+" does not exist.")
		return
	}
	writeJSON(w, http.StatusOK, busOut(b))
}

func (h *Handler) UpdateEventBus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name             string
		Description      *string
		KmsKeyIdentifier *string
		DeadLetterConfig json.RawMessage
		LogConfig        json.RawMessage
	}
	if !decode(w, r, &in) {
		return
	}
	name := busName(in.Name)
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.getBus(r, name)
	if !ok {
		writeErr(w, "ResourceNotFoundException", "Event bus "+name+" does not exist.")
		return
	}
	// Omitted fields are cleared, as AWS does.
	if in.Description != nil && *in.Description == "" {
		in.Description = nil
	}
	b.Description, b.KmsKeyIdentifier = in.Description, in.KmsKeyIdentifier
	b.DeadLetterConfig, b.LogConfig = in.DeadLetterConfig, in.LogConfig
	b.LastModifiedTime = epoch(h.now())
	create := b.Implicit
	if create {
		if _, err := h.Store.Get(name, service, typeBus, util.NamespaceFromHeader(r)); err == nil {
			create = false
		}
	}
	if err := h.putBus(r, b, create); err != nil {
		writeErr(w, "InternalException", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, busOut(b))
}

func (h *Handler) ListEventBuses(w http.ResponseWriter, r *http.Request) {
	var in struct {
		NamePrefix string
		NextToken  string
		Limit      int
	}
	if !decode(w, r, &in) {
		return
	}
	rows, _ := h.Store.List(service, typeBus, util.NamespaceFromHeader(r))
	buses := map[string]*busDoc{}
	def, _ := h.getBus(r, defaultBus)
	buses[defaultBus] = def
	for _, row := range rows {
		var b busDoc
		if json.Unmarshal(row.Attributes, &b) == nil {
			buses[b.Name] = &b
		}
	}
	var names []string
	for n := range buses {
		if strings.HasPrefix(n, in.NamePrefix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	page, next := paginate(names, in.NextToken, in.Limit)
	list := []map[string]any{}
	for _, n := range page {
		list = append(list, busOut(buses[n]))
	}
	out := map[string]any{"EventBuses": list}
	if next != "" {
		out["NextToken"] = next
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteEventBus(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if !decode(w, r, &in) {
		return
	}
	name := busName(in.Name)
	if name == defaultBus {
		writeErr(w, "ValidationException", "Cannot delete event bus default.")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ns := util.NamespaceFromHeader(r)
	// Rules go with their bus, so nothing is left behind.
	for _, rule := range h.rulesOnBus(r, name) {
		h.Store.Delete(ruleID(name, rule.Name), service, typeRule, ns)
	}
	h.Store.Delete(name, service, typeBus, ns)
	writeJSON(w, http.StatusOK, map[string]any{})
}

func paginate(items []string, token string, limit int) ([]string, string) {
	start, _ := strconv.Atoi(token)
	if start < 0 || start > len(items) {
		start = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	end := start + limit
	if end >= len(items) {
		return items[start:], ""
	}
	return items[start:end], strconv.Itoa(end)
}

// ─── rules ─────────────────────────────────────────────────────────────

var ruleNameRE = regexp.MustCompile(`^[\.\-_A-Za-z0-9]{1,64}$`)

func ruleArn(r *http.Request, bus, name string) string {
	res := "rule/" + name
	if bus != defaultBus {
		res = "rule/" + bus + "/" + name
	}
	return "arn:aws:events:" + util.Region(r) + ":" + util.AccountID(r) + ":" + res
}

func (h *Handler) getRule(r *http.Request, bus, name string) (*ruleDoc, bool) {
	res, err := h.Store.Get(ruleID(bus, name), service, typeRule, util.NamespaceFromHeader(r))
	if err != nil || res == nil {
		return nil, false
	}
	var d ruleDoc
	if json.Unmarshal(res.Attributes, &d) != nil {
		return nil, false
	}
	return &d, true
}

func (h *Handler) saveRule(r *http.Request, d *ruleDoc, create bool) error {
	attrs, _ := json.Marshal(d)
	res := &resource.Resource{ID: ruleID(d.EventBusName, d.Name), Namespace: util.NamespaceFromHeader(r), Service: service, Type: typeRule, Attributes: attrs}
	if create {
		return h.Store.Create(res)
	}
	return h.Store.Update(res)
}

func (h *Handler) rulesOnBus(r *http.Request, bus string) []ruleDoc {
	return rulesOnBus(h.Store, util.NamespaceFromHeader(r), bus)
}

func rulesOnBus(store resource.Store, scope, bus string) []ruleDoc {
	rows, _ := store.List(service, typeRule, scope)
	var out []ruleDoc
	for _, row := range rows {
		if !strings.HasPrefix(row.ID, bus+"/") {
			continue
		}
		var d ruleDoc
		if json.Unmarshal(row.Attributes, &d) == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *Handler) PutRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name               string
		EventBusName       string
		EventPattern       *string
		ScheduleExpression *string
		State              string
		Description        *string
		RoleArn            *string
		Tags               []tag
	}
	if !decode(w, r, &in) {
		return
	}
	if !ruleNameRE.MatchString(in.Name) {
		writeErr(w, "ValidationException", "1 validation error detected: Value '"+in.Name+"' at 'name' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\.\\-_A-Za-z0-9]+")
		return
	}
	if in.EventPattern != nil && *in.EventPattern == "" {
		in.EventPattern = nil
	}
	if in.ScheduleExpression != nil && *in.ScheduleExpression == "" {
		in.ScheduleExpression = nil
	}
	if in.Description != nil && *in.Description == "" {
		in.Description = nil
	}
	if in.EventPattern == nil && in.ScheduleExpression == nil {
		writeErr(w, "ValidationException", "Parameter(s) EventPattern or ScheduleExpression must be specified.")
		return
	}
	if in.EventPattern != nil {
		if _, err := CompilePattern(*in.EventPattern); err != nil {
			writeErr(w, "InvalidEventPatternException", err.Error())
			return
		}
	}
	bus := busName(in.EventBusName)
	if in.ScheduleExpression != nil {
		if _, err := ParseSchedule(*in.ScheduleExpression); err != nil {
			writeErr(w, "ValidationException", err.Error())
			return
		}
		if bus != defaultBus {
			writeErr(w, "ValidationException", "ScheduleExpression is supported only on the default event bus.")
			return
		}
	}
	switch in.State {
	case "":
		in.State = stateEnabled
	case stateEnabled, stateDisabled, "ENABLED_WITH_ALL_CLOUDTRAIL_MANAGEMENT_EVENTS":
	default:
		writeErr(w, "ValidationException", "Parameter State is not valid.")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.busExists(r, bus) {
		writeErr(w, "ResourceNotFoundException", "Event bus "+bus+" does not exist.")
		return
	}
	d, exists := h.getRule(r, bus, in.Name)
	if !exists {
		d = &ruleDoc{Name: in.Name, EventBusName: bus, Arn: ruleArn(r, bus, in.Name), CreatedBy: util.AccountID(r), Tags: tagMap(nil, in.Tags)}
	}
	// PutRule replaces the rule's definition; tags and targets stay.
	d.EventPattern, d.ScheduleExpression = in.EventPattern, in.ScheduleExpression
	d.State, d.Description, d.RoleArn = in.State, in.Description, in.RoleArn
	if err := h.saveRule(r, d, !exists); err != nil {
		writeErr(w, "InternalException", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"RuleArn": d.Arn})
}

func ruleOut(d *ruleDoc) map[string]any {
	out := map[string]any{
		"Name": d.Name, "Arn": d.Arn, "EventBusName": d.EventBusName, "State": d.State,
	}
	if d.EventPattern != nil {
		out["EventPattern"] = *d.EventPattern
	}
	if d.ScheduleExpression != nil {
		out["ScheduleExpression"] = *d.ScheduleExpression
	}
	if d.Description != nil {
		out["Description"] = *d.Description
	}
	if d.RoleArn != nil {
		out["RoleArn"] = *d.RoleArn
	}
	return out
}

type ruleRef struct {
	Name         string
	EventBusName string
}

func (h *Handler) DescribeRule(w http.ResponseWriter, r *http.Request) {
	var in ruleRef
	if !decode(w, r, &in) {
		return
	}
	bus := busName(in.EventBusName)
	d, ok := h.getRule(r, bus, in.Name)
	if !ok {
		notFoundRule(w, in.Name, bus)
		return
	}
	out := ruleOut(d)
	out["CreatedBy"] = d.CreatedBy
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
	var in struct {
		NamePrefix   string
		EventBusName string
		NextToken    string
		Limit        int
	}
	if !decode(w, r, &in) {
		return
	}
	bus := busName(in.EventBusName)
	if !h.busExists(r, bus) {
		writeErr(w, "ResourceNotFoundException", "Event bus "+bus+" does not exist.")
		return
	}
	byName := map[string]ruleDoc{}
	var names []string
	for _, d := range h.rulesOnBus(r, bus) {
		if strings.HasPrefix(d.Name, in.NamePrefix) {
			byName[d.Name] = d
			names = append(names, d.Name)
		}
	}
	page, next := paginate(names, in.NextToken, in.Limit)
	list := []map[string]any{}
	for _, n := range page {
		d := byName[n]
		list = append(list, ruleOut(&d))
	}
	out := map[string]any{"Rules": list}
	if next != "" {
		out["NextToken"] = next
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         string
		EventBusName string
		Force        bool
	}
	if !decode(w, r, &in) {
		return
	}
	bus := busName(in.EventBusName)
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.getRule(r, bus, in.Name)
	if !ok {
		// AWS treats deleting a missing rule as success.
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if len(d.Targets) > 0 {
		writeErr(w, "ValidationException", "Rule can't be deleted since it has targets.")
		return
	}
	h.Store.Delete(ruleID(bus, in.Name), service, typeRule, util.NamespaceFromHeader(r))
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *Handler) setState(state string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in ruleRef
		if !decode(w, r, &in) {
			return
		}
		bus := busName(in.EventBusName)
		h.mu.Lock()
		defer h.mu.Unlock()
		d, ok := h.getRule(r, bus, in.Name)
		if !ok {
			notFoundRule(w, in.Name, bus)
			return
		}
		d.State = state
		if err := h.saveRule(r, d, false); err != nil {
			writeErr(w, "InternalException", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}
}

// ─── targets ───────────────────────────────────────────────────────────

var targetIDRE = regexp.MustCompile(`^[\.\-_A-Za-z0-9]{1,64}$`)

func parseTarget(raw json.RawMessage) (Target, error) {
	var t Target
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, err
	}
	t.Raw = raw
	return t, nil
}

func validateTarget(t Target) string {
	if !targetIDRE.MatchString(t.Id) {
		return "Parameter Id is not valid."
	}
	if !strings.HasPrefix(t.Arn, "arn:") {
		return "Parameter " + t.Arn + " is not valid. Reason: Provided Arn is not in correct format."
	}
	n := 0
	if t.Input != nil {
		n++
		if !json.Valid([]byte(*t.Input)) {
			return "Input is not valid JSON text."
		}
	}
	if t.InputPath != nil {
		n++
		if _, err := parsePath(*t.InputPath); err != nil {
			return "InputPath is not a valid JSON path."
		}
	}
	if t.InputTransformer != nil {
		n++
		if t.InputTransformer.InputTemplate == "" {
			return "InputTemplate is required in InputTransformer."
		}
		for k, p := range t.InputTransformer.InputPathsMap {
			if strings.HasPrefix(k, "aws.") {
				return "InputPathsMap key " + k + " is reserved."
			}
			if _, err := parsePath(p); err != nil {
				return "InputPathsMap value for " + k + " is not a valid JSON path."
			}
		}
	}
	if n > 1 {
		return "Only one of Input, InputPath, or InputTransformer must be provided."
	}
	return ""
}

func (h *Handler) PutTargets(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rule         string
		EventBusName string
		Targets      []json.RawMessage
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Targets) == 0 || len(in.Targets) > 10 {
		writeErr(w, "ValidationException", "Targets must contain between 1 and 10 items.")
		return
	}
	var targets []Target
	for _, raw := range in.Targets {
		t, err := parseTarget(raw)
		if err != nil {
			writeErr(w, "ValidationException", "Target is not valid: "+err.Error())
			return
		}
		if msg := validateTarget(t); msg != "" {
			writeErr(w, "ValidationException", msg)
			return
		}
		targets = append(targets, t)
	}
	bus := busName(in.EventBusName)
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.getRule(r, bus, in.Rule)
	if !ok {
		notFoundRule(w, in.Rule, bus)
		return
	}
	existing := map[string]int{}
	for i, raw := range d.Targets {
		if t, err := parseTarget(raw); err == nil {
			existing[t.Id] = i
		}
	}
	failed := []map[string]string{}
	for _, t := range targets {
		if i, ok := existing[t.Id]; ok {
			d.Targets[i] = t.Raw
			continue
		}
		if len(d.Targets) >= maxTargets {
			failed = append(failed, map[string]string{
				"TargetId": t.Id, "ErrorCode": "LimitExceededException",
				"ErrorMessage": fmt.Sprintf("The requested resource exceeds the maximum number allowed (%d targets per rule).", maxTargets),
			})
			continue
		}
		existing[t.Id] = len(d.Targets)
		d.Targets = append(d.Targets, t.Raw)
	}
	if err := h.saveRule(r, d, false); err != nil {
		writeErr(w, "InternalException", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"FailedEntryCount": len(failed), "FailedEntries": failed})
}

func (h *Handler) RemoveTargets(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rule         string
		EventBusName string
		Ids          []string
		Force        bool
	}
	if !decode(w, r, &in) {
		return
	}
	bus := busName(in.EventBusName)
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.getRule(r, bus, in.Rule)
	if !ok {
		notFoundRule(w, in.Rule, bus)
		return
	}
	drop := map[string]bool{}
	for _, id := range in.Ids {
		drop[id] = true
	}
	var keep []json.RawMessage
	for _, raw := range d.Targets {
		if t, err := parseTarget(raw); err == nil && drop[t.Id] {
			continue
		}
		keep = append(keep, raw)
	}
	d.Targets = keep
	if err := h.saveRule(r, d, false); err != nil {
		writeErr(w, "InternalException", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"FailedEntryCount": 0, "FailedEntries": []any{}})
}

func (h *Handler) ListTargetsByRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rule         string
		EventBusName string
		NextToken    string
		Limit        int
	}
	if !decode(w, r, &in) {
		return
	}
	bus := busName(in.EventBusName)
	d, ok := h.getRule(r, bus, in.Rule)
	if !ok {
		notFoundRule(w, in.Rule, bus)
		return
	}
	idx := make([]string, len(d.Targets))
	for i := range d.Targets {
		idx[i] = strconv.Itoa(i)
	}
	page, next := paginate(idx, in.NextToken, in.Limit)
	list := []json.RawMessage{}
	for _, s := range page {
		i, _ := strconv.Atoi(s)
		list = append(list, d.Targets[i])
	}
	out := map[string]any{"Targets": list}
	if next != "" {
		out["NextToken"] = next
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) ListRuleNamesByTarget(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TargetArn    string
		EventBusName string
	}
	if !decode(w, r, &in) {
		return
	}
	names := []string{}
	for _, d := range h.rulesOnBus(r, busName(in.EventBusName)) {
		for _, raw := range d.Targets {
			if t, err := parseTarget(raw); err == nil && t.Arn == in.TargetArn {
				names = append(names, d.Name)
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"RuleNames": names})
}

// ─── tags ──────────────────────────────────────────────────────────────

type tag struct {
	Key   string
	Value string
}

func tagMap(m map[string]string, tags []tag) map[string]string {
	if len(tags) == 0 {
		return m
	}
	if m == nil {
		m = map[string]string{}
	}
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

// withTagged loads the bus or rule an ARN names, lets fn change its tags,
// and saves it. It writes the error response itself.
func (h *Handler) withTagged(w http.ResponseWriter, r *http.Request, arn string, fn func(map[string]string) map[string]string) bool {
	_, _, res, ok := arnParts(arn)
	switch {
	case ok && strings.HasPrefix(res, "event-bus/"):
		name := strings.TrimPrefix(res, "event-bus/")
		b, found := h.getBus(r, name)
		if !found {
			writeErr(w, "ResourceNotFoundException", "Event bus "+name+" does not exist.")
			return false
		}
		b.Tags = fn(b.Tags)
		create := false
		if b.Implicit {
			_, err := h.Store.Get(name, service, typeBus, util.NamespaceFromHeader(r))
			create = err != nil
		}
		if err := h.putBus(r, b, create); err != nil {
			writeErr(w, "InternalException", err.Error())
			return false
		}
		return true
	case ok && strings.HasPrefix(res, "rule/"):
		rest := strings.TrimPrefix(res, "rule/")
		bus, name := defaultBus, rest
		if i := strings.LastIndex(rest, "/"); i >= 0 {
			bus, name = rest[:i], rest[i+1:]
		}
		d, found := h.getRule(r, bus, name)
		if !found {
			notFoundRule(w, name, bus)
			return false
		}
		d.Tags = fn(d.Tags)
		if err := h.saveRule(r, d, false); err != nil {
			writeErr(w, "InternalException", err.Error())
			return false
		}
		return true
	}
	writeErr(w, "ValidationException", "Parameter ResourceARN is not valid.")
	return false
}

func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ResourceARN string
		Tags        []tag
	}
	if !decode(w, r, &in) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.withTagged(w, r, in.ResourceARN, func(m map[string]string) map[string]string { return tagMap(m, in.Tags) }) {
		writeJSON(w, http.StatusOK, map[string]any{})
	}
}

func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ResourceARN string
		TagKeys     []string
	}
	if !decode(w, r, &in) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.withTagged(w, r, in.ResourceARN, func(m map[string]string) map[string]string {
		for _, k := range in.TagKeys {
			delete(m, k)
		}
		return m
	}) {
		writeJSON(w, http.StatusOK, map[string]any{})
	}
}

func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var in struct{ ResourceARN string }
	if !decode(w, r, &in) {
		return
	}
	var tags map[string]string
	if !h.withTaggedRead(w, r, in.ResourceARN, &tags) {
		return
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	list := []tag{}
	for _, k := range keys {
		list = append(list, tag{k, tags[k]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"Tags": list})
}

func (h *Handler) withTaggedRead(w http.ResponseWriter, r *http.Request, arn string, out *map[string]string) bool {
	_, _, res, ok := arnParts(arn)
	switch {
	case ok && strings.HasPrefix(res, "event-bus/"):
		name := strings.TrimPrefix(res, "event-bus/")
		b, found := h.getBus(r, name)
		if !found {
			writeErr(w, "ResourceNotFoundException", "Event bus "+name+" does not exist.")
			return false
		}
		*out = b.Tags
		return true
	case ok && strings.HasPrefix(res, "rule/"):
		rest := strings.TrimPrefix(res, "rule/")
		bus, name := defaultBus, rest
		if i := strings.LastIndex(rest, "/"); i >= 0 {
			bus, name = rest[:i], rest[i+1:]
		}
		d, found := h.getRule(r, bus, name)
		if !found {
			notFoundRule(w, name, bus)
			return false
		}
		*out = d.Tags
		return true
	}
	writeErr(w, "ValidationException", "Parameter ResourceARN is not valid.")
	return false
}

// ─── events ────────────────────────────────────────────────────────────

// Envelope is the event every target sees, in AWS's field order.
type Envelope struct {
	Version    string          `json:"version"`
	ID         string          `json:"id"`
	DetailType string          `json:"detail-type"`
	Source     string          `json:"source"`
	Account    string          `json:"account"`
	Time       string          `json:"time"`
	Region     string          `json:"region"`
	Resources  []string        `json:"resources"`
	Detail     json.RawMessage `json:"detail"`
}

func (h *Handler) PutEvents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Entries []struct {
			Time         *float64
			Source       *string
			Resources    []string
			DetailType   *string
			Detail       *string
			EventBusName string
		}
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Entries) == 0 || len(in.Entries) > 10 {
		writeErr(w, "ValidationException", "1 validation error detected: Value at 'entries' failed to satisfy constraint: Member must have length between 1 and 10")
		return
	}
	scope := Scope{Namespace: util.IdentityFrom(r).Namespace, Region: util.Region(r), AccountID: util.AccountID(r)}
	storeScope := util.NamespaceFromHeader(r)
	results := []map[string]string{}
	failed := 0
	fail := func(code, msg string) {
		failed++
		results = append(results, map[string]string{"ErrorCode": code, "ErrorMessage": msg})
	}
	for _, e := range in.Entries {
		switch {
		case e.Source == nil || *e.Source == "":
			fail("InvalidArgument", "Parameter Source is not valid. Reason: Source is a required argument.")
			continue
		case e.DetailType == nil || *e.DetailType == "":
			fail("InvalidArgument", "Parameter DetailType is not valid. Reason: DetailType is a required argument.")
			continue
		case e.Detail == nil || *e.Detail == "":
			fail("InvalidArgument", "Parameter Detail is not valid. Reason: Detail is a required argument.")
			continue
		case strings.HasPrefix(*e.Source, "aws."):
			fail("NotAuthorizedForSourceException", "Not authorized for the source.")
			continue
		}
		var detail map[string]json.RawMessage
		if json.Unmarshal([]byte(*e.Detail), &detail) != nil {
			fail("MalformedDetail", "Detail is malformed.")
			continue
		}
		bus := busName(e.EventBusName)
		if !h.busExists(r, bus) {
			fail("InvalidArgument", "Event bus "+bus+" does not exist.")
			continue
		}
		at := h.now()
		if e.Time != nil {
			at = time.UnixMilli(int64(*e.Time * 1000)).UTC()
		}
		env := Envelope{
			Version: "0", ID: uuid.NewString(), DetailType: *e.DetailType, Source: *e.Source,
			Account: scope.AccountID, Time: at.Format(time.RFC3339), Region: scope.Region,
			Resources: e.Resources, Detail: json.RawMessage(*e.Detail),
		}
		if env.Resources == nil {
			env.Resources = []string{}
		}
		h.fanOut(r.Context(), scope, rulesOnBus(h.Store, storeScope, bus), env, false)
		results = append(results, map[string]string{"EventId": env.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"FailedEntryCount": failed, "Entries": results})
}

// fanOut sends one event to the targets of every enabled rule that
// matches it. scheduled says the rules are scheduled ones being fired, which
// match without a pattern.
func (h *Handler) fanOut(ctx context.Context, scope Scope, rules []ruleDoc, env Envelope, scheduled bool) {
	eventJSON, _ := marshalNoEscape(env)
	for _, d := range rules {
		if d.State == stateDisabled {
			continue
		}
		if !scheduled {
			if d.EventPattern == nil {
				continue
			}
			p, err := CompilePattern(*d.EventPattern)
			if err != nil || !p.Match(eventJSON) {
				continue
			}
		}
		tc := TransformContext{RuleArn: d.Arn, RuleName: d.Name, IngestionTime: env.Time}
		for _, raw := range d.Targets {
			t, err := parseTarget(raw)
			if err != nil {
				continue
			}
			payload, err := targetPayload(t, eventJSON, tc)
			del := Delivery{TargetArn: t.Arn, Payload: payload, Target: t, EventID: env.ID, RuleArn: d.Arn}
			if err != nil {
				h.Log.Add(Attempt{Time: h.now(), Namespace: scope.Namespace, Region: scope.Region, RuleArn: d.Arn,
					TargetID: t.Id, TargetArn: t.Arn, EventID: env.ID, Status: "failed", Error: "input transformation: " + err.Error()})
				continue
			}
			h.deliver(ctx, scope, del)
		}
	}
}

func (h *Handler) TestEventPattern(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EventPattern string
		Event        string
	}
	if !decode(w, r, &in) {
		return
	}
	p, err := CompilePattern(in.EventPattern)
	if err != nil {
		writeErr(w, "InvalidEventPatternException", err.Error())
		return
	}
	if !json.Valid([]byte(in.Event)) {
		writeErr(w, "ValidationException", "Parameter Event is not valid.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"Result": p.Match([]byte(in.Event))})
}
