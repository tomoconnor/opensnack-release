// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"net/http"
	"strconv"
	"strings"

	"opensnack/internal/resource"
	"opensnack/internal/util"
)

type roleRec struct {
	Name               string            `json:"name"`
	Path               string            `json:"path"`
	ID                 string            `json:"role_id"`
	Created            string            `json:"created_at"`
	AssumeRolePolicy   string            `json:"assume_role_policy"`
	Description        string            `json:"description,omitempty"`
	MaxSessionDuration int               `json:"max_session_duration,omitempty"`
	Boundary           string            `json:"permissions_boundary,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
}

func (ro *roleRec) wire(r *http.Request) Role {
	max := ro.MaxSessionDuration
	if max == 0 {
		max = defaultMaxSession
	}
	return Role{
		Path:                     normPath(ro.Path),
		RoleName:                 ro.Name,
		RoleId:                   ro.ID,
		Arn:                      arn(r, "role", ro.Path, ro.Name),
		CreateDate:               ro.Created,
		AssumeRolePolicyDocument: encodeDoc(ro.AssumeRolePolicy),
		Description:              ro.Description,
		MaxSessionDuration:       max,
		PermissionsBoundary:      boundary(ro.Boundary),
		Tags:                     tagList(ro.Tags),
	}
}

// RoleInfo is what STS needs to know about a role it is asked to assume.
type RoleInfo struct {
	Name, ID, Arn      string
	MaxSessionDuration int
}

// LookupRole finds a role by name in a namespace, for STS.
func LookupRole(store resource.Store, ns, account, name string) (*RoleInfo, bool) {
	var ro roleRec
	if !(&Handler{Store: store}).load(ns, tRole, name, &ro) {
		return nil, false
	}
	max := ro.MaxSessionDuration
	if max == 0 {
		max = defaultMaxSession
	}
	return &RoleInfo{
		Name: ro.Name, ID: ro.ID, MaxSessionDuration: max,
		Arn: "arn:aws:iam::" + account + ":role" + normPath(ro.Path) + ro.Name,
	}, true
}

func (h *Handler) getRole(w http.ResponseWriter, ns, name string) (*roleRec, bool) {
	var ro roleRec
	if !h.load(ns, tRole, name, &ro) {
		noSuchEntity(w, "The role with name "+name+" cannot be found.")
		return nil, false
	}
	return &ro, true
}

func maxSession(w http.ResponseWriter, v string) (int, bool) {
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 3600 || n > 43200 {
		fail(w, http.StatusBadRequest, "ValidationError",
			"1 validation error detected: Value '"+v+"' at 'maxSessionDuration' failed to satisfy constraint: Member must have value between 3600 and 43200")
		return 0, false
	}
	return n, true
}

func (h *Handler) CreateRole(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "RoleName", "AssumeRolePolicyDocument")
	if !ok {
		return
	}
	ns, name, doc := util.GlobalNamespace(r), vals[0], vals[1]
	if !validDoc(w, doc) {
		return
	}
	max, ok := maxSession(w, r.FormValue("MaxSessionDuration"))
	if !ok {
		return
	}
	if h.exists(ns, tRole, name) {
		alreadyExists(w, "Role with name "+name+" already exists.")
		return
	}
	ro := roleRec{
		Name:               name,
		Path:               normPath(r.FormValue("Path")),
		ID:                 newID("AROA", 17),
		Created:            now(),
		AssumeRolePolicy:   doc,
		Description:        r.FormValue("Description"),
		MaxSessionDuration: max,
		Boundary:           r.FormValue("PermissionsBoundary"),
		Tags:               parseTags(r),
	}
	if err := h.create(ns, tRole, name, &ro); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	writeResult(w, "CreateRole", struct {
		Role Role `xml:"Role"`
	}{ro.wire(r)})
}

func (h *Handler) GetRole(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "RoleName")
	if !ok {
		return
	}
	ro, ok := h.getRole(w, util.GlobalNamespace(r), vals[0])
	if !ok {
		return
	}
	writeResult(w, "GetRole", struct {
		Role Role `xml:"Role"`
	}{ro.wire(r)})
}

func (h *Handler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "RoleName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	ro, ok := h.getRole(w, ns, vals[0])
	if !ok {
		return
	}
	max, ok := maxSession(w, r.FormValue("MaxSessionDuration"))
	if !ok {
		return
	}
	if max != 0 {
		ro.MaxSessionDuration = max
	}
	if has(r, "Description") {
		ro.Description = r.FormValue("Description")
	}
	h.update(ns, tRole, ro.Name, ro)
	writeResult(w, "UpdateRole", struct{}{})
}

func (h *Handler) UpdateRoleDescription(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "RoleName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	ro, ok := h.getRole(w, ns, vals[0])
	if !ok {
		return
	}
	ro.Description = r.FormValue("Description")
	h.update(ns, tRole, ro.Name, ro)
	writeResult(w, "UpdateRoleDescription", struct {
		Role Role `xml:"Role"`
	}{ro.wire(r)})
}

func (h *Handler) UpdateAssumeRolePolicy(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "RoleName", "PolicyDocument")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	ro, ok := h.getRole(w, ns, vals[0])
	if !ok || !validDoc(w, vals[1]) {
		return
	}
	ro.AssumeRolePolicy = vals[1]
	h.update(ns, tRole, ro.Name, ro)
	writeResult(w, "UpdateAssumeRolePolicy", nil)
}

func (h *Handler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "RoleName")
	if !ok {
		return
	}
	ns, name := util.GlobalNamespace(r), vals[0]
	if _, ok := h.getRole(w, ns, name); !ok {
		return
	}
	if len(h.related(ns, tRoleAttachment, name)) > 0 || len(h.related(ns, tRolePolicy, name)) > 0 {
		deleteConflict(w, "Cannot delete entity, must detach all policies first.")
		return
	}
	if len(h.profilesForRole(ns, name)) > 0 {
		deleteConflict(w, "Cannot delete entity, must remove roles from instance profile first.")
		return
	}
	h.remove(ns, tRole, name)
	writeResult(w, "DeleteRole", nil)
}

func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	ns, prefix := util.GlobalNamespace(r), r.FormValue("PathPrefix")
	roles := []Role{}
	for _, it := range h.rows(ns, tRole) {
		var ro roleRec
		if h.load(ns, tRole, it.ID, &ro) && strings.HasPrefix(normPath(ro.Path), prefix) {
			wr := ro.wire(r)
			wr.Tags = nil
			roles = append(roles, wr)
		}
	}
	writeResult(w, "ListRoles", struct {
		Roles       []Role `xml:"Roles>member"`
		IsTruncated bool   `xml:"IsTruncated"`
	}{roles, false})
}
