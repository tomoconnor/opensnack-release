// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ssm

import (
	"net/http"
	"sort"

	"opensnack/internal/util"
)

const maxTags = 50

// taggedParameter checks the resource type and loads the parameter. Only
// parameters can be tagged here.
func (h *Handler) taggedParameter(r *http.Request, resourceType, resourceID string) (func() *ssmError, *paramRecord, *ssmError) {
	if resourceType != "Parameter" {
		return nil, nil, &ssmError{"InvalidResourceType", "The resource type " + resourceType + " is not supported."}
	}
	res, p, e := h.load(util.NamespaceFromHeader(r), nameFromID(resourceID))
	if e != nil {
		return nil, nil, &ssmError{"InvalidResourceId", "The resource ID \"" + resourceID + "\" is not valid. Verify the ID and try again."}
	}
	return func() *ssmError { return h.save(res, p, false) }, p, nil
}

// AddTagsToResource adds or overwrites tags on a parameter
func (h *Handler) AddTagsToResource(w http.ResponseWriter, r *http.Request) {
	var req AddTagsToResourceInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	save, p, e := h.taggedParameter(r, req.ResourceType, req.ResourceId)
	if e != nil {
		writeError(w, e)
		return
	}
	for _, t := range req.Tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			writeError(w, errValidation("Invalid tag: "+t.Key))
			return
		}
		found := false
		for i := range p.Tags {
			if p.Tags[i].Key == t.Key {
				p.Tags[i].Value = t.Value
				found = true
			}
		}
		if !found {
			p.Tags = append(p.Tags, t)
		}
	}
	if len(p.Tags) > maxTags {
		writeError(w, &ssmError{"TooManyTagsError", "The Targets parameter includes too many tags. Remove one or more tags and try the command again."})
		return
	}
	if e := save(); e != nil {
		writeError(w, e)
		return
	}
	writeSSMJSON(w, http.StatusOK, struct{}{})
}

// RemoveTagsFromResource removes tags from a parameter by key
func (h *Handler) RemoveTagsFromResource(w http.ResponseWriter, r *http.Request) {
	var req RemoveTagsFromResourceInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	save, p, e := h.taggedParameter(r, req.ResourceType, req.ResourceId)
	if e != nil {
		writeError(w, e)
		return
	}
	kept := p.Tags[:0]
	for _, t := range p.Tags {
		if !contains(req.TagKeys, t.Key) {
			kept = append(kept, t)
		}
	}
	p.Tags = kept
	if e := save(); e != nil {
		writeError(w, e)
		return
	}
	writeSSMJSON(w, http.StatusOK, struct{}{})
}

// ListTagsForResource lists a parameter's tags, sorted by key
func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req ListTagsForResourceInput
	if !decode(w, r, &req) {
		return
	}
	_, p, e := h.taggedParameter(r, req.ResourceType, req.ResourceId)
	if e != nil {
		writeError(w, e)
		return
	}
	tags := append([]Tag{}, p.Tags...)
	sort.Slice(tags, func(i, j int) bool { return tags[i].Key < tags[j].Key })
	writeSSMJSON(w, http.StatusOK, ListTagsForResourceOutput{TagList: tags})
}
