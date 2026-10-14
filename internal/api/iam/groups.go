// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"net/http"
	"strings"

	"opensnack/internal/util"
)

type groupRec struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	ID      string `json:"group_id"`
	Created string `json:"created_at"`
}

func (g *groupRec) wire(r *http.Request) Group {
	return Group{
		Path:       normPath(g.Path),
		GroupName:  g.Name,
		GroupId:    g.ID,
		Arn:        arn(r, "group", g.Path, g.Name),
		CreateDate: g.Created,
	}
}

func (h *Handler) getGroup(w http.ResponseWriter, ns, name string) (*groupRec, bool) {
	var g groupRec
	if !h.load(ns, tGroup, name, &g) {
		noSuchEntity(w, "The group with name "+name+" cannot be found.")
		return nil, false
	}
	return &g, true
}

// groupsOf lists the groups a user belongs to.
func (h *Handler) groupsOf(ns, user string) []string {
	var out []string
	for _, it := range h.rows(ns, tGroupMember) {
		if g, u, ok := strings.Cut(it.ID, "|"); ok && u == user {
			out = append(out, g)
		}
	}
	return out
}

func (h *Handler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "GroupName")
	if !ok {
		return
	}
	ns, name := util.GlobalNamespace(r), vals[0]
	if h.exists(ns, tGroup, name) {
		alreadyExists(w, "Group with name "+name+" already exists.")
		return
	}
	g := groupRec{Name: name, Path: normPath(r.FormValue("Path")), ID: newID("AGPA", 17), Created: now()}
	if err := h.create(ns, tGroup, name, &g); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	writeResult(w, "CreateGroup", struct {
		Group Group `xml:"Group"`
	}{g.wire(r)})
}

func (h *Handler) GetGroup(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "GroupName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	g, ok := h.getGroup(w, ns, vals[0])
	if !ok {
		return
	}
	users := []User{}
	for _, name := range h.related(ns, tGroupMember, g.Name) {
		var u userRec
		if h.load(ns, tUser, name, &u) {
			wu := u.wire(r)
			wu.Tags = nil
			users = append(users, wu)
		}
	}
	writeResult(w, "GetGroup", struct {
		Group       Group  `xml:"Group"`
		Users       []User `xml:"Users>member"`
		IsTruncated bool   `xml:"IsTruncated"`
	}{g.wire(r), users, false})
}

func (h *Handler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "GroupName")
	if !ok {
		return
	}
	ns, name := util.GlobalNamespace(r), vals[0]
	g, ok := h.getGroup(w, ns, name)
	if !ok {
		return
	}
	if p := r.FormValue("NewPath"); p != "" {
		g.Path = p
	}
	newName := r.FormValue("NewGroupName")
	if newName == "" || newName == name {
		h.update(ns, tGroup, name, g)
		writeResult(w, "UpdateGroup", nil)
		return
	}
	if h.exists(ns, tGroup, newName) {
		alreadyExists(w, "Group with name "+newName+" already exists.")
		return
	}
	g.Name = newName
	if err := h.create(ns, tGroup, newName, g); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	h.remove(ns, tGroup, name)
	h.moveRelated(ns, tGroupPolicy, name, newName, false)
	h.moveRelated(ns, tGroupAttachment, name, newName, false)
	h.moveRelated(ns, tGroupMember, name, newName, false)
	writeResult(w, "UpdateGroup", nil)
}

func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "GroupName")
	if !ok {
		return
	}
	ns, name := util.GlobalNamespace(r), vals[0]
	if _, ok := h.getGroup(w, ns, name); !ok {
		return
	}
	if len(h.related(ns, tGroupMember, name)) > 0 {
		deleteConflict(w, "Cannot delete entity, must remove users from group first.")
		return
	}
	if len(h.related(ns, tGroupPolicy, name)) > 0 || len(h.related(ns, tGroupAttachment, name)) > 0 {
		deleteConflict(w, "Cannot delete entity, must delete policies first.")
		return
	}
	h.remove(ns, tGroup, name)
	writeResult(w, "DeleteGroup", nil)
}

func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	ns, prefix := util.GlobalNamespace(r), r.FormValue("PathPrefix")
	groups := []Group{}
	for _, it := range h.rows(ns, tGroup) {
		var g groupRec
		if h.load(ns, tGroup, it.ID, &g) && strings.HasPrefix(normPath(g.Path), prefix) {
			groups = append(groups, g.wire(r))
		}
	}
	writeResult(w, "ListGroups", struct {
		Groups      []Group `xml:"Groups>member"`
		IsTruncated bool    `xml:"IsTruncated"`
	}{groups, false})
}

func (h *Handler) AddUserToGroup(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "GroupName", "UserName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	if _, ok := h.getGroup(w, ns, vals[0]); !ok {
		return
	}
	if _, ok := h.getUser(w, ns, vals[1]); !ok {
		return
	}
	if err := h.put(ns, tGroupMember, vals[0]+"|"+vals[1], struct{}{}); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	writeResult(w, "AddUserToGroup", nil)
}

func (h *Handler) RemoveUserFromGroup(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "GroupName", "UserName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	if _, ok := h.getGroup(w, ns, vals[0]); !ok {
		return
	}
	if _, ok := h.getUser(w, ns, vals[1]); !ok {
		return
	}
	h.remove(ns, tGroupMember, vals[0]+"|"+vals[1])
	writeResult(w, "RemoveUserFromGroup", nil)
}

func (h *Handler) ListGroupsForUser(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "UserName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	if _, ok := h.getUser(w, ns, vals[0]); !ok {
		return
	}
	groups := []Group{}
	for _, name := range h.groupsOf(ns, vals[0]) {
		var g groupRec
		if h.load(ns, tGroup, name, &g) {
			groups = append(groups, g.wire(r))
		}
	}
	writeResult(w, "ListGroupsForUser", struct {
		Groups      []Group `xml:"Groups>member"`
		IsTruncated bool    `xml:"IsTruncated"`
	}{groups, false})
}
