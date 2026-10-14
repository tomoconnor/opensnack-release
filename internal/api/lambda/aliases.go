// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
)

var aliasNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func checkAliasName(name string) error {
	if !aliasNamePattern.MatchString(name) || isVersion(name) {
		return errValidation("1 validation error detected: Value '" + name +
			"' at 'name' failed to satisfy constraint: Member must satisfy regular expression pattern: (?!^[0-9]+$)([a-zA-Z0-9-_]+)")
	}
	return nil
}

// checkAliasTarget validates the version an alias points at and its
// weighted routing.
func (h *Handler) checkAliasTarget(r *http.Request, name, version string, routing *AliasRoutingConfiguration) error {
	if version != "$LATEST" {
		if !isVersion(version) {
			return errValidation("1 validation error detected: Value '" + version +
				"' at 'functionVersion' failed to satisfy constraint: Member must satisfy regular expression pattern: (\\$LATEST|[0-9]+)")
		}
		if _, err := h.loadVersion(r, name, version); err != nil {
			return err
		}
	}
	if routing == nil {
		return nil
	}
	if len(routing.AdditionalVersionWeights) > 1 {
		return errInvalid("Number of items in AdditionalVersionWeights cannot be greater than 1")
	}
	for v, weight := range routing.AdditionalVersionWeights {
		if weight < 0 || weight > 1 {
			return errValidation(fmt.Sprintf("1 validation error detected: Value '%v' at 'routingConfig.additionalVersionWeights' failed to satisfy constraint: Map value must satisfy constraint: [Member must have value less than or equal to 1.0, Member must have value greater than or equal to 0.0]", weight))
		}
		if version == "$LATEST" {
			return errInvalid("$LATEST is not supported for an alias pointing to more than 1 version")
		}
		if v == version {
			return errInvalid("Invalid function version " + v + ". Function version " + v + " is already included in the routing configuration.")
		}
		if _, err := h.loadVersion(r, name, v); err != nil {
			return err
		}
	}
	return nil
}

func normaliseRouting(rc *AliasRoutingConfiguration) *AliasRoutingConfiguration {
	if rc == nil || len(rc.AdditionalVersionWeights) == 0 {
		return nil
	}
	return rc
}

// CreateAlias  POST /2015-03-31/functions/{name}/aliases
func (h *Handler) CreateAlias(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		Name            string
		FunctionVersion string
		Description     string
		RoutingConfig   *AliasRoutingConfiguration
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	ref, err := parseFunctionName(r, name)
	if err == nil {
		err = ref.requireUnqualified()
	}
	if err == nil {
		err = checkAliasName(in.Name)
	}
	if err != nil {
		writeError(w, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := h.checkAliasTarget(r, ref.Name, in.FunctionVersion, in.RoutingConfig); err != nil {
		writeError(w, err)
		return
	}
	arn := qualifiedARN(r, ref.Name, in.Name)
	if _, err := h.loadAlias(r, ref.Name, in.Name); err == nil {
		writeError(w, errConflict("Alias already exists: "+arn))
		return
	}
	a := AliasConfiguration{
		AliasArn:        arn,
		Name:            in.Name,
		FunctionVersion: in.FunctionVersion,
		Description:     in.Description,
		RoutingConfig:   normaliseRouting(in.RoutingConfig),
		RevisionId:      newRevisionID(),
	}
	if err := h.put(r, typeAlias, ref.Name+":"+in.Name, a); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// GetAlias  GET /2015-03-31/functions/{name}/aliases/{alias}
func (h *Handler) GetAlias(w http.ResponseWriter, r *http.Request, name, alias string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	a, err := h.loadAlias(r, ref.Name, alias)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// UpdateAlias  PUT /2015-03-31/functions/{name}/aliases/{alias}
func (h *Handler) UpdateAlias(w http.ResponseWriter, r *http.Request, name, alias string) {
	var in struct {
		FunctionVersion *string
		Description     *string
		RoutingConfig   *AliasRoutingConfiguration
		RevisionId      string
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	a, err := h.loadAlias(r, ref.Name, alias)
	if err != nil {
		writeError(w, err)
		return
	}
	if in.RevisionId != "" && in.RevisionId != a.RevisionId {
		writeError(w, errPrecondition("The Revision Id provided does not match the latest Revision Id. Call the GetFunction/GetAlias API to retrieve the latest Revision Id"))
		return
	}
	if in.FunctionVersion != nil {
		a.FunctionVersion = *in.FunctionVersion
	}
	if in.Description != nil {
		a.Description = *in.Description
	}
	if in.RoutingConfig != nil {
		a.RoutingConfig = normaliseRouting(in.RoutingConfig)
	}
	if err := h.checkAliasTarget(r, ref.Name, a.FunctionVersion, a.RoutingConfig); err != nil {
		writeError(w, err)
		return
	}
	a.RevisionId = newRevisionID()
	if err := h.put(r, typeAlias, ref.Name+":"+alias, a); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// DeleteAlias  DELETE /2015-03-31/functions/{name}/aliases/{alias}
func (h *Handler) DeleteAlias(w http.ResponseWriter, r *http.Request, name, alias string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	if _, err := h.loadAlias(r, ref.Name, alias); err != nil {
		writeError(w, err)
		return
	}
	id := ref.Name + ":" + alias
	for _, typ := range []string{typeAlias, typePolicy, typeURL, typeEventInvoke} {
		h.del(r, typ, id)
	}
	noContent(w)
}

// ListAliases  GET /2015-03-31/functions/{name}/aliases
func (h *Handler) ListAliases(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	version := r.URL.Query().Get("FunctionVersion")
	var aliases []AliasConfiguration
	for _, row := range h.listPrefixed(r, typeAlias, ref.Name) {
		var a AliasConfiguration
		if json.Unmarshal(row.Attributes, &a) == nil && (version == "" || a.FunctionVersion == version) {
			aliases = append(aliases, a)
		}
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].Name < aliases[j].Name })
	start, end, next := page(r, len(aliases))
	out := map[string]any{"Aliases": append([]AliasConfiguration{}, aliases[start:end]...)}
	if next != "" {
		out["NextMarker"] = next
	}
	writeJSON(w, http.StatusOK, out)
}
