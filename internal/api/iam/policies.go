// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/util"
)

const maxPolicyVersions = 5

type versionRec struct {
	ID       string `json:"id"`
	Document string `json:"document"`
	Created  string `json:"created_at"`
}

type policyRec struct {
	Name           string            `json:"name"`
	Path           string            `json:"path"`
	ID             string            `json:"policy_id"`
	Description    string            `json:"description,omitempty"`
	Created        string            `json:"created_at"`
	Updated        string            `json:"updated_at"`
	DefaultVersion string            `json:"default_version"`
	NextVersion    int               `json:"next_version"`
	Versions       []versionRec      `json:"versions"`
	Tags           map[string]string `json:"tags,omitempty"`
}

func (p *policyRec) arn(r *http.Request) string {
	return arn(r, "policy", p.Path, p.Name)
}

func (p *policyRec) wire(r *http.Request, h *Handler, ns string) Policy {
	return Policy{
		PolicyName:       p.Name,
		PolicyId:         p.ID,
		Arn:              p.arn(r),
		Path:             normPath(p.Path),
		DefaultVersionId: p.DefaultVersion,
		AttachmentCount:  h.attachmentCount(ns, p.arn(r)),
		IsAttachable:     true,
		Description:      p.Description,
		CreateDate:       p.Created,
		UpdateDate:       p.Updated,
		Tags:             tagList(p.Tags),
	}
}

func (p *policyRec) version(id string) (*versionRec, int) {
	for i := range p.Versions {
		if p.Versions[i].ID == id {
			return &p.Versions[i], i
		}
	}
	return nil, -1
}

// versionTime has millisecond precision so the provider, which prunes the
// oldest version by CreateDate, can order versions made in the same second.
func versionTime() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) (*policyRec, string, bool) {
	vals, ok := required(w, r, "PolicyArn")
	if !ok {
		return nil, "", false
	}
	ns := util.GlobalNamespace(r)
	var p policyRec
	acct, name, ok := parsePolicyArn(vals[0])
	if !ok {
		fail(w, http.StatusBadRequest, "InvalidInput", "ARN "+vals[0]+" is not valid.")
		return nil, "", false
	}
	if acct != util.AccountID(r) || !h.load(ns, tPolicy, name, &p) || p.arn(r) != vals[0] {
		noSuchEntity(w, "Policy "+vals[0]+" does not exist or is not attachable.")
		return nil, "", false
	}
	return &p, ns, true
}

func (h *Handler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "PolicyName", "PolicyDocument")
	if !ok {
		return
	}
	ns, name, doc := util.GlobalNamespace(r), vals[0], vals[1]
	if !validDoc(w, doc) {
		return
	}
	if h.exists(ns, tPolicy, name) {
		alreadyExists(w, "A policy called "+name+" already exists. Duplicate names are not allowed.")
		return
	}
	created := now()
	p := policyRec{
		Name:           name,
		Path:           normPath(r.FormValue("Path")),
		ID:             newID("ANPA", 17),
		Description:    r.FormValue("Description"),
		Created:        created,
		Updated:        created,
		DefaultVersion: "v1",
		NextVersion:    2,
		Versions:       []versionRec{{ID: "v1", Document: doc, Created: versionTime()}},
		Tags:           parseTags(r),
	}
	if err := h.create(ns, tPolicy, name, &p); err != nil {
		fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
		return
	}
	writeResult(w, "CreatePolicy", struct {
		Policy Policy `xml:"Policy"`
	}{p.wire(r, h, ns)})
}

func (h *Handler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getPolicy(w, r)
	if !ok {
		return
	}
	writeResult(w, "GetPolicy", struct {
		Policy Policy `xml:"Policy"`
	}{p.wire(r, h, ns)})
}

func (h *Handler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getPolicy(w, r)
	if !ok {
		return
	}
	if h.attachmentCount(ns, p.arn(r)) > 0 {
		deleteConflict(w, "Cannot delete a policy attached to entities.")
		return
	}
	if len(p.Versions) > 1 {
		deleteConflict(w, "This policy has more than one version. Before you delete a policy, you must delete the policy's versions. The default version is deleted with the policy.")
		return
	}
	h.remove(ns, tPolicy, p.Name)
	writeResult(w, "DeletePolicy", nil)
}

// ListPolicies lists the account's own policies; opensnack has no catalogue
// of AWS managed policies, so Scope=AWS is empty.
func (h *Handler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	scope, prefix := r.FormValue("Scope"), r.FormValue("PathPrefix")
	onlyAttached := r.FormValue("OnlyAttached") == "true"
	policies := []Policy{}
	if scope != "AWS" {
		for _, it := range h.rows(ns, tPolicy) {
			var p policyRec
			if !h.load(ns, tPolicy, it.ID, &p) || !strings.HasPrefix(normPath(p.Path), prefix) {
				continue
			}
			wp := p.wire(r, h, ns)
			if onlyAttached && wp.AttachmentCount == 0 {
				continue
			}
			wp.Tags, wp.Description = nil, ""
			policies = append(policies, wp)
		}
	}
	writeResult(w, "ListPolicies", struct {
		Policies    []Policy `xml:"Policies>member"`
		IsTruncated bool     `xml:"IsTruncated"`
	}{policies, false})
}

func (h *Handler) CreatePolicyVersion(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getPolicy(w, r)
	if !ok {
		return
	}
	vals, ok := required(w, r, "PolicyDocument")
	if !ok || !validDoc(w, vals[0]) {
		return
	}
	if len(p.Versions) >= maxPolicyVersions {
		fail(w, http.StatusConflict, "LimitExceeded",
			"A managed policy can have up to 5 versions. Before you create a new version, you must delete an existing version.")
		return
	}
	if p.NextVersion == 0 {
		p.NextVersion = len(p.Versions) + 1
	}
	v := versionRec{ID: "v" + strconv.Itoa(p.NextVersion), Document: vals[0], Created: versionTime()}
	p.NextVersion++
	p.Versions = append(p.Versions, v)
	isDefault := r.FormValue("SetAsDefault") == "true"
	if isDefault {
		p.DefaultVersion = v.ID
		p.Updated = now()
	}
	h.update(ns, tPolicy, p.Name, p)
	writeResult(w, "CreatePolicyVersion", struct {
		PolicyVersion PolicyVersion `xml:"PolicyVersion"`
	}{PolicyVersion{VersionId: v.ID, IsDefaultVersion: isDefault, CreateDate: v.Created}})
}

func (h *Handler) GetPolicyVersion(w http.ResponseWriter, r *http.Request) {
	p, _, ok := h.getPolicy(w, r)
	if !ok {
		return
	}
	vals, ok := required(w, r, "VersionId")
	if !ok {
		return
	}
	v, _ := p.version(vals[0])
	if v == nil {
		noSuchEntity(w, "Policy "+p.arn(r)+" version "+vals[0]+" does not exist or is not attachable.")
		return
	}
	writeResult(w, "GetPolicyVersion", struct {
		PolicyVersion PolicyVersion `xml:"PolicyVersion"`
	}{PolicyVersion{Document: encodeDoc(v.Document), VersionId: v.ID,
		IsDefaultVersion: v.ID == p.DefaultVersion, CreateDate: v.Created}})
}

func (h *Handler) ListPolicyVersions(w http.ResponseWriter, r *http.Request) {
	p, _, ok := h.getPolicy(w, r)
	if !ok {
		return
	}
	versions := []PolicyVersion{}
	for _, v := range p.Versions {
		versions = append(versions, PolicyVersion{VersionId: v.ID, IsDefaultVersion: v.ID == p.DefaultVersion, CreateDate: v.Created})
	}
	writeResult(w, "ListPolicyVersions", struct {
		Versions    []PolicyVersion `xml:"Versions>member"`
		IsTruncated bool            `xml:"IsTruncated"`
	}{versions, false})
}

func (h *Handler) DeletePolicyVersion(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getPolicy(w, r)
	if !ok {
		return
	}
	vals, ok := required(w, r, "VersionId")
	if !ok {
		return
	}
	_, i := p.version(vals[0])
	if i < 0 {
		noSuchEntity(w, "Policy "+p.arn(r)+" version "+vals[0]+" does not exist or is not attachable.")
		return
	}
	if p.Versions[i].ID == p.DefaultVersion {
		deleteConflict(w, "Cannot delete the default version of a policy.")
		return
	}
	p.Versions = append(p.Versions[:i], p.Versions[i+1:]...)
	h.update(ns, tPolicy, p.Name, p)
	writeResult(w, "DeletePolicyVersion", nil)
}

func (h *Handler) SetDefaultPolicyVersion(w http.ResponseWriter, r *http.Request) {
	p, ns, ok := h.getPolicy(w, r)
	if !ok {
		return
	}
	vals, ok := required(w, r, "VersionId")
	if !ok {
		return
	}
	if v, _ := p.version(vals[0]); v == nil {
		noSuchEntity(w, "Policy "+p.arn(r)+" version "+vals[0]+" does not exist or is not attachable.")
		return
	}
	if p.DefaultVersion != vals[0] {
		p.DefaultVersion = vals[0]
		p.Updated = now()
		h.update(ns, tPolicy, p.Name, p)
	}
	writeResult(w, "SetDefaultPolicyVersion", nil)
}

func (h *Handler) ListEntitiesForPolicy(w http.ResponseWriter, r *http.Request) {
	vals, ok := required(w, r, "PolicyArn")
	if !ok {
		return
	}
	ns, pa := util.GlobalNamespace(r), vals[0]
	out := struct {
		PolicyGroups []PolicyGroup `xml:"PolicyGroups>member"`
		PolicyUsers  []PolicyUser  `xml:"PolicyUsers>member"`
		PolicyRoles  []PolicyRole  `xml:"PolicyRoles>member"`
		IsTruncated  bool          `xml:"IsTruncated"`
	}{}
	for _, owner := range h.attachedTo(ns, tGroupAttachment, pa) {
		var g groupRec
		if h.load(ns, tGroup, owner, &g) {
			out.PolicyGroups = append(out.PolicyGroups, PolicyGroup{g.Name, g.ID})
		}
	}
	for _, owner := range h.attachedTo(ns, tUserAttachment, pa) {
		var u userRec
		if h.load(ns, tUser, owner, &u) {
			out.PolicyUsers = append(out.PolicyUsers, PolicyUser{u.Name, u.ID})
		}
	}
	for _, owner := range h.attachedTo(ns, tRoleAttachment, pa) {
		var ro roleRec
		if h.load(ns, tRole, owner, &ro) {
			out.PolicyRoles = append(out.PolicyRoles, PolicyRole{ro.Name, ro.ID})
		}
	}
	writeResult(w, "ListEntitiesForPolicy", out)
}
