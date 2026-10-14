// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"opensnack/internal/util"
)

// Function URLs are control plane only: the URL has the AWS form
// (https://{id}.lambda-url.{region}.on.aws/) but nothing serves it yet.

type urlRecord struct {
	Config FunctionUrlConfig
}

func checkURLSettings(authType, invokeMode string) error {
	if authType != "NONE" && authType != "AWS_IAM" {
		return errValidation("1 validation error detected: Value '" + authType + "' at 'authType' failed to satisfy constraint: Member must satisfy enum value set: [AWS_IAM, NONE]")
	}
	if invokeMode != "BUFFERED" && invokeMode != "RESPONSE_STREAM" {
		return errValidation("1 validation error detected: Value '" + invokeMode + "' at 'invokeMode' failed to satisfy constraint: Member must satisfy enum value set: [BUFFERED, RESPONSE_STREAM]")
	}
	return nil
}

func normaliseCors(c *Cors) *Cors {
	if c == nil || (!c.AllowCredentials && c.AllowHeaders == nil && c.AllowMethods == nil &&
		c.AllowOrigins == nil && c.ExposeHeaders == nil && c.MaxAge == 0) {
		return nil
	}
	return c
}

// urlRef checks the function and that the qualifier is one a URL can hang
// off: none, or an alias.
func (h *Handler) urlRef(r *http.Request, name string) (fnRef, error) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		return ref, err
	}
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		return ref, err
	}
	if ref.Qualifier == "$LATEST" || isVersion(ref.Qualifier) {
		return ref, errInvalid("Function URL can only be created for $LATEST or alias, but not for version " + ref.Qualifier)
	}
	if ref.Qualifier != "" {
		if _, err := h.loadAlias(r, ref.Name, ref.Qualifier); err != nil {
			return ref, errFunctionNotFound(qualifiedARN(r, ref.Name, ref.Qualifier))
		}
	}
	return ref, nil
}

func urlTime() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
}

// CreateFunctionUrlConfig  POST /2021-10-31/functions/{name}/url
func (h *Handler) CreateFunctionUrlConfig(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		AuthType   string
		Cors       *Cors
		InvokeMode string
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.InvokeMode == "" {
		in.InvokeMode = "BUFFERED"
	}
	if err := checkURLSettings(in.AuthType, in.InvokeMode); err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, err := h.urlRef(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	arn := qualifiedARN(r, ref.Name, ref.Qualifier)
	var existing urlRecord
	if h.get(r, typeURL, policyID(ref), &existing) {
		writeError(w, errConflict("Failed to create function url config for [functionArn = "+arn+"]. Error message:  FunctionUrlConfig exists for this Lambda function"))
		return
	}
	now := urlTime()
	cfg := FunctionUrlConfig{
		FunctionUrl:      "https://" + util.RandomHex(16) + ".lambda-url." + util.Region(r) + ".on.aws/",
		FunctionArn:      arn,
		AuthType:         in.AuthType,
		Cors:             normaliseCors(in.Cors),
		CreationTime:     now,
		LastModifiedTime: now,
		InvokeMode:       in.InvokeMode,
	}
	if err := h.put(r, typeURL, policyID(ref), urlRecord{Config: cfg}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"FunctionUrl":  cfg.FunctionUrl,
		"FunctionArn":  cfg.FunctionArn,
		"AuthType":     cfg.AuthType,
		"Cors":         cfg.Cors,
		"CreationTime": cfg.CreationTime,
		"InvokeMode":   cfg.InvokeMode,
	})
}

// GetFunctionUrlConfig  GET /2021-10-31/functions/{name}/url
func (h *Handler) GetFunctionUrlConfig(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := h.urlRef(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	var rec urlRecord
	if !h.get(r, typeURL, policyID(ref), &rec) {
		writeError(w, errNotFound("The resource you requested does not exist."))
		return
	}
	writeJSON(w, http.StatusOK, rec.Config)
}

// UpdateFunctionUrlConfig  PUT /2021-10-31/functions/{name}/url
func (h *Handler) UpdateFunctionUrlConfig(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		AuthType   string
		Cors       *Cors
		InvokeMode string
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, err := h.urlRef(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	var rec urlRecord
	if !h.get(r, typeURL, policyID(ref), &rec) {
		writeError(w, errNotFound("The resource you requested does not exist."))
		return
	}
	cfg := &rec.Config
	if in.AuthType != "" {
		cfg.AuthType = in.AuthType
	}
	if in.InvokeMode != "" {
		cfg.InvokeMode = in.InvokeMode
	}
	if in.Cors != nil {
		cfg.Cors = normaliseCors(in.Cors)
	}
	if err := checkURLSettings(cfg.AuthType, cfg.InvokeMode); err != nil {
		writeError(w, err)
		return
	}
	cfg.LastModifiedTime = urlTime()
	if err := h.put(r, typeURL, policyID(ref), rec); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// DeleteFunctionUrlConfig  DELETE /2021-10-31/functions/{name}/url
func (h *Handler) DeleteFunctionUrlConfig(w http.ResponseWriter, r *http.Request, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, err := h.urlRef(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	var rec urlRecord
	if !h.get(r, typeURL, policyID(ref), &rec) {
		writeError(w, errNotFound("The resource you requested does not exist."))
		return
	}
	h.del(r, typeURL, policyID(ref))
	noContent(w)
}

// ListFunctionUrlConfigs  GET /2021-10-31/functions/{name}/urls
func (h *Handler) ListFunctionUrlConfigs(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	var configs []FunctionUrlConfig
	for _, row := range h.listPrefixed(r, typeURL, ref.Name) {
		var rec urlRecord
		if json.Unmarshal(row.Attributes, &rec) == nil {
			configs = append(configs, rec.Config)
		}
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].FunctionArn < configs[j].FunctionArn })
	start, end, next := page(r, len(configs))
	out := map[string]any{"FunctionUrlConfigs": append([]FunctionUrlConfig{}, configs[start:end]...)}
	if next != "" {
		out["NextMarker"] = next
	}
	writeJSON(w, http.StatusOK, out)
}
