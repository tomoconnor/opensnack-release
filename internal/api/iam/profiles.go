// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"net/http"
	"slices"
	"strings"

	"opensnack/internal/util"
)

type profileRec struct {
	Name    string            `json:"name"`
	Path    string            `json:"path"`
	ID      string            `json:"instance_profile_id"`
	Created string            `json:"created_at"`
	Roles   []string          `json:"roles"`
	Tags    map[string]string `json:"tags,omitempty"`
}

func (p *profileRec) wire(r *http.Request, h *Handler, ns string) InstanceProfile {
	roles := []Role{}
	for _, name := range p.Roles {
		var ro roleRec
		if h.load(ns, tRole, name, &ro) {
			wr := ro.wire(r)
			wr.Tags = nil
			roles = append(roles, wr)
		}
	}
	return InstanceProfile{
		Path:                normPath(p.Path),
		InstanceProfileName: p.Name,
		InstanceProfileId:   p.ID,
		Arn:                 arn(r, "instance-profile", p.Path, p.Name),
		CreateDate:          p.Created,
		Roles:               roles,
		Tags:                tagList(p.Tags),
	}
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) (*profileRec, string, bool) {
	vals, ok := required(w, r, "InstanceProfileName")
	if !ok {
		return nil, "", false
	}
	ns := util.GlobalNamespace(r)
	var p profileRec
	if !h.load(ns, tInstanceProfile, vals[0], &p) {
		noSuchEntity(w, "Instance Profile "+vals[0]+" cannot be found.")
		return nil, "", false
	}
	return &p, ns, true
}

func (h *Handler) profilesForRole(ns, role string) []profileRec {
	var out []profileRec
	for _, it := range h.rows(ns, tInstanceProfile) {
		var p profileRec
		if h.load(ns, tInstanceProfile, it.ID, &p) && slices.Contains(p.Roles, role) {
			out = append(out, p)
		}
	}
	return out
}

func (h *Handler) CreateInstanceProfile(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "InstanceProfileName")
	if !ok {
		return
	}
	ns, name := util.GlobalNamespace(r), vals[0]
	if h.exists(ns, tInstanceProfile, name) {
		alreadyExists(w, "Instance Profile "+name+" already exists.")
		return
	}
	p := profileRec{
		Name:    name,
		Path:    normPath(r.FormValue("Path")),
		ID:      newID("AIPA", 17),
		Created: now(),
		Roles:   []string{},
		Tags:    parseTags(r),
	}
	if err := h.create(ns, tInstanceProfile, name, &p); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	writeResult(w, "CreateInstanceProfile", struct {
		InstanceProfile InstanceProfile `xml:"InstanceProfile"`
	}{p.wire(r, h, ns)})
}

func (h *Handler) GetInstanceProfile(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getProfile(w, r)
	if !ok {
		return
	}
	writeResult(w, "GetInstanceProfile", struct {
		InstanceProfile InstanceProfile `xml:"InstanceProfile"`
	}{p.wire(r, h, ns)})
}

func (h *Handler) DeleteInstanceProfile(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getProfile(w, r)
	if !ok {
		return
	}
	if len(p.Roles) > 0 {
		deleteConflict(w, "Cannot delete entity, must remove roles from instance profile first.")
		return
	}
	h.remove(ns, tInstanceProfile, p.Name)
	writeResult(w, "DeleteInstanceProfile", nil)
}

func (h *Handler) ListInstanceProfiles(w http.ResponseWriter, r *http.Request) {
	ns, prefix := util.GlobalNamespace(r), r.FormValue("PathPrefix")
	list := []InstanceProfile{}
	for _, it := range h.rows(ns, tInstanceProfile) {
		var p profileRec
		if h.load(ns, tInstanceProfile, it.ID, &p) && strings.HasPrefix(normPath(p.Path), prefix) {
			list = append(list, p.wire(r, h, ns))
		}
	}
	writeResult(w, "ListInstanceProfiles", struct {
		InstanceProfiles []InstanceProfile `xml:"InstanceProfiles>member"`
		IsTruncated      bool              `xml:"IsTruncated"`
	}{list, false})
}

func (h *Handler) ListInstanceProfilesForRole(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "RoleName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	if _, ok := h.getRole(w, ns, vals[0]); !ok {
		return
	}
	list := []InstanceProfile{}
	for _, p := range h.profilesForRole(ns, vals[0]) {
		list = append(list, p.wire(r, h, ns))
	}
	writeResult(w, "ListInstanceProfilesForRole", struct {
		InstanceProfiles []InstanceProfile `xml:"InstanceProfiles>member"`
		IsTruncated      bool              `xml:"IsTruncated"`
	}{list, false})
}

func (h *Handler) AddRoleToInstanceProfile(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getProfile(w, r)
	if !ok {
		return
	}
	vals, ok := required(w, r, "RoleName")
	if !ok {
		return
	}
	if _, ok := h.getRole(w, ns, vals[0]); !ok {
		return
	}
	if len(p.Roles) >= 1 {
		fail(w, http.StatusConflict, "LimitExceeded", "Cannot exceed quota for InstanceSessionsPerInstanceProfile: 1")
		return
	}
	p.Roles = append(p.Roles, vals[0])
	h.update(ns, tInstanceProfile, p.Name, p)
	writeResult(w, "AddRoleToInstanceProfile", nil)
}

func (h *Handler) RemoveRoleFromInstanceProfile(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getProfile(w, r)
	if !ok {
		return
	}
	vals, ok := required(w, r, "RoleName")
	if !ok {
		return
	}
	i := slices.Index(p.Roles, vals[0])
	if i < 0 {
		noSuchEntity(w, "The role with name "+vals[0]+" cannot be found.")
		return
	}
	p.Roles = slices.Delete(p.Roles, i, i+1)
	h.update(ns, tInstanceProfile, p.Name, p)
	writeResult(w, "RemoveRoleFromInstanceProfile", nil)
}
