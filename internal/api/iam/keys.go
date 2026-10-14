// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"opensnack/internal/util"
)

type keyRec struct {
	User    string `json:"user"`
	ID      string `json:"access_key_id"`
	Secret  string `json:"secret_access_key"`
	Status  string `json:"status"`
	Created string `json:"created_at"`
}

func (k *keyRec) meta() AccessKey {
	return AccessKey{UserName: k.User, AccessKeyId: k.ID, Status: k.Status, CreateDate: k.Created}
}

// NewSecret returns a random 40-character secret access key.
func NewSecret() string {
	b := make([]byte, 30)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func (h *Handler) userKeys(ns, user string) []keyRec {
	var out []keyRec
	for _, it := range h.rows(ns, tAccessKey) {
		var k keyRec
		if h.load(ns, tAccessKey, it.ID, &k) && k.User == user {
			out = append(out, k)
		}
	}
	return out
}

func (h *Handler) CreateAccessKey(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "UserName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	if _, ok := h.getUser(w, ns, vals[0]); !ok {
		return
	}
	if len(h.userKeys(ns, vals[0])) >= 2 {
		fail(w, http.StatusConflict, "LimitExceeded", "Cannot exceed quota for AccessKeysPerUser: 2")
		return
	}
	k := keyRec{User: vals[0], ID: newID("AKIA", 16), Secret: NewSecret(), Status: "Active", Created: now()}
	if err := h.create(ns, tAccessKey, k.ID, &k); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	out := k.meta()
	out.SecretAccessKey = k.Secret
	writeResult(w, "CreateAccessKey", struct {
		AccessKey AccessKey `xml:"AccessKey"`
	}{out})
}

func (h *Handler) ListAccessKeys(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "UserName")
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	if _, ok := h.getUser(w, ns, vals[0]); !ok {
		return
	}
	list := []AccessKey{}
	for _, k := range h.userKeys(ns, vals[0]) {
		list = append(list, k.meta())
	}
	writeResult(w, "ListAccessKeys", struct {
		AccessKeyMetadata []AccessKey `xml:"AccessKeyMetadata>member"`
		IsTruncated       bool        `xml:"IsTruncated"`
	}{list, false})
}

func (h *Handler) getKey(w http.ResponseWriter, r *http.Request) (*keyRec, string, bool) {
	vals, ok := required(w, r, "AccessKeyId")
	if !ok {
		return nil, "", false
	}
	ns := util.GlobalNamespace(r)
	var k keyRec
	if !h.load(ns, tAccessKey, vals[0], &k) || (r.FormValue("UserName") != "" && k.User != r.FormValue("UserName")) {
		noSuchEntity(w, "The Access Key with id "+vals[0]+" cannot be found.")
		return nil, "", false
	}
	return &k, ns, true
}

func (h *Handler) UpdateAccessKey(w http.ResponseWriter, r *http.Request) {
	k, ns, ok := h.getKey(w, r)
	if !ok {
		return
	}
	switch s := r.FormValue("Status"); s {
	case "Active", "Inactive":
		k.Status = s
	default:
		fail(w, http.StatusBadRequest, "ValidationError",
			"1 validation error detected: Value '"+s+"' at 'status' failed to satisfy constraint: Member must satisfy enum value set: [Active, Inactive]")
		return
	}
	h.update(ns, tAccessKey, k.ID, k)
	writeResult(w, "UpdateAccessKey", nil)
}

func (h *Handler) DeleteAccessKey(w http.ResponseWriter, r *http.Request) {
	k, ns, ok := h.getKey(w, r)
	if !ok {
		return
	}
	h.remove(ns, tAccessKey, k.ID)
	writeResult(w, "DeleteAccessKey", nil)
}

func (h *Handler) GetAccessKeyLastUsed(w http.ResponseWriter, r *http.Request) {
	k, _, ok := h.getKey(w, r)
	if !ok {
		return
	}
	type lastUsed struct {
		Region      string `xml:"Region"`
		ServiceName string `xml:"ServiceName"`
	}
	writeResult(w, "GetAccessKeyLastUsed", struct {
		UserName          string   `xml:"UserName"`
		AccessKeyLastUsed lastUsed `xml:"AccessKeyLastUsed"`
	}{k.User, lastUsed{"N/A", "N/A"}})
}
