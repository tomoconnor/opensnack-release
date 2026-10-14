// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"net/http"
	"strings"

	"opensnack/internal/util"
)

type userRec struct {
	Name     string            `json:"name"`
	Path     string            `json:"path"`
	ID       string            `json:"user_id"`
	Created  string            `json:"created_at"`
	Boundary string            `json:"permissions_boundary,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
}

func (u *userRec) wire(r *http.Request) User {
	return User{
		Path:                normPath(u.Path),
		UserName:            u.Name,
		UserId:              u.ID,
		Arn:                 arn(r, "user", u.Path, u.Name),
		CreateDate:          u.Created,
		PermissionsBoundary: boundary(u.Boundary),
		Tags:                tagList(u.Tags),
	}
}

func (h *Handler) getUser(w http.ResponseWriter, ns, name string) (*userRec, bool) {
	var u userRec
	if !h.load(ns, tUser, name, &u) {
		noSuchEntity(w, "The user with name "+name+" cannot be found.")
		return nil, false
	}
	return &u, true
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "UserName")
	if !ok {
		return
	}
	ns, name := util.GlobalNamespace(r), vals[0]
	if h.exists(ns, tUser, name) {
		alreadyExists(w, "User with name "+name+" already exists.")
		return
	}
	u := userRec{
		Name:     name,
		Path:     normPath(r.FormValue("Path")),
		ID:       newID("AIDA", 17),
		Created:  now(),
		Boundary: r.FormValue("PermissionsBoundary"),
		Tags:     parseTags(r),
	}
	if err := h.create(ns, tUser, name, &u); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	writeResult(w, "CreateUser", struct {
		User User `xml:"User"`
	}{u.wire(r)})
}

// GetUser without a UserName describes the caller.
func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	ns, name := util.GlobalNamespace(r), r.FormValue("UserName")
	if name == "" {
		id := util.IdentityFrom(r)
		writeResult(w, "GetUser", struct {
			User User `xml:"User"`
		}{User{Path: "/", UserName: id.Namespace, UserId: id.Namespace,
			Arn: arn(r, "user", "/", id.Namespace), CreateDate: now()}})
		return
	}
	u, ok := h.getUser(w, ns, name)
	if !ok {
		return
	}
	writeResult(w, "GetUser", struct {
		User User `xml:"User"`
	}{u.wire(r)})
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "UserName")
	if !ok {
		return
	}
	ns, name := util.GlobalNamespace(r), vals[0]
	u, ok := h.getUser(w, ns, name)
	if !ok {
		return
	}
	if p := r.FormValue("NewPath"); p != "" {
		u.Path = p
	}
	newName := r.FormValue("NewUserName")
	if newName == "" || newName == name {
		h.update(ns, tUser, name, u)
		writeResult(w, "UpdateUser", nil)
		return
	}
	if h.exists(ns, tUser, newName) {
		alreadyExists(w, "User with name "+newName+" already exists.")
		return
	}
	u.Name = newName
	if err := h.create(ns, tUser, newName, u); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	h.remove(ns, tUser, name)
	h.moveRelated(ns, tUserPolicy, name, newName, false)
	h.moveRelated(ns, tUserAttachment, name, newName, false)
	h.moveRelated(ns, tGroupMember, name, newName, true)
	for _, it := range h.rows(ns, tAccessKey) {
		var k keyRec
		if h.load(ns, tAccessKey, it.ID, &k) && k.User == name {
			k.User = newName
			h.update(ns, tAccessKey, it.ID, &k)
		}
	}
	writeResult(w, "UpdateUser", nil)
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "UserName")
	if !ok {
		return
	}
	ns, name := util.GlobalNamespace(r), vals[0]
	if _, ok := h.getUser(w, ns, name); !ok {
		return
	}
	switch {
	case len(h.userKeys(ns, name)) > 0:
		deleteConflict(w, "Cannot delete entity, must delete access keys first.")
		return
	case len(h.related(ns, tUserPolicy, name)) > 0 || len(h.related(ns, tUserAttachment, name)) > 0:
		deleteConflict(w, "Cannot delete entity, must delete policies first.")
		return
	case len(h.groupsOf(ns, name)) > 0:
		deleteConflict(w, "Cannot delete entity, must remove users from group first.")
		return
	}
	h.remove(ns, tUser, name)
	writeResult(w, "DeleteUser", nil)
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	ns, prefix := util.GlobalNamespace(r), r.FormValue("PathPrefix")
	users := []User{}
	for _, it := range h.rows(ns, tUser) {
		var u userRec
		if h.load(ns, tUser, it.ID, &u) && strings.HasPrefix(normPath(u.Path), prefix) {
			wu := u.wire(r)
			wu.Tags = nil
			users = append(users, wu)
		}
	}
	writeResult(w, "ListUsers", struct {
		Users       []User `xml:"Users>member"`
		IsTruncated bool   `xml:"IsTruncated"`
	}{users, false})
}
