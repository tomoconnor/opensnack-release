// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sts

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/api/iam"
	"opensnack/internal/auth"
	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const APIVersion = "2011-06-15"

type Handler struct {
	Store    resource.Store
	Sessions *Sessions
}

// NewHandler serves STS. Store is where AssumeRole looks roles up; sessions
// is the registry issued keys go into (register it with the authenticator so
// they verify).
func NewHandler(store resource.Store, sessions *Sessions) *Handler {
	if sessions == nil {
		sessions = NewSessions()
	}
	return &Handler{Store: store, Sessions: sessions}
}

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidParameterValue", "Failed to parse form", "")
		return
	}
	action := r.FormValue("Action")

	switch action {
	case "GetCallerIdentity":
		h.GetCallerIdentity(w, r)
	case "AssumeRole":
		h.AssumeRole(w, r)
	case "GetSessionToken":
		h.GetSessionToken(w, r)
	default:
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidAction",
			"Unknown STS action",
			action,
		)
	}
}

// GetCallerIdentity reports who the credentials belong to: the assumed role
// for keys AssumeRole issued, otherwise an IAM user named after the
// caller's namespace, in their account.
func (h *Handler) GetCallerIdentity(w http.ResponseWriter, r *http.Request) {
	id := util.IdentityFrom(r)
	arn, userID := "arn:aws:iam::"+id.AccountID+":user/"+id.Namespace, id.Namespace
	if sess := h.Sessions.Get(id.AccessKeyID); sess != nil {
		arn, userID = sess.Arn, sess.UserID
	}
	resp := GetCallerIdentityResponse{
		GetCallerIdentityResult: GetCallerIdentityResult{
			Arn:     arn,
			UserId:  userID,
			Account: id.AccountID,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

var sessionName = regexp.MustCompile(`^[\w+=,.@-]{2,64}$`)

func validationError(w http.ResponseWriter, msg string) {
	awsresponses.WriteErrorXML(w, http.StatusBadRequest, "ValidationError", msg, "")
}

// duration parses DurationSeconds, defaulting to def and bounded by [min, max].
func duration(w http.ResponseWriter, r *http.Request, def, min, max int) (time.Duration, bool) {
	v := r.FormValue("DurationSeconds")
	if v == "" {
		return time.Duration(def) * time.Second, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		validationError(w, "The requested DurationSeconds must be between "+strconv.Itoa(min)+" and "+strconv.Itoa(max)+" seconds.")
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// issue mints a session for the caller's namespace and account.
func (h *Handler) issue(id *util.Identity, ttl time.Duration, arn, userID string) *Session {
	sess := &Session{
		Credential: auth.Credential{
			AccessKeyID:     newAccessKeyID(),
			SecretAccessKey: iam.NewSecret(),
			AccountID:       id.AccountID,
			Namespace:       id.Namespace,
		},
		SessionToken: "FwoGZXIvYXdzE" + randomB64(192),
		Expires:      time.Now().Add(ttl).UTC().Truncate(time.Second),
		Arn:          arn,
		UserID:       userID,
	}
	h.Sessions.add(sess)
	return sess
}

func (s *Session) wire() Credentials {
	return Credentials{
		AccessKeyId:     s.Credential.AccessKeyID,
		SecretAccessKey: s.Credential.SecretAccessKey,
		SessionToken:    s.SessionToken,
		Expiration:      s.Expires.Format("2006-01-02T15:04:05Z"),
	}
}

func (h *Handler) AssumeRole(w http.ResponseWriter, r *http.Request) {
	roleArn, session := r.FormValue("RoleArn"), r.FormValue("RoleSessionName")
	if roleArn == "" || session == "" {
		validationError(w, "RoleArn and RoleSessionName are required.")
		return
	}
	if !sessionName.MatchString(session) {
		validationError(w, "1 validation error detected: Value '"+session+"' at 'roleSessionName' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\w+=,.@-]*")
		return
	}

	id := util.IdentityFrom(r)
	caller := "arn:aws:iam::" + id.AccountID + ":user/" + id.Namespace
	if sess := h.Sessions.Get(id.AccessKeyID); sess != nil {
		caller = sess.Arn
	}
	// Roles live in the caller's own account; anything else (another
	// account, a malformed ARN, a role that doesn't exist) is denied the way
	// AWS denies it.
	parts := strings.SplitN(roleArn, ":", 6)
	var role *iam.RoleInfo
	if len(parts) == 6 && parts[4] == id.AccountID && strings.HasPrefix(parts[5], "role/") {
		name := parts[5][strings.LastIndex(parts[5], "/")+1:]
		if info, ok := iam.LookupRole(h.Store, util.GlobalNamespace(r), id.AccountID, name); ok && info.Arn == roleArn {
			role = info
		}
	}
	if role == nil {
		awsresponses.WriteErrorXML(w, http.StatusForbidden, "AccessDenied",
			"User: "+caller+" is not authorized to perform: sts:AssumeRole on resource: "+roleArn, "")
		return
	}

	ttl, ok := duration(w, r, 3600, 900, role.MaxSessionDuration)
	if !ok {
		return
	}
	assumedID := role.ID + ":" + session
	assumedArn := "arn:aws:sts::" + id.AccountID + ":assumed-role/" + role.Name + "/" + session
	sess := h.issue(id, ttl, assumedArn, assumedID)

	awsresponses.WriteXML(w, AssumeRoleResponse{
		Result: AssumeRoleResult{
			Credentials:     sess.wire(),
			AssumedRoleUser: AssumedRoleUser{AssumedRoleId: assumedID, Arn: assumedArn},
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	})
}

// GetSessionToken issues temporary keys that act as the caller.
func (h *Handler) GetSessionToken(w http.ResponseWriter, r *http.Request) {
	id := util.IdentityFrom(r)
	if strings.HasPrefix(id.AccessKeyID, "ASIA") && h.Sessions.Get(id.AccessKeyID) != nil {
		awsresponses.WriteErrorXML(w, http.StatusForbidden, "AccessDenied",
			"Cannot call GetSessionToken with session credentials", "")
		return
	}
	ttl, ok := duration(w, r, 43200, 900, 129600)
	if !ok {
		return
	}
	sess := h.issue(id, ttl, "arn:aws:iam::"+id.AccountID+":user/"+id.Namespace, id.Namespace)
	awsresponses.WriteXML(w, GetSessionTokenResponse{
		Result:           GetSessionTokenResult{Credentials: sess.wire()},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	})
}
