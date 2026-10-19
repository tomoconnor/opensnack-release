// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/json"
	"net/http"
	"strings"

	"opensnack/internal/util"
)

// Bucket policies are enforced only as explicit Denies on deletes: deleting
// the bucket, an object or an object version. That is what a "nobody
// deletes this" policy is for, and nothing else in opensnack checks
// permissions, so Allows mean nothing. Policy changes are never denied,
// as AWS always lets the account's root user fix a policy that locks
// everyone out.
//
// A Deny applies when its action, resource and principal match the request
// and every condition holds. Conditions understood: aws:PrincipalArn and
// aws:PrincipalAccount with the String and Arn operators (and their Not
// and IfExists forms), and aws:SecureTransport (always true). A statement
// with any other condition is left out, so an unknown condition never
// denies.

// caller is the ARN aws:PrincipalArn means for the request: the role for
// assumed-role sessions, else the namespace's user.
func (h *Handler) caller(r *http.Request) string {
	if h.Principal != nil {
		if arn := h.Principal(r); arn != "" {
			return arn
		}
	}
	id := util.IdentityFrom(r)
	return "arn:aws:iam::" + id.AccountID + ":user/" + id.Namespace
}

// denied says whether meta's policy explicitly denies the request's caller
// action on resource (an S3 ARN).
func (h *Handler) denied(r *http.Request, meta *bucketMeta, action, resource string) bool {
	if meta == nil || meta.Policy == "" {
		return false
	}
	return policyDenies(meta.Policy, h.caller(r), util.AccountID(r), action, resource)
}

func policyDenies(policy, principal, account, action, resource string) bool {
	var doc struct {
		Statement json.RawMessage
	}
	if json.Unmarshal([]byte(policy), &doc) != nil {
		return false
	}
	var stmts []map[string]any
	if json.Unmarshal(doc.Statement, &stmts) != nil {
		var one map[string]any
		if json.Unmarshal(doc.Statement, &one) != nil {
			return false
		}
		stmts = []map[string]any{one}
	}
	for _, s := range stmts {
		if s["Effect"] != "Deny" {
			continue
		}
		if !matchesEither(s, "Action", "NotAction", action, true) || !matchesEither(s, "Resource", "NotResource", resource, false) {
			continue
		}
		if p, ok := s["Principal"]; ok && !principalMatches(p, principal, account) {
			continue
		}
		if p, ok := s["NotPrincipal"]; ok && principalMatches(p, principal, account) {
			continue
		}
		if conditionsHold(s["Condition"], principal, account) {
			return true
		}
	}
	return false
}

// matchesEither applies an element or its Not form: Action/NotAction,
// Resource/NotResource.
func matchesEither(s map[string]any, key, notKey, value string, fold bool) bool {
	if v, ok := s[notKey]; ok {
		return !anyGlob(strs(v), value, fold)
	}
	return anyGlob(strs(s[key]), value, fold)
}

// principalMatches reads a Principal: "*", {"AWS": "*"}, or AWS ARNs and
// account IDs (an account means everyone in it).
func principalMatches(p any, principal, account string) bool {
	if s, ok := p.(string); ok {
		return s == "*"
	}
	m, _ := p.(map[string]any)
	for _, a := range strs(m["AWS"]) {
		if a == "*" || a == principal || a == account || a == "arn:aws:iam::"+account+":root" {
			return true
		}
	}
	return false
}

func conditionsHold(c any, principal, account string) bool {
	if c == nil {
		return true
	}
	ops, ok := c.(map[string]any)
	if !ok {
		return false
	}
	for op, kv := range ops {
		keys, ok := kv.(map[string]any)
		if !ok {
			return false
		}
		for key, want := range keys {
			var have string
			switch strings.ToLower(key) {
			case "aws:principalarn":
				have = principal
			case "aws:principalaccount":
				have = account
			case "aws:securetransport":
				have = "true"
			default:
				return false
			}
			if !conditionHolds(op, have, strs(want)) {
				return false
			}
		}
	}
	return true
}

func conditionHolds(op, have string, want []string) bool {
	base := strings.TrimSuffix(op, "IfExists")
	switch base {
	case "StringEquals", "ArnEquals":
		return anyEqual(want, have, false)
	case "StringEqualsIgnoreCase":
		return anyEqual(want, have, true)
	case "StringLike", "ArnLike":
		return anyGlob(want, have, false)
	case "StringNotEquals", "ArnNotEquals":
		return !anyEqual(want, have, false)
	case "StringNotEqualsIgnoreCase":
		return !anyEqual(want, have, true)
	case "StringNotLike", "ArnNotLike":
		return !anyGlob(want, have, false)
	case "Bool":
		return anyEqual(want, have, true)
	}
	return false
}

func anyEqual(want []string, have string, fold bool) bool {
	for _, w := range want {
		if w == have || (fold && strings.EqualFold(w, have)) {
			return true
		}
	}
	return false
}

func anyGlob(patterns []string, s string, fold bool) bool {
	for _, p := range patterns {
		if fold {
			if glob(strings.ToLower(p), strings.ToLower(s)) {
				return true
			}
		} else if glob(p, s) {
			return true
		}
	}
	return false
}

// glob is IAM's wildcard match: * is any run of characters (slashes
// included), ? is any one.
func glob(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for i := len(s); i >= 0; i-- {
				if glob(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
		default:
			if s == "" || s[0] != p[0] {
				return false
			}
		}
		p, s = p[1:], s[1:]
	}
	return s == ""
}

// strs reads a policy value that is a string or a list of strings.
func strs(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func accessDenied(w http.ResponseWriter) {
	writeS3Error(w, http.StatusForbidden, "AccessDenied", "Access Denied", "")
}

func objectARN(bucket, key string) string { return "arn:aws:s3:::" + bucket + "/" + key }

// deleteAction is the permission a delete needs: a version or the key.
func deleteAction(versionID string) string {
	if versionID != "" {
		return "s3:DeleteObjectVersion"
	}
	return "s3:DeleteObject"
}
