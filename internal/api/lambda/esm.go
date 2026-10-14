// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"opensnack/internal/util"
)

// Event source mappings are stored only: nothing polls the sources yet. A
// poller can List the "event-source-mapping" rows and Invoke through the
// handler's Executor.

// esmInput is the body of Create and Update. Pointers tell "not given"
// apart from zero values.
type esmInput struct {
	EventSourceArn                 string
	FunctionName                   *string
	Enabled                        *bool
	BatchSize                      *int
	MaximumBatchingWindowInSeconds *int
	ParallelizationFactor          *int
	StartingPosition               string
	StartingPositionTimestamp      *float64
	MaximumRecordAgeInSeconds      *int
	BisectBatchOnFunctionError     *bool
	MaximumRetryAttempts           *int
	TumblingWindowInSeconds        *int
	DestinationConfig              *DestinationConfig
	FilterCriteria                 *FilterCriteria
	FunctionResponseTypes          *[]string
	ScalingConfig                  *ScalingConfig
	KMSKeyArn                      *string
	Topics                         []string
	Queues                         []string
	Tags                           map[string]string

	MetricsConfig                       any
	SourceAccessConfigurations          any
	SelfManagedEventSource              any
	AmazonManagedKafkaEventSourceConfig any
	SelfManagedKafkaEventSourceConfig   any
	DocumentDBEventSourceConfig         any
	ProvisionedPollerConfig             any
}

func esmKind(arn string) string {
	switch {
	case strings.Contains(arn, ":sqs:"):
		return "sqs"
	case strings.Contains(arn, ":kinesis:"), strings.Contains(arn, ":dynamodb:"):
		return "stream"
	default:
		return "other"
	}
}

func esmARN(r *http.Request, id string) string {
	return "arn:aws:lambda:" + util.Region(r) + ":" + util.AccountID(r) + ":event-source-mapping:" + id
}

func intp(v int) *int    { return &v }
func boolp(v bool) *bool { return &v }

// esmFunction resolves a FunctionName member to the ARN the mapping reports.
func (h *Handler) esmFunction(r *http.Request, name string) (string, error) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		return "", err
	}
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		return "", err
	}
	if err := h.checkQualifier(r, ref.Name, ref.Qualifier); err != nil {
		return "", err
	}
	return qualifiedARN(r, ref.Name, ref.Qualifier), nil
}

// apply writes the given fields into c and validates the result.
func (in *esmInput) apply(c *EventSourceMappingConfiguration, kind string) error {
	if in.BatchSize != nil {
		c.BatchSize = *in.BatchSize
	}
	if in.MaximumBatchingWindowInSeconds != nil {
		c.MaximumBatchingWindowInSeconds = *in.MaximumBatchingWindowInSeconds
	}
	if in.FilterCriteria != nil {
		if len(in.FilterCriteria.Filters) == 0 {
			c.FilterCriteria = nil
		} else {
			c.FilterCriteria = in.FilterCriteria
		}
	}
	if in.FunctionResponseTypes != nil {
		c.FunctionResponseTypes = *in.FunctionResponseTypes
	}
	if in.ScalingConfig != nil {
		if in.ScalingConfig.MaximumConcurrency == 0 {
			c.ScalingConfig = nil
		} else {
			c.ScalingConfig = in.ScalingConfig
		}
	}
	if in.KMSKeyArn != nil {
		c.KMSKeyArn = *in.KMSKeyArn
	}
	if kind == "stream" {
		if in.ParallelizationFactor != nil {
			c.ParallelizationFactor = in.ParallelizationFactor
		}
		if in.MaximumRecordAgeInSeconds != nil {
			c.MaximumRecordAgeInSeconds = in.MaximumRecordAgeInSeconds
		}
		if in.BisectBatchOnFunctionError != nil {
			c.BisectBatchOnFunctionError = in.BisectBatchOnFunctionError
		}
		if in.MaximumRetryAttempts != nil {
			c.MaximumRetryAttempts = in.MaximumRetryAttempts
		}
		if in.TumblingWindowInSeconds != nil {
			c.TumblingWindowInSeconds = in.TumblingWindowInSeconds
		}
		if in.DestinationConfig != nil {
			dc := *in.DestinationConfig
			if dc.OnFailure == nil {
				dc.OnFailure = &Destination{}
			}
			c.DestinationConfig = &dc
		}
	}
	for _, v := range []struct {
		dst *any
		src any
	}{
		{&c.MetricsConfig, in.MetricsConfig},
		{&c.SourceAccessConfigurations, in.SourceAccessConfigurations},
		{&c.SelfManagedEventSource, in.SelfManagedEventSource},
		{&c.AmazonManagedKafkaEventSourceConfig, in.AmazonManagedKafkaEventSourceConfig},
		{&c.SelfManagedKafkaEventSourceConfig, in.SelfManagedKafkaEventSourceConfig},
		{&c.DocumentDBEventSourceConfig, in.DocumentDBEventSourceConfig},
		{&c.ProvisionedPollerConfig, in.ProvisionedPollerConfig},
	} {
		if v.src != nil {
			*v.dst = v.src
		}
	}

	switch kind {
	case "sqs":
		if c.BatchSize < 1 || c.BatchSize > 10000 {
			return errInvalid("BatchSize must be between 1 and 10000 for an SQS event source.")
		}
		if c.BatchSize > 10 && c.MaximumBatchingWindowInSeconds < 1 {
			return errInvalid("Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10")
		}
	default:
		if c.BatchSize < 1 || c.BatchSize > 10000 {
			return errInvalid("BatchSize must be between 1 and 10000.")
		}
	}
	if c.MaximumBatchingWindowInSeconds < 0 || c.MaximumBatchingWindowInSeconds > 300 {
		return errInvalid("MaximumBatchingWindowInSeconds must be between 0 and 300.")
	}
	return nil
}

func esmState(enabled bool) string {
	if enabled {
		return "Enabled"
	}
	return "Disabled"
}

// CreateEventSourceMapping  POST /2015-03-31/event-source-mappings
func (h *Handler) CreateEventSourceMapping(w http.ResponseWriter, r *http.Request) {
	var in esmInput
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.FunctionName == nil {
		writeError(w, errValidation("1 validation error detected: Value null at 'functionName' failed to satisfy constraint: Member must not be null"))
		return
	}
	if err := checkTags(in.Tags); err != nil {
		writeError(w, err)
		return
	}
	kind := esmKind(in.EventSourceArn)
	if kind == "stream" && in.StartingPosition == "" {
		writeError(w, errInvalid("StartingPosition is required for a stream event source."))
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	fnARN, err := h.esmFunction(r, *in.FunctionName)
	if err != nil {
		writeError(w, err)
		return
	}
	if in.EventSourceArn != "" {
		rows, _ := h.Store.List(service, typeESM, ns(r))
		for _, row := range rows {
			var other esmRecord
			if json.Unmarshal(row.Attributes, &other) == nil &&
				other.Config.EventSourceArn == in.EventSourceArn && other.Config.FunctionArn == fnARN {
				writeError(w, errConflict(fmt.Sprintf("The event source arn (%s) and function (%s) provided mapping already exists. Please update or delete the existing mapping with UUID %s",
					in.EventSourceArn, fnARN, other.Config.UUID)))
				return
			}
		}
	}

	id := uuid.NewString()
	c := EventSourceMappingConfiguration{
		UUID:                      id,
		EventSourceMappingArn:     esmARN(r, id),
		EventSourceArn:            in.EventSourceArn,
		FunctionArn:               fnARN,
		FunctionResponseTypes:     []string{},
		StartingPosition:          in.StartingPosition,
		StartingPositionTimestamp: in.StartingPositionTimestamp,
		Topics:                    in.Topics,
		Queues:                    in.Queues,
		StateTransitionReason:     "USER_INITIATED",
	}
	switch kind {
	case "sqs":
		c.BatchSize = 10
	case "stream":
		c.BatchSize = 100
		c.ParallelizationFactor = intp(1)
		c.MaximumRecordAgeInSeconds = intp(-1)
		c.BisectBatchOnFunctionError = boolp(false)
		c.MaximumRetryAttempts = intp(-1)
		c.TumblingWindowInSeconds = intp(0)
		c.DestinationConfig = &DestinationConfig{OnFailure: &Destination{}}
		c.LastProcessingResult = "No records processed"
	default:
		c.BatchSize = 100
	}
	if err := in.apply(&c, kind); err != nil {
		writeError(w, err)
		return
	}
	c.State = esmState(in.Enabled == nil || *in.Enabled)
	c.LastModified = epochNow()

	if err := h.put(r, typeESM, id, esmRecord{Config: c, Tags: in.Tags}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, c)
}

func (h *Handler) loadESM(r *http.Request, id string) (*esmRecord, error) {
	var rec esmRecord
	if !h.get(r, typeESM, id, &rec) {
		return nil, errNotFound("The resource you requested does not exist.")
	}
	return &rec, nil
}

// GetEventSourceMapping  GET /2015-03-31/event-source-mappings/{uuid}
func (h *Handler) GetEventSourceMapping(w http.ResponseWriter, r *http.Request, id string) {
	rec, err := h.loadESM(r, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec.Config)
}

// UpdateEventSourceMapping  PUT /2015-03-31/event-source-mappings/{uuid}
func (h *Handler) UpdateEventSourceMapping(w http.ResponseWriter, r *http.Request, id string) {
	var in esmInput
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, err := h.loadESM(r, id)
	if err != nil {
		writeError(w, err)
		return
	}
	c := rec.Config
	if in.FunctionName != nil {
		if c.FunctionArn, err = h.esmFunction(r, *in.FunctionName); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := in.apply(&c, esmKind(c.EventSourceArn)); err != nil {
		writeError(w, err)
		return
	}
	if in.Enabled != nil {
		c.State = esmState(*in.Enabled)
	}
	c.LastModified = epochNow()
	rec.Config = c
	if err := h.put(r, typeESM, id, rec); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, c)
}

// DeleteEventSourceMapping  DELETE /2015-03-31/event-source-mappings/{uuid}
func (h *Handler) DeleteEventSourceMapping(w http.ResponseWriter, r *http.Request, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, err := h.loadESM(r, id)
	if err != nil {
		writeError(w, err)
		return
	}
	h.del(r, typeESM, id)
	c := rec.Config
	c.State = "Deleting"
	writeJSON(w, http.StatusAccepted, c)
}

// ListEventSourceMappings  GET /2015-03-31/event-source-mappings
func (h *Handler) ListEventSourceMappings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("EventSourceArn")
	var fnARN string
	if name := q.Get("FunctionName"); name != "" {
		ref, err := parseFunctionName(r, name)
		if err != nil {
			writeError(w, err)
			return
		}
		fnARN = qualifiedARN(r, ref.Name, ref.Qualifier)
	}
	rows, _ := h.Store.List(service, typeESM, ns(r))
	var out []EventSourceMappingConfiguration
	for _, row := range rows {
		var rec esmRecord
		if json.Unmarshal(row.Attributes, &rec) != nil {
			continue
		}
		c := rec.Config
		if (source == "" || c.EventSourceArn == source) && (fnARN == "" || c.FunctionArn == fnARN) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UUID < out[j].UUID })
	start, end, next := page(r, len(out))
	resp := map[string]any{"EventSourceMappings": append([]EventSourceMappingConfiguration{}, out[start:end]...)}
	if next != "" {
		resp["NextMarker"] = next
	}
	writeJSON(w, http.StatusOK, resp)
}

// EventSourceMappings returns every mapping in a regional scope, for a
// poller to work through.
func (h *Handler) EventSourceMappings(scope string) []EventSourceMappingConfiguration {
	rows, _ := h.Store.List(service, typeESM, scope)
	var out []EventSourceMappingConfiguration
	for _, row := range rows {
		var rec esmRecord
		if json.Unmarshal(row.Attributes, &rec) == nil {
			out = append(out, rec.Config)
		}
	}
	return out
}
