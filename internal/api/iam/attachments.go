// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"net/http"
	"strings"

	"opensnack/internal/util"
)

// Inline policies and managed-policy attachments behave the same for users,
// roles and groups; only the parameter and row type names differ.

type ownerKind struct {
	typ, param, label string
	inline, attached  string
}

var owners = map[string]ownerKind{
	tUser:  {tUser, "UserName", "user", tUserPolicy, tUserAttachment},
	tRole:  {tRole, "RoleName", "role", tRolePolicy, tRoleAttachment},
	tGroup: {tGroup, "GroupName", "group", tGroupPolicy, tGroupAttachment},
}

type inlineRec struct {
	Document string `json:"document"`
}

type attachRec struct {
	PolicyArn string `json:"policy_arn"`
}

// owner reads and checks the user, role or group a request names.
func (h *Handler) owner(w http.ResponseWriter, r *http.Request, k ownerKind) (string, string, bool) {
	vals, ok := required(w, r, k.param)
	if !ok {
		return "", "", false
	}
	ns := util.GlobalNamespace(r)
	if !h.exists(ns, k.typ, vals[0]) {
		noSuchEntity(w, "The "+k.label+" with name "+vals[0]+" cannot be found.")
		return "", "", false
	}
	return ns, vals[0], true
}

func (h *Handler) putInline(typ string) action {
	k := owners[typ]
	return func(w http.ResponseWriter, r *http.Request) {
		ns, owner, ok := h.owner(w, r, k)
		if !ok {
			return
		}
		vals, ok := required(w, r, "PolicyName", "PolicyDocument")
		if !ok || !validDoc(w, vals[1]) {
			return
		}
		if err := h.put(ns, k.inline, owner+"|"+vals[0], &inlineRec{Document: vals[1]}); err != nil {
			fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
			return
		}
		writeResult(w, r.FormValue("Action"), nil)
	}
}

func (h *Handler) getInline(typ string) action {
	k := owners[typ]
	return func(w http.ResponseWriter, r *http.Request) {
		ns, owner, ok := h.owner(w, r, k)
		if !ok {
			return
		}
		vals, ok := required(w, r, "PolicyName")
		if !ok {
			return
		}
		var p inlineRec
		if !h.load(ns, k.inline, owner+"|"+vals[0], &p) {
			noSuchEntity(w, "The "+k.label+" policy with name "+vals[0]+" cannot be found.")
			return
		}
		out := struct {
			UserName       string `xml:"UserName,omitempty"`
			RoleName       string `xml:"RoleName,omitempty"`
			GroupName      string `xml:"GroupName,omitempty"`
			PolicyName     string `xml:"PolicyName"`
			PolicyDocument string `xml:"PolicyDocument"`
		}{PolicyName: vals[0], PolicyDocument: encodeDoc(p.Document)}
		switch typ {
		case tUser:
			out.UserName = owner
		case tRole:
			out.RoleName = owner
		case tGroup:
			out.GroupName = owner
		}
		writeResult(w, r.FormValue("Action"), out)
	}
}

func (h *Handler) deleteInline(typ string) action {
	k := owners[typ]
	return func(w http.ResponseWriter, r *http.Request) {
		ns, owner, ok := h.owner(w, r, k)
		if !ok {
			return
		}
		vals, ok := required(w, r, "PolicyName")
		if !ok {
			return
		}
		id := owner + "|" + vals[0]
		if !h.exists(ns, k.inline, id) {
			noSuchEntity(w, "The "+k.label+" policy with name "+vals[0]+" cannot be found.")
			return
		}
		h.remove(ns, k.inline, id)
		writeResult(w, r.FormValue("Action"), nil)
	}
}

func (h *Handler) listInline(typ string) action {
	k := owners[typ]
	return func(w http.ResponseWriter, r *http.Request) {
		ns, owner, ok := h.owner(w, r, k)
		if !ok {
			return
		}
		names := h.related(ns, k.inline, owner)
		if names == nil {
			names = []string{}
		}
		writeResult(w, r.FormValue("Action"), struct {
			PolicyNames []string `xml:"PolicyNames>member"`
			IsTruncated bool     `xml:"IsTruncated"`
		}{names, false})
	}
}

// attachedTo lists the owners of typ that have policyArn attached.
func (h *Handler) attachedTo(ns, typ, policyArn string) []string {
	var out []string
	for _, it := range h.rows(ns, typ) {
		if o, a, ok := strings.Cut(it.ID, "|"); ok && a == policyArn {
			out = append(out, o)
		}
	}
	return out
}

func (h *Handler) attachmentCount(ns, policyArn string) int {
	return len(h.attachedTo(ns, tUserAttachment, policyArn)) +
		len(h.attachedTo(ns, tRoleAttachment, policyArn)) +
		len(h.attachedTo(ns, tGroupAttachment, policyArn))
}

func (h *Handler) attach(typ string) action {
	k := owners[typ]
	return func(w http.ResponseWriter, r *http.Request) {
		ns, owner, ok := h.owner(w, r, k)
		if !ok {
			return
		}
		vals, ok := required(w, r, "PolicyArn")
		if !ok {
			return
		}
		pa := vals[0]
		acct, name, ok := parsePolicyArn(pa)
		if !ok {
			fail(w, http.StatusBadRequest, "InvalidInput", "ARN "+pa+" is not valid.")
			return
		}
		// AWS managed policies (account "aws") aren't modelled; accept them.
		if acct != "aws" {
			var p policyRec
			if acct != util.AccountID(r) || !h.load(ns, tPolicy, name, &p) || p.arn(r) != pa {
				noSuchEntity(w, "Policy "+pa+" does not exist or is not attachable.")
				return
			}
		}
		if err := h.put(ns, k.attached, owner+"|"+pa, &attachRec{PolicyArn: pa}); err != nil {
			fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
			return
		}
		writeResult(w, r.FormValue("Action"), nil)
	}
}

func (h *Handler) detach(typ string) action {
	k := owners[typ]
	return func(w http.ResponseWriter, r *http.Request) {
		ns, owner, ok := h.owner(w, r, k)
		if !ok {
			return
		}
		vals, ok := required(w, r, "PolicyArn")
		if !ok {
			return
		}
		id := owner + "|" + vals[0]
		if !h.exists(ns, k.attached, id) {
			noSuchEntity(w, "Policy "+vals[0]+" was not found.")
			return
		}
		h.remove(ns, k.attached, id)
		writeResult(w, r.FormValue("Action"), nil)
	}
}

func (h *Handler) listAttached(typ string) action {
	k := owners[typ]
	return func(w http.ResponseWriter, r *http.Request) {
		ns, owner, ok := h.owner(w, r, k)
		if !ok {
			return
		}
		prefix := r.FormValue("PathPrefix")
		list := []AttachedPolicy{}
		for _, pa := range h.related(ns, k.attached, owner) {
			_, name, _ := parsePolicyArn(pa)
			path := pa[strings.Index(pa, ":policy/")+len(":policy"):]
			if strings.HasPrefix(path, prefix) {
				list = append(list, AttachedPolicy{PolicyName: name, PolicyArn: pa})
			}
		}
		writeResult(w, r.FormValue("Action"), struct {
			AttachedPolicies []AttachedPolicy `xml:"AttachedPolicies>member"`
			IsTruncated      bool             `xml:"IsTruncated"`
		}{list, false})
	}
}
