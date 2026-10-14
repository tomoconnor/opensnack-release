// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"encoding/json"
	"net/http"
	"sort"
)

type eventInvokeInput struct {
	MaximumRetryAttempts     *int
	MaximumEventAgeInSeconds *int
	DestinationConfig        *DestinationConfig
}

func (in eventInvokeInput) check() error {
	if v := in.MaximumRetryAttempts; v != nil && (*v < 0 || *v > 2) {
		return errValidation("1 validation error detected: Value at 'maximumRetryAttempts' failed to satisfy constraint: Member must have value less than or equal to 2")
	}
	if v := in.MaximumEventAgeInSeconds; v != nil && (*v < 60 || *v > 21600) {
		return errValidation("1 validation error detected: Value at 'maximumEventAgeInSeconds' failed to satisfy constraint: Member must have value between 60 and 21600")
	}
	return nil
}

// eventInvokeConfigOut fills the empty destinations AWS reports when none
// are configured.
func eventInvokeConfigOut(c FunctionEventInvokeConfig) FunctionEventInvokeConfig {
	dc := DestinationConfig{OnSuccess: &Destination{}, OnFailure: &Destination{}}
	if c.DestinationConfig != nil {
		if c.DestinationConfig.OnSuccess != nil {
			dc.OnSuccess = c.DestinationConfig.OnSuccess
		}
		if c.DestinationConfig.OnFailure != nil {
			dc.OnFailure = c.DestinationConfig.OnFailure
		}
	}
	c.DestinationConfig = &dc
	return c
}

func (h *Handler) eventInvokeRef(r *http.Request, name string) (fnRef, error) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		return ref, err
	}
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		return ref, err
	}
	return ref, h.checkQualifier(r, ref.Name, ref.Qualifier)
}

func eventInvokeARN(r *http.Request, ref fnRef) string {
	q := ref.Qualifier
	if q == "" {
		q = "$LATEST"
	}
	return qualifiedARN(r, ref.Name, q)
}

// PutFunctionEventInvokeConfig handles PUT (replace) and, with merge, POST
// (UpdateFunctionEventInvokeConfig) on /2019-09-25/functions/{name}/event-invoke-config.
func (h *Handler) PutFunctionEventInvokeConfig(w http.ResponseWriter, r *http.Request, name string, merge bool) {
	var in eventInvokeInput
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if err := in.check(); err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, err := h.eventInvokeRef(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	var cfg FunctionEventInvokeConfig
	exists := h.get(r, typeEventInvoke, policyID(ref), &cfg)
	if merge && !exists {
		writeError(w, errNotFound("The function "+eventInvokeARN(r, ref)+" doesn't have an EventInvokeConfig"))
		return
	}
	if !merge {
		cfg = FunctionEventInvokeConfig{}
	}
	if in.MaximumRetryAttempts != nil {
		cfg.MaximumRetryAttempts = in.MaximumRetryAttempts
	}
	if in.MaximumEventAgeInSeconds != nil {
		cfg.MaximumEventAgeInSeconds = in.MaximumEventAgeInSeconds
	}
	if in.DestinationConfig != nil {
		cfg.DestinationConfig = in.DestinationConfig
	}
	cfg.FunctionArn = eventInvokeARN(r, ref)
	cfg.LastModified = epochNow()
	if err := h.put(r, typeEventInvoke, policyID(ref), cfg); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, eventInvokeConfigOut(cfg))
}

// GetFunctionEventInvokeConfig  GET /2019-09-25/functions/{name}/event-invoke-config
func (h *Handler) GetFunctionEventInvokeConfig(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := h.eventInvokeRef(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	var cfg FunctionEventInvokeConfig
	if !h.get(r, typeEventInvoke, policyID(ref), &cfg) {
		writeError(w, errNotFound("The function "+eventInvokeARN(r, ref)+" doesn't have an EventInvokeConfig"))
		return
	}
	writeJSON(w, http.StatusOK, eventInvokeConfigOut(cfg))
}

// DeleteFunctionEventInvokeConfig  DELETE /2019-09-25/functions/{name}/event-invoke-config
func (h *Handler) DeleteFunctionEventInvokeConfig(w http.ResponseWriter, r *http.Request, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, err := h.eventInvokeRef(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	var cfg FunctionEventInvokeConfig
	if !h.get(r, typeEventInvoke, policyID(ref), &cfg) {
		writeError(w, errNotFound("The function "+eventInvokeARN(r, ref)+" doesn't have an EventInvokeConfig"))
		return
	}
	h.del(r, typeEventInvoke, policyID(ref))
	noContent(w)
}

// ListFunctionEventInvokeConfigs  GET /2019-09-25/functions/{name}/event-invoke-config/list
func (h *Handler) ListFunctionEventInvokeConfigs(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	var configs []FunctionEventInvokeConfig
	for _, row := range h.listPrefixed(r, typeEventInvoke, ref.Name) {
		var c FunctionEventInvokeConfig
		if json.Unmarshal(row.Attributes, &c) == nil {
			configs = append(configs, eventInvokeConfigOut(c))
		}
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].FunctionArn < configs[j].FunctionArn })
	start, end, next := page(r, len(configs))
	out := map[string]any{"FunctionEventInvokeConfigs": append([]FunctionEventInvokeConfig{}, configs[start:end]...)}
	if next != "" {
		out["NextMarker"] = next
	}
	writeJSON(w, http.StatusOK, out)
}
