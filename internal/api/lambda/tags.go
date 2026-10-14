// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"net/http"
	"regexp"
	"strings"

	"opensnack/internal/util"
)

func checkTags(tags map[string]string) error {
	if len(tags) > 50 {
		return errInvalid("Number of tags exceeds resource tag limit.")
	}
	for k, v := range tags {
		if k == "" || len(k) > 128 || len(v) > 256 {
			return errInvalid("The tag key or value is not valid: " + k)
		}
		if strings.HasPrefix(strings.ToLower(k), "aws:") {
			return errInvalid("Tag keys starting with 'aws:' are reserved for internal use: " + k)
		}
	}
	return nil
}

var taggableARN = regexp.MustCompile(`^arn:aws[a-zA-Z-]*:lambda:([a-z0-9-]+):(\d{12}):(function|event-source-mapping):([a-zA-Z0-9_-]+)(:.*)?$`)

// taggable loads the tags of the function or event source mapping an ARN
// names, and returns a save function for the new set. The caller holds h.mu.
func (h *Handler) taggable(r *http.Request, arn string) (map[string]string, func(map[string]string) error, error) {
	m := taggableARN.FindStringSubmatch(arn)
	if m == nil {
		return nil, nil, errInvalid("Invalid ARN: " + arn)
	}
	if m[1] != util.Region(r) || m[2] != util.AccountID(r) {
		return nil, nil, errNotFound("The resource you requested does not exist: " + arn)
	}
	if m[5] != "" {
		return nil, nil, errInvalid("Tags are not supported on a qualified ARN: " + arn)
	}
	if m[3] == "function" {
		fn, err := h.loadFunction(r, m[4])
		if err != nil {
			return nil, nil, err
		}
		return fn.Tags, func(tags map[string]string) error {
			fn.Tags = tags
			return h.put(r, typeFunction, m[4], fn)
		}, nil
	}
	var esm esmRecord
	if !h.get(r, typeESM, m[4], &esm) {
		return nil, nil, errNotFound("The resource you requested does not exist: " + arn)
	}
	return esm.Tags, func(tags map[string]string) error {
		esm.Tags = tags
		return h.put(r, typeESM, m[4], esm)
	}, nil
}

// ListTags  GET /2017-03-31/tags/{arn}
func (h *Handler) ListTags(w http.ResponseWriter, r *http.Request, arn string) {
	tags, _, err := h.taggable(r, arn)
	if err != nil {
		writeError(w, err)
		return
	}
	if tags == nil {
		tags = map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}

// TagResource  POST /2017-03-31/tags/{arn}
func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request, arn string) {
	var in struct{ Tags map[string]string }
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	tags, save, err := h.taggable(r, arn)
	if err != nil {
		writeError(w, err)
		return
	}
	merged := map[string]string{}
	for k, v := range tags {
		merged[k] = v
	}
	for k, v := range in.Tags {
		merged[k] = v
	}
	if err := checkTags(merged); err != nil {
		writeError(w, err)
		return
	}
	if err := save(merged); err != nil {
		writeError(w, err)
		return
	}
	noContent(w)
}

// UntagResource  DELETE /2017-03-31/tags/{arn}?tagKeys=a&tagKeys=b
func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request, arn string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tags, save, err := h.taggable(r, arn)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, k := range r.URL.Query()["tagKeys"] {
		delete(tags, k)
	}
	if len(tags) == 0 {
		tags = nil
	}
	if err := save(tags); err != nil {
		writeError(w, err)
		return
	}
	noContent(w)
}
