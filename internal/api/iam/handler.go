// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	APIVersion = "2010-05-08"
	xmlns      = "https://iam.amazonaws.com/doc/2010-05-08/"

	defaultMaxSession = 3600
)

// Store types. Relations are their own rows, keyed "<owner>|<other>": IAM
// names can't contain "|".
const (
	tUser            = "user"
	tRole            = "role"
	tGroup           = "group"
	tPolicy          = "policy"
	tInstanceProfile = "instance_profile"
	tAccessKey       = "access_key"
	tGroupMember     = "group_member" // group|user
	tUserPolicy      = "user_policy"  // user|policyName (inline)
	tRolePolicy      = "role_policy"
	tGroupPolicy     = "group_policy"
	tUserAttachment  = "user_attachment" // user|policyArn (managed)
	tRoleAttachment  = "role_attachment"
	tGroupAttachment = "group_attachment"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

type action func(w http.ResponseWriter, r *http.Request)

func (h *Handler) actions() map[string]action {
	return map[string]action{
		"CreateUser": h.CreateUser, "GetUser": h.GetUser, "UpdateUser": h.UpdateUser,
		"DeleteUser": h.DeleteUser, "ListUsers": h.ListUsers,
		"TagUser": h.tagOp(tUser, "UserName"), "UntagUser": h.untagOp(tUser, "UserName"),
		"ListUserTags":                  h.listTagsOp(tUser, "UserName"),
		"PutUserPermissionsBoundary":    h.putBoundary(tUser, "UserName"),
		"DeleteUserPermissionsBoundary": h.deleteBoundary(tUser, "UserName"),

		"CreateRole": h.CreateRole, "GetRole": h.GetRole, "DeleteRole": h.DeleteRole,
		"ListRoles": h.ListRoles, "UpdateRole": h.UpdateRole,
		"UpdateRoleDescription": h.UpdateRoleDescription, "UpdateAssumeRolePolicy": h.UpdateAssumeRolePolicy,
		"TagRole": h.tagOp(tRole, "RoleName"), "UntagRole": h.untagOp(tRole, "RoleName"),
		"ListRoleTags":                  h.listTagsOp(tRole, "RoleName"),
		"PutRolePermissionsBoundary":    h.putBoundary(tRole, "RoleName"),
		"DeleteRolePermissionsBoundary": h.deleteBoundary(tRole, "RoleName"),

		"CreateGroup": h.CreateGroup, "GetGroup": h.GetGroup, "UpdateGroup": h.UpdateGroup,
		"DeleteGroup": h.DeleteGroup, "ListGroups": h.ListGroups,
		"AddUserToGroup": h.AddUserToGroup, "RemoveUserFromGroup": h.RemoveUserFromGroup,
		"ListGroupsForUser": h.ListGroupsForUser,

		"CreatePolicy": h.CreatePolicy, "GetPolicy": h.GetPolicy, "DeletePolicy": h.DeletePolicy,
		"ListPolicies": h.ListPolicies, "ListEntitiesForPolicy": h.ListEntitiesForPolicy,
		"CreatePolicyVersion": h.CreatePolicyVersion, "GetPolicyVersion": h.GetPolicyVersion,
		"DeletePolicyVersion": h.DeletePolicyVersion, "ListPolicyVersions": h.ListPolicyVersions,
		"SetDefaultPolicyVersion": h.SetDefaultPolicyVersion,
		"TagPolicy":               h.tagOp(tPolicy, "PolicyArn"), "UntagPolicy": h.untagOp(tPolicy, "PolicyArn"),
		"ListPolicyTags": h.listTagsOp(tPolicy, "PolicyArn"),

		"PutUserPolicy": h.putInline(tUser), "GetUserPolicy": h.getInline(tUser),
		"DeleteUserPolicy": h.deleteInline(tUser), "ListUserPolicies": h.listInline(tUser),
		"PutRolePolicy": h.putInline(tRole), "GetRolePolicy": h.getInline(tRole),
		"DeleteRolePolicy": h.deleteInline(tRole), "ListRolePolicies": h.listInline(tRole),
		"PutGroupPolicy": h.putInline(tGroup), "GetGroupPolicy": h.getInline(tGroup),
		"DeleteGroupPolicy": h.deleteInline(tGroup), "ListGroupPolicies": h.listInline(tGroup),

		"AttachUserPolicy": h.attach(tUser), "DetachUserPolicy": h.detach(tUser),
		"ListAttachedUserPolicies": h.listAttached(tUser),
		"AttachRolePolicy":         h.attach(tRole), "DetachRolePolicy": h.detach(tRole),
		"ListAttachedRolePolicies": h.listAttached(tRole),
		"AttachGroupPolicy":        h.attach(tGroup), "DetachGroupPolicy": h.detach(tGroup),
		"ListAttachedGroupPolicies": h.listAttached(tGroup),

		"CreateInstanceProfile": h.CreateInstanceProfile, "GetInstanceProfile": h.GetInstanceProfile,
		"DeleteInstanceProfile": h.DeleteInstanceProfile, "ListInstanceProfiles": h.ListInstanceProfiles,
		"AddRoleToInstanceProfile":      h.AddRoleToInstanceProfile,
		"RemoveRoleFromInstanceProfile": h.RemoveRoleFromInstanceProfile,
		"ListInstanceProfilesForRole":   h.ListInstanceProfilesForRole,
		"TagInstanceProfile":            h.tagOp(tInstanceProfile, "InstanceProfileName"),
		"UntagInstanceProfile":          h.untagOp(tInstanceProfile, "InstanceProfileName"),
		"ListInstanceProfileTags":       h.listTagsOp(tInstanceProfile, "InstanceProfileName"),

		"CreateAccessKey": h.CreateAccessKey, "ListAccessKeys": h.ListAccessKeys,
		"UpdateAccessKey": h.UpdateAccessKey, "DeleteAccessKey": h.DeleteAccessKey,
		"GetAccessKeyLastUsed": h.GetAccessKeyLastUsed,

		// Credentials opensnack doesn't model: the provider lists them when
		// destroying a user with force_destroy.
		"GetLoginProfile":                h.noLoginProfile,
		"ListMFADevices":                 h.emptyList("MFADevices"),
		"ListSSHPublicKeys":              h.emptyList("SSHPublicKeys"),
		"ListSigningCertificates":        h.emptyList("Certificates"),
		"ListServiceSpecificCredentials": h.emptyList("ServiceSpecificCredentials"),
		"ListVirtualMFADevices":          h.emptyList("VirtualMFADevices"),
	}
}

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		fail(w, http.StatusBadRequest, "MalformedQueryString", "Failed to parse form")
		return
	}
	act := r.FormValue("Action")
	fn, ok := h.actions()[act]
	if !ok {
		fail(w, http.StatusBadRequest, "InvalidAction", "Could not find operation "+act+" for version "+APIVersion)
		return
	}
	fn(w, r)
}

//
// Responses
//

func writeResult(w http.ResponseWriter, act string, result any) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, "<%sResponse xmlns=%q>", act, xmlns)
	if result != nil {
		enc := xml.NewEncoder(&buf)
		if err := enc.EncodeElement(result, xml.StartElement{Name: xml.Name{Local: act + "Result"}}); err != nil {
			fail(w, http.StatusInternalServerError, "ServiceFailure", err.Error())
			return
		}
	}
	fmt.Fprintf(&buf, "<ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>",
		awsresponses.NextRequestID(), act)
	awsresponses.WriteAWSHeaders(w)
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	awsresponses.WriteErrorXML(w, status, code, msg, "")
}

func noSuchEntity(w http.ResponseWriter, msg string) {
	fail(w, http.StatusNotFound, "NoSuchEntity", msg)
}

func deleteConflict(w http.ResponseWriter, msg string) {
	fail(w, http.StatusConflict, "DeleteConflict", msg)
}

func alreadyExists(w http.ResponseWriter, msg string) {
	fail(w, http.StatusConflict, "EntityAlreadyExists", msg)
}

// required reads a parameter, writing a ValidationError when it is absent.
func required(w http.ResponseWriter, r *http.Request, names ...string) ([]string, bool) {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = r.FormValue(n)
		if out[i] == "" {
			fail(w, http.StatusBadRequest, "ValidationError",
				"1 validation error detected: Value null at '"+lowerFirst(n)+"' failed to satisfy constraint: Member must not be null")
			return nil, false
		}
	}
	return out, true
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func has(r *http.Request, name string) bool {
	_, ok := r.Form[name]
	return ok
}

//
// Identifiers
//

func now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

const idAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// newID returns an IAM unique ID such as AROA… (roles) or AIDA… (users).
func newID(prefix string, n int) string {
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return prefix + string(b)
}

func normPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func arn(r *http.Request, kind, path, name string) string {
	return "arn:aws:iam::" + util.AccountID(r) + ":" + kind + normPath(path) + name
}

// parsePolicyArn splits arn:aws:iam::ACCOUNT:policy/path/NAME.
func parsePolicyArn(a string) (account, name string, ok bool) {
	parts := strings.SplitN(a, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "iam" || !strings.HasPrefix(parts[5], "policy/") {
		return "", "", false
	}
	return parts[4], parts[5][strings.LastIndex(parts[5], "/")+1:], true
}

// encodeDoc renders a policy document the way IAM returns it: URL-encoded.
// Clients decode with QueryUnescape, so spaces go out as %20, not "+".
func encodeDoc(doc string) string {
	return strings.ReplaceAll(url.QueryEscape(doc), "+", "%20")
}

func validDoc(w http.ResponseWriter, doc string) bool {
	if !json.Valid([]byte(doc)) {
		fail(w, http.StatusBadRequest, "MalformedPolicyDocument", "Syntax errors in policy.")
		return false
	}
	return true
}

//
// Store helpers
//

func (h *Handler) load(ns, typ, id string, v any) bool {
	res, err := h.Store.Get(id, "iam", typ, ns)
	if err != nil || res == nil {
		return false
	}
	return json.Unmarshal(res.Attributes, v) == nil
}

func (h *Handler) exists(ns, typ, id string) bool {
	var v map[string]any
	return h.load(ns, typ, id, &v)
}

func (h *Handler) create(ns, typ, id string, v any) error {
	buf, _ := json.Marshal(v)
	return h.Store.Create(&resource.Resource{ID: id, Namespace: ns, Service: "iam", Type: typ, Attributes: buf})
}

func (h *Handler) update(ns, typ, id string, v any) error {
	buf, _ := json.Marshal(v)
	return h.Store.Update(&resource.Resource{ID: id, Namespace: ns, Service: "iam", Type: typ, Attributes: buf})
}

func (h *Handler) remove(ns, typ, id string) {
	_ = h.Store.Delete(id, "iam", typ, ns)
}

// put creates or replaces a row.
func (h *Handler) put(ns, typ, id string, v any) error {
	if h.exists(ns, typ, id) {
		return h.update(ns, typ, id, v)
	}
	return h.create(ns, typ, id, v)
}

func (h *Handler) rows(ns, typ string) []resource.Resource {
	items, _ := h.Store.List("iam", typ, ns)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// related returns the second half of every "owner|x" row of typ.
func (h *Handler) related(ns, typ, owner string) []string {
	var out []string
	for _, it := range h.rows(ns, typ) {
		if o, x, ok := strings.Cut(it.ID, "|"); ok && o == owner {
			out = append(out, x)
		}
	}
	return out
}

// moveRelated renames the owner of every "owner|x" row of typ (when
// second is false) or the x of every "x|owner" row (when it is true).
func (h *Handler) moveRelated(ns, typ, from, to string, second bool) {
	for _, it := range h.rows(ns, typ) {
		a, b, ok := strings.Cut(it.ID, "|")
		if !ok {
			continue
		}
		var id string
		switch {
		case !second && a == from:
			id = to + "|" + b
		case second && b == from:
			id = a + "|" + to
		default:
			continue
		}
		h.remove(ns, typ, it.ID)
		_ = h.Store.Create(&resource.Resource{ID: id, Namespace: ns, Service: "iam", Type: typ, Attributes: it.Attributes})
	}
}

//
// Tags
//

func parseTags(r *http.Request) map[string]string {
	tags := map[string]string{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
		if k == "" {
			break
		}
		tags[k] = r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))
	}
	return tags
}

func tagList(tags map[string]string) []Tag {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Tag, 0, len(keys))
	for _, k := range keys {
		out = append(out, Tag{Key: k, Value: tags[k]})
	}
	return out
}

// taggable resolves the store row behind a tag operation's key parameter.
func (h *Handler) taggable(w http.ResponseWriter, r *http.Request, typ, param string) (ns, id string, rec map[string]any, ok bool) {
	vals, ok := required(w, r, param)
	if !ok {
		return "", "", nil, false
	}
	ns, id = util.GlobalNamespace(r), vals[0]
	if typ == tPolicy {
		_, id, _ = parsePolicyArn(id)
	}
	if !h.load(ns, typ, id, &rec) {
		noSuchEntity(w, fmt.Sprintf("The %s with name %s cannot be found.", strings.ReplaceAll(typ, "_", " "), id))
		return "", "", nil, false
	}
	return ns, id, rec, true
}

func recTags(rec map[string]any) map[string]string {
	tags := map[string]string{}
	if m, ok := rec["tags"].(map[string]any); ok {
		for k, v := range m {
			tags[k], _ = v.(string)
		}
	}
	return tags
}

func (h *Handler) tagOp(typ, param string) action {
	return func(w http.ResponseWriter, r *http.Request) {
		ns, id, rec, ok := h.taggable(w, r, typ, param)
		if !ok {
			return
		}
		tags := recTags(rec)
		for k, v := range parseTags(r) {
			tags[k] = v
		}
		if len(tags) > 50 {
			fail(w, http.StatusBadRequest, "LimitExceeded", "Cannot exceed quota for TagsPerResource: 50")
			return
		}
		rec["tags"] = tags
		h.update(ns, typ, id, rec)
		writeResult(w, r.FormValue("Action"), nil)
	}
}

func (h *Handler) untagOp(typ, param string) action {
	return func(w http.ResponseWriter, r *http.Request) {
		ns, id, rec, ok := h.taggable(w, r, typ, param)
		if !ok {
			return
		}
		tags := recTags(rec)
		for i := 1; ; i++ {
			k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
			if k == "" {
				break
			}
			delete(tags, k)
		}
		rec["tags"] = tags
		h.update(ns, typ, id, rec)
		writeResult(w, r.FormValue("Action"), nil)
	}
}

func (h *Handler) listTagsOp(typ, param string) action {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, rec, ok := h.taggable(w, r, typ, param)
		if !ok {
			return
		}
		writeResult(w, r.FormValue("Action"), struct {
			Tags        []Tag `xml:"Tags>member"`
			IsTruncated bool  `xml:"IsTruncated"`
		}{tagList(recTags(rec)), false})
	}
}

func boundary(arn string) *PermissionsBoundary {
	if arn == "" {
		return nil
	}
	return &PermissionsBoundary{PermissionsBoundaryType: "Policy", PermissionsBoundaryArn: arn}
}

func (h *Handler) putBoundary(typ, param string) action {
	return func(w http.ResponseWriter, r *http.Request) {
		ns, id, rec, ok := h.taggable(w, r, typ, param)
		if !ok {
			return
		}
		vals, ok := required(w, r, "PermissionsBoundary")
		if !ok {
			return
		}
		rec["permissions_boundary"] = vals[0]
		h.update(ns, typ, id, rec)
		writeResult(w, r.FormValue("Action"), nil)
	}
}

func (h *Handler) deleteBoundary(typ, param string) action {
	return func(w http.ResponseWriter, r *http.Request) {
		ns, id, rec, ok := h.taggable(w, r, typ, param)
		if !ok {
			return
		}
		delete(rec, "permissions_boundary")
		h.update(ns, typ, id, rec)
		writeResult(w, r.FormValue("Action"), nil)
	}
}

//
// Unmodelled credentials
//

func (h *Handler) noLoginProfile(w http.ResponseWriter, r *http.Request) {
	noSuchEntity(w, "Login Profile for User "+r.FormValue("UserName")+" cannot be found.")
}

func (h *Handler) emptyList(field string) action {
	return func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "<%s></%s><IsTruncated>false</IsTruncated>", field, field)
		writeResult(w, r.FormValue("Action"), struct {
			Inner string `xml:",innerxml"`
		}{buf.String()})
	}
}
