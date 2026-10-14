// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package secretsmanager

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

const (
	APIVersion = "2017-10-17"

	stageCurrent  = "AWSCURRENT"
	stagePrevious = "AWSPREVIOUS"

	// maxDeprecatedVersions is how many unlabelled versions AWS keeps
	// before it starts removing the oldest.
	maxDeprecatedVersions = 100
)

type Handler struct {
	Store resource.Store
	// mu serialises read-modify-write of secrets: the store has no
	// compare-and-swap, and Terraform writes a secret's version and policy
	// concurrently.
	mu sync.Mutex
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// smError is a Secrets Manager error response
type smError struct {
	errType string
	message string
}

func errNotFound() *smError {
	return &smError{"ResourceNotFoundException", "Secrets Manager can't find the specified secret."}
}

func errInvalidParam(msg string) *smError { return &smError{"InvalidParameterException", msg} }

func errInvalidRequest(msg string) *smError { return &smError{"InvalidRequestException", msg} }

func errMarkedForDeletion() *smError {
	return errInvalidRequest("You can't perform this operation on the secret because it was marked for deletion.")
}

// secretRecord is a secret as stored. The ARN, with its random suffix, is
// minted once at create and never regenerated.
type secretRecord struct {
	ARN             string          `json:"arn"`
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	KmsKeyID        string          `json:"kms_key_id,omitempty"`
	Type            string          `json:"type,omitempty"`
	CreatedDate     float64         `json:"created_date"`
	LastChangedDate float64         `json:"last_changed_date,omitempty"`
	DeletedDate     float64         `json:"deleted_date,omitempty"`
	Tags            []Tag           `json:"tags,omitempty"`
	ResourcePolicy  string          `json:"resource_policy,omitempty"`
	Versions        []versionRecord `json:"versions,omitempty"`
}

type versionRecord struct {
	VersionID    string   `json:"version_id"`
	SecretString *string  `json:"secret_string,omitempty"`
	SecretBinary *string  `json:"secret_binary,omitempty"`
	Stages       []string `json:"stages"`
	CreatedDate  float64  `json:"created_date"`
}

func secretArn(r *http.Request, name string) string {
	// AWS appends a hyphen and six random characters to the name
	return "arn:aws:secretsmanager:" + util.Region(r) + ":" + util.AccountID(r) + ":secret:" + name + "-" + randomSuffix(6)
}

func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[v.Int64()]
	}
	return string(b)
}

func epoch(t time.Time) float64 { return float64(t.UnixNano()/int64(time.Millisecond)) / 1000 }

// writeSecretsJSON writes JSON response with SecretsManager-specific Content-Type
func writeSecretsJSON(w http.ResponseWriter, status int, v interface{}) error {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("Server", "AmazonEC2")
	w.Header().Set("X-Amz-Request-Id", awsresponses.NextRequestID())
	w.Header().Set("X-Amz-Id-2", "opensnackfakeid")
	w.Header().Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	w.Header().Set("Connection", "close")

	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e *smError) {
	status := http.StatusBadRequest
	if e.errType == "InternalServiceError" {
		status = http.StatusInternalServerError
	}
	writeSecretsJSON(w, status, map[string]any{"__type": e.errType, "message": e.message})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := util.DecodeAWSJSON(r, v); err != nil {
		writeError(w, errInvalidParam("Invalid request body: "+err.Error()))
		return false
	}
	return true
}

// Dispatch handles SecretsManager JSON API requests
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")

	if target == "" {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingAuthenticationTokenException",
			"message": "Missing X-Amz-Target header",
		})
		return
	}

	// SecretsManager uses secretsmanager prefix
	if !strings.HasPrefix(target, "secretsmanager.") {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Invalid action: " + target,
		})
		return
	}

	action := strings.TrimPrefix(target, "secretsmanager.")
	switch action {
	case "CreateSecret":
		h.CreateSecret(w, r)
	case "DescribeSecret":
		h.DescribeSecret(w, r)
	case "UpdateSecret":
		h.UpdateSecret(w, r)
	case "GetSecretValue":
		h.GetSecretValue(w, r)
	case "PutSecretValue":
		h.PutSecretValue(w, r)
	case "UpdateSecretVersionStage":
		h.UpdateSecretVersionStage(w, r)
	case "ListSecretVersionIds":
		h.ListSecretVersionIds(w, r)
	case "ListSecrets":
		h.ListSecrets(w, r)
	case "DeleteSecret":
		h.DeleteSecret(w, r)
	case "RestoreSecret":
		h.RestoreSecret(w, r)
	case "GetResourcePolicy":
		h.GetResourcePolicy(w, r)
	case "PutResourcePolicy":
		h.PutResourcePolicy(w, r)
	case "DeleteResourcePolicy":
		h.DeleteResourcePolicy(w, r)
	case "ValidateResourcePolicy":
		h.ValidateResourcePolicy(w, r)
	case "TagResource":
		h.TagResource(w, r)
	case "UntagResource":
		h.UntagResource(w, r)
	case "GetRandomPassword":
		h.GetRandomPassword(w, r)
	default:
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Unknown operation: " + action,
		})
	}
}

// load finds a secret by name, full ARN or partial ARN (without the suffix).
// A secret whose recovery window has passed is purged here.
func (h *Handler) load(ns, secretID string) (*resource.Resource, *secretRecord, *smError) {
	if secretID == "" {
		return nil, nil, errInvalidParam("You must provide a value for SecretId.")
	}
	var candidates []string
	if strings.HasPrefix(secretID, "arn:") {
		i := strings.Index(secretID, ":secret:")
		if i < 0 {
			return nil, nil, errNotFound()
		}
		name := secretID[i+len(":secret:"):]
		if len(name) > 7 && name[len(name)-7] == '-' {
			candidates = append(candidates, name[:len(name)-7])
		}
		candidates = append(candidates, name)
	} else {
		candidates = []string{secretID}
	}

	for _, name := range candidates {
		res, err := h.Store.Get(name, "secretsmanager", "secret", ns)
		if err != nil {
			continue
		}
		var rec secretRecord
		if err := json.Unmarshal(res.Attributes, &rec); err != nil {
			return nil, nil, &smError{"InternalServiceError", "Failed to decode secret " + name}
		}
		if strings.HasPrefix(secretID, "arn:") && rec.ARN != secretID && !strings.HasPrefix(rec.ARN, secretID+"-") {
			continue
		}
		if rec.DeletedDate > 0 && float64(time.Now().Unix()) >= rec.DeletedDate {
			_ = h.Store.Delete(name, "secretsmanager", "secret", ns)
			return nil, nil, errNotFound()
		}
		return res, &rec, nil
	}
	return nil, nil, errNotFound()
}

// loadLive is load for operations that are refused on a secret pending deletion.
func (h *Handler) loadLive(ns, secretID string) (*resource.Resource, *secretRecord, *smError) {
	res, rec, e := h.load(ns, secretID)
	if e == nil && rec.DeletedDate > 0 {
		return nil, nil, errMarkedForDeletion()
	}
	return res, rec, e
}

func (h *Handler) save(res *resource.Resource, rec *secretRecord) *smError {
	buf, _ := json.Marshal(rec)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		return &smError{"InternalServiceError", "Failed to update secret: " + err.Error()}
	}
	return nil
}

// update loads a live secret, applies fn and saves it.
func (h *Handler) update(w http.ResponseWriter, r *http.Request, secretID string, fn func(*secretRecord) *smError, out func(*secretRecord) any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	res, rec, e := h.loadLive(util.NamespaceFromHeader(r), secretID)
	if e == nil {
		e = fn(rec)
	}
	if e == nil {
		e = h.save(res, rec)
	}
	if e != nil {
		writeError(w, e)
		return
	}
	writeSecretsJSON(w, http.StatusOK, out(rec))
}

func arnName(rec *secretRecord) any { return ARNNameOutput{ARN: rec.ARN, Name: rec.Name} }

func validValue(s, b *string) *smError {
	if s != nil && b != nil {
		return errInvalidParam("You can't specify both a binary secret value and a string secret value in the same secret.")
	}
	return nil
}

func hasStage(stages []string, stage string) bool {
	for _, s := range stages {
		if s == stage {
			return true
		}
	}
	return false
}

func removeStage(stages []string, stage string) []string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		if s != stage {
			out = append(out, s)
		}
	}
	return out
}

func (rec *secretRecord) version(id string) *versionRecord {
	for i := range rec.Versions {
		if rec.Versions[i].VersionID == id {
			return &rec.Versions[i]
		}
	}
	return nil
}

func (rec *secretRecord) versionWithStage(stage string) *versionRecord {
	for i := range rec.Versions {
		if hasStage(rec.Versions[i].Stages, stage) {
			return &rec.Versions[i]
		}
	}
	return nil
}

// moveStage attaches a staging label to one version, taking it off whichever
// version had it. Moving AWSCURRENT leaves AWSPREVIOUS on the old current
// version, as AWS does.
func (rec *secretRecord) moveStage(stage, to string) {
	var from string
	for i := range rec.Versions {
		v := &rec.Versions[i]
		if v.VersionID != to && hasStage(v.Stages, stage) {
			from = v.VersionID
			v.Stages = removeStage(v.Stages, stage)
		}
	}
	if v := rec.version(to); v != nil && !hasStage(v.Stages, stage) {
		v.Stages = append(v.Stages, stage)
	}
	if stage == stageCurrent && from != "" {
		rec.moveStage(stagePrevious, from)
	}
}

// addVersion stores a new secret value as a version with the given labels
// (AWSCURRENT by default). A repeated ClientRequestToken with the same value is
// idempotent; with a different value it is an error.
func (rec *secretRecord) addVersion(token string, s, b *string, stages []string) (*versionRecord, *smError) {
	if token == "" {
		token = uuid.New().String()
	}
	if v := rec.version(token); v != nil {
		if equalPtr(v.SecretString, s) && equalPtr(v.SecretBinary, b) {
			return v, nil
		}
		return nil, &smError{"ResourceExistsException",
			"You can't modify an existing version, you can only create a new version."}
	}
	if len(stages) == 0 {
		stages = []string{stageCurrent}
	}
	rec.Versions = append(rec.Versions, versionRecord{
		VersionID:    token,
		SecretString: s,
		SecretBinary: b,
		CreatedDate:  epoch(time.Now()),
	})
	for _, st := range stages {
		rec.moveStage(st, token)
	}
	rec.pruneDeprecated()
	return rec.version(token), nil
}

func (rec *secretRecord) pruneDeprecated() {
	n := 0
	for _, v := range rec.Versions {
		if len(v.Stages) == 0 {
			n++
		}
	}
	if n <= maxDeprecatedVersions {
		return
	}
	kept := rec.Versions[:0]
	for _, v := range rec.Versions {
		if len(v.Stages) == 0 && n > maxDeprecatedVersions {
			n--
			continue
		}
		kept = append(kept, v)
	}
	rec.Versions = kept
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// versionsToStages maps labelled versions to their labels; deprecated
// (unlabelled) versions are left out, as in AWS.
func (rec *secretRecord) versionsToStages() map[string][]string {
	out := map[string][]string{}
	for _, v := range rec.Versions {
		if len(v.Stages) > 0 {
			out[v.VersionID] = v.Stages
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CreateSecret creates a new secret
func (h *Handler) CreateSecret(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req CreateSecretInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if req.Name == "" {
		writeError(w, errInvalidParam("You must provide a value for Name."))
		return
	}
	if e := validValue(req.SecretString, req.SecretBinary); e != nil {
		writeError(w, e)
		return
	}

	if _, existing, e := h.load(ns, req.Name); e == nil {
		if existing.DeletedDate > 0 {
			writeError(w, errInvalidRequest("You can't create this secret because a secret with this name is already scheduled for deletion."))
			return
		}
		writeError(w, &smError{"ResourceExistsException", "The operation failed because the secret " + req.Name + " already exists."})
		return
	}

	now := epoch(time.Now())
	rec := &secretRecord{
		ARN:             secretArn(r, req.Name),
		Name:            req.Name,
		Description:     req.Description,
		KmsKeyID:        req.KmsKeyId,
		Type:            req.Type,
		CreatedDate:     now,
		LastChangedDate: now,
		Tags:            req.Tags,
	}
	out := CreateSecretOutput{ARN: rec.ARN, Name: rec.Name}
	if req.SecretString != nil || req.SecretBinary != nil {
		v, e := rec.addVersion(req.ClientRequestToken, req.SecretString, req.SecretBinary, nil)
		if e != nil {
			writeError(w, e)
			return
		}
		out.VersionId = v.VersionID
	}

	buf, _ := json.Marshal(rec)
	res := &resource.Resource{
		ID:         req.Name,
		Namespace:  ns,
		Service:    "secretsmanager",
		Type:       "secret",
		Attributes: buf,
	}
	if err := h.Store.Create(res); err != nil {
		writeError(w, &smError{"InternalServiceError", "Failed to create secret: " + err.Error()})
		return
	}
	writeSecretsJSON(w, http.StatusOK, out)
}

// DescribeSecret describes a secret, including one that is pending deletion
func (h *Handler) DescribeSecret(w http.ResponseWriter, r *http.Request) {
	var req SecretIdInput
	if !decode(w, r, &req) {
		return
	}
	_, rec, e := h.load(util.NamespaceFromHeader(r), req.SecretId)
	if e != nil {
		writeError(w, e)
		return
	}
	writeSecretsJSON(w, http.StatusOK, DescribeSecretOutput{
		ARN:                rec.ARN,
		Name:               rec.Name,
		Description:        rec.Description,
		KmsKeyId:           rec.KmsKeyID,
		LastChangedDate:    rec.LastChangedDate,
		DeletedDate:        rec.DeletedDate,
		Tags:               rec.Tags,
		VersionIdsToStages: rec.versionsToStages(),
		CreatedDate:        rec.CreatedDate,
		Type:               rec.Type,
	})
}

// UpdateSecret changes the description or KMS key, and with a value adds a
// new AWSCURRENT version.
func (h *Handler) UpdateSecret(w http.ResponseWriter, r *http.Request) {
	var req UpdateSecretInput
	if !decode(w, r, &req) {
		return
	}
	if e := validValue(req.SecretString, req.SecretBinary); e != nil {
		writeError(w, e)
		return
	}
	var versionID string
	h.update(w, r, req.SecretId, func(rec *secretRecord) *smError {
		if req.Description != nil {
			rec.Description = *req.Description
		}
		if req.KmsKeyId != nil {
			rec.KmsKeyID = *req.KmsKeyId
		}
		if req.Type != nil {
			rec.Type = *req.Type
		}
		if req.SecretString != nil || req.SecretBinary != nil {
			v, e := rec.addVersion(req.ClientRequestToken, req.SecretString, req.SecretBinary, nil)
			if e != nil {
				return e
			}
			versionID = v.VersionID
		}
		rec.LastChangedDate = epoch(time.Now())
		return nil
	}, func(rec *secretRecord) any {
		return UpdateSecretOutput{ARN: rec.ARN, Name: rec.Name, VersionId: versionID}
	})
}

// GetSecretValue returns one version, chosen by VersionId and/or
// VersionStage (AWSCURRENT when neither is given).
func (h *Handler) GetSecretValue(w http.ResponseWriter, r *http.Request) {
	var req GetSecretValueInput
	if !decode(w, r, &req) {
		return
	}
	_, rec, e := h.loadLive(util.NamespaceFromHeader(r), req.SecretId)
	if e != nil {
		writeError(w, e)
		return
	}

	var v *versionRecord
	switch {
	case req.VersionId != "":
		v = rec.version(req.VersionId)
		if v != nil && req.VersionStage != "" && !hasStage(v.Stages, req.VersionStage) {
			writeError(w, errInvalidRequest("The secret version "+req.VersionId+" does not have the staging label "+req.VersionStage+"."))
			return
		}
		if v == nil {
			writeError(w, &smError{"ResourceNotFoundException",
				"Secrets Manager can't find the specified secret value for VersionId: " + req.VersionId})
			return
		}
	default:
		stage := req.VersionStage
		if stage == "" {
			stage = stageCurrent
		}
		if v = rec.versionWithStage(stage); v == nil {
			writeError(w, &smError{"ResourceNotFoundException",
				"Secrets Manager can't find the specified secret value for staging label: " + stage})
			return
		}
	}

	stages := v.Stages
	if stages == nil {
		stages = []string{}
	}
	writeSecretsJSON(w, http.StatusOK, GetSecretValueOutput{
		ARN:           rec.ARN,
		Name:          rec.Name,
		VersionId:     v.VersionID,
		SecretString:  v.SecretString,
		SecretBinary:  v.SecretBinary,
		VersionStages: stages,
		CreatedDate:   v.CreatedDate,
	})
}

// PutSecretValue adds a version. With the default labels it becomes
// AWSCURRENT and the previous current version becomes AWSPREVIOUS.
func (h *Handler) PutSecretValue(w http.ResponseWriter, r *http.Request) {
	var req PutSecretValueInput
	if !decode(w, r, &req) {
		return
	}
	if req.SecretString == nil && req.SecretBinary == nil {
		writeError(w, errInvalidParam("You must provide either SecretString or SecretBinary."))
		return
	}
	if e := validValue(req.SecretString, req.SecretBinary); e != nil {
		writeError(w, e)
		return
	}
	var v *versionRecord
	h.update(w, r, req.SecretId, func(rec *secretRecord) *smError {
		var e *smError
		v, e = rec.addVersion(req.ClientRequestToken, req.SecretString, req.SecretBinary, req.VersionStages)
		rec.LastChangedDate = epoch(time.Now())
		return e
	}, func(rec *secretRecord) any {
		return PutSecretValueOutput{ARN: rec.ARN, Name: rec.Name, VersionId: v.VersionID, VersionStages: v.Stages}
	})
}

// UpdateSecretVersionStage moves a staging label between versions
func (h *Handler) UpdateSecretVersionStage(w http.ResponseWriter, r *http.Request) {
	var req UpdateSecretVersionStageInput
	if !decode(w, r, &req) {
		return
	}
	if req.VersionStage == "" {
		writeError(w, errInvalidParam("You must provide a value for VersionStage."))
		return
	}
	h.update(w, r, req.SecretId, func(rec *secretRecord) *smError {
		holder := rec.versionWithStage(req.VersionStage)
		if req.RemoveFromVersionId != "" {
			if holder == nil || holder.VersionID != req.RemoveFromVersionId {
				return errInvalidParam("The staging label " + req.VersionStage +
					" is currently not attached to version " + req.RemoveFromVersionId + ".")
			}
		}
		if req.MoveToVersionId == "" {
			if req.RemoveFromVersionId == "" {
				return errInvalidParam("You must provide MoveToVersionId or RemoveFromVersionId.")
			}
			if req.VersionStage == stageCurrent {
				return errInvalidParam("You can only move staging label AWSCURRENT to a different secret version. It can't be completely removed.")
			}
			holder.Stages = removeStage(holder.Stages, req.VersionStage)
		} else {
			if rec.version(req.MoveToVersionId) == nil {
				return &smError{"ResourceNotFoundException", "Secrets Manager can't find the specified secret version: " + req.MoveToVersionId}
			}
			if holder != nil && holder.VersionID != req.MoveToVersionId && req.RemoveFromVersionId == "" {
				return errInvalidParam("The parameter RemoveFromVersionId can't be empty. Staging label " +
					req.VersionStage + " is currently attached to version " + holder.VersionID +
					", so you must explicitly reference that version in RemoveFromVersionId.")
			}
			rec.moveStage(req.VersionStage, req.MoveToVersionId)
		}
		rec.LastChangedDate = epoch(time.Now())
		return nil
	}, arnName)
}

// ListSecretVersionIds lists the labelled versions, newest first
func (h *Handler) ListSecretVersionIds(w http.ResponseWriter, r *http.Request) {
	var req ListSecretVersionIdsInput
	if !decode(w, r, &req) {
		return
	}
	_, rec, e := h.loadLive(util.NamespaceFromHeader(r), req.SecretId)
	if e != nil {
		writeError(w, e)
		return
	}
	versions := make([]SecretVersionsListEntry, 0, len(rec.Versions))
	for i := len(rec.Versions) - 1; i >= 0; i-- {
		v := rec.Versions[i]
		if len(v.Stages) == 0 && !req.IncludeDeprecated {
			continue
		}
		entry := SecretVersionsListEntry{VersionId: v.VersionID, VersionStages: v.Stages, CreatedDate: v.CreatedDate}
		if rec.KmsKeyID != "" {
			entry.KmsKeyIds = []string{rec.KmsKeyID}
		}
		versions = append(versions, entry)
	}
	start, end, next, perr := page(len(versions), req.MaxResults, req.NextToken)
	if perr != nil {
		writeError(w, perr)
		return
	}
	writeSecretsJSON(w, http.StatusOK, ListSecretVersionIdsOutput{
		ARN: rec.ARN, Name: rec.Name, Versions: versions[start:end], NextToken: next,
	})
}

// page applies MaxResults/NextToken paging; the token is an offset.
func page(n int, maxResults int32, token string) (int, int, string, *smError) {
	start := 0
	if token != "" {
		i, err := strconv.Atoi(token)
		if err != nil || i < 0 || i > n {
			return 0, 0, "", errInvalidParam("Invalid NextToken")
		}
		start = i
	}
	size := 100
	if maxResults > 0 {
		size = int(maxResults)
	}
	end := start + size
	if end >= n {
		return start, n, "", nil
	}
	return start, end, strconv.Itoa(end), nil
}

// matchFilter reports whether a value matches a ListSecrets filter value:
// a case-sensitive prefix match, negated by a leading "!".
func matchFilter(values []string, candidates ...string) bool {
	for _, v := range values {
		neg := strings.HasPrefix(v, "!")
		v = strings.TrimPrefix(v, "!")
		hit := false
		for _, c := range candidates {
			if strings.HasPrefix(c, v) {
				hit = true
				break
			}
		}
		if hit != neg {
			return true
		}
	}
	return false
}

func (rec *secretRecord) matches(f Filter) bool {
	var tagKeys, tagValues []string
	for _, t := range rec.Tags {
		tagKeys = append(tagKeys, t.Key)
		tagValues = append(tagValues, t.Value)
	}
	switch f.Key {
	case "name":
		return matchFilter(f.Values, rec.Name)
	case "description":
		return matchFilter(f.Values, rec.Description)
	case "tag-key":
		return matchFilter(f.Values, tagKeys...)
	case "tag-value":
		return matchFilter(f.Values, tagValues...)
	case "all":
		return matchFilter(f.Values, append(append([]string{rec.Name, rec.Description}, tagKeys...), tagValues...)...)
	}
	return true
}

// ListSecrets lists secrets, leaving out those pending deletion unless asked
func (h *Handler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req ListSecretsInput
	if !decode(w, r, &req) {
		return
	}

	rows, err := h.Store.List("secretsmanager", "secret", ns)
	if err != nil {
		writeError(w, &smError{"InternalServiceError", "Failed to list secrets: " + err.Error()})
		return
	}

	var recs []secretRecord
	for _, row := range rows {
		var rec secretRecord
		if json.Unmarshal(row.Attributes, &rec) != nil {
			continue
		}
		if rec.DeletedDate > 0 && float64(time.Now().Unix()) >= rec.DeletedDate {
			_ = h.Store.Delete(row.ID, "secretsmanager", "secret", ns)
			continue
		}
		if rec.DeletedDate > 0 && !req.IncludePlannedDeletion {
			continue
		}
		ok := true
		for _, f := range req.Filters {
			if !rec.matches(f) {
				ok = false
				break
			}
		}
		if ok {
			recs = append(recs, rec)
		}
	}
	sort.Slice(recs, func(i, j int) bool {
		if req.SortOrder == "asc" {
			return recs[i].CreatedDate < recs[j].CreatedDate
		}
		return recs[i].CreatedDate > recs[j].CreatedDate
	})

	start, end, next, perr := page(len(recs), req.MaxResults, req.NextToken)
	if perr != nil {
		writeError(w, perr)
		return
	}
	list := make([]SecretListEntry, 0, end-start)
	for _, rec := range recs[start:end] {
		list = append(list, SecretListEntry{
			ARN:                    rec.ARN,
			Name:                   rec.Name,
			Description:            rec.Description,
			KmsKeyId:               rec.KmsKeyID,
			LastChangedDate:        rec.LastChangedDate,
			DeletedDate:            rec.DeletedDate,
			Tags:                   rec.Tags,
			SecretVersionsToStages: rec.versionsToStages(),
			CreatedDate:            rec.CreatedDate,
			Type:                   rec.Type,
		})
	}
	writeSecretsJSON(w, http.StatusOK, ListSecretsOutput{SecretList: list, NextToken: next})
}

// DeleteSecret schedules deletion after a recovery window (30 days by
// default) or, with ForceDeleteWithoutRecovery, deletes immediately.
func (h *Handler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req DeleteSecretInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	force := req.ForceDeleteWithoutRecovery != nil && *req.ForceDeleteWithoutRecovery
	if force && req.RecoveryWindowInDays != nil {
		writeError(w, errInvalidParam("You can't use ForceDeleteWithoutRecovery in conjunction with RecoveryWindowInDays."))
		return
	}
	window := int64(30)
	if req.RecoveryWindowInDays != nil {
		window = *req.RecoveryWindowInDays
		if window < 7 || window > 30 {
			writeError(w, errInvalidParam("The RecoveryWindowInDays value must be between 7 and 30 days (inclusive)."))
			return
		}
	}

	res, rec, e := h.load(ns, req.SecretId)
	if e != nil {
		writeError(w, e)
		return
	}

	now := time.Now().UTC()
	if force {
		if err := h.Store.Delete(res.ID, "secretsmanager", "secret", ns); err != nil {
			writeError(w, &smError{"InternalServiceError", "Failed to delete secret: " + err.Error()})
			return
		}
		writeSecretsJSON(w, http.StatusOK, DeleteSecretOutput{ARN: rec.ARN, Name: rec.Name, DeletionDate: epoch(now)})
		return
	}
	if rec.DeletedDate > 0 {
		writeError(w, errMarkedForDeletion())
		return
	}
	rec.DeletedDate = float64(now.AddDate(0, 0, int(window)).Unix())
	if e := h.save(res, rec); e != nil {
		writeError(w, e)
		return
	}
	writeSecretsJSON(w, http.StatusOK, DeleteSecretOutput{ARN: rec.ARN, Name: rec.Name, DeletionDate: rec.DeletedDate})
}

// RestoreSecret cancels a scheduled deletion
func (h *Handler) RestoreSecret(w http.ResponseWriter, r *http.Request) {
	var req SecretIdInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	res, rec, e := h.load(util.NamespaceFromHeader(r), req.SecretId)
	if e == nil {
		rec.DeletedDate = 0
		e = h.save(res, rec)
	}
	if e != nil {
		writeError(w, e)
		return
	}
	writeSecretsJSON(w, http.StatusOK, arnName(rec))
}

// GetResourcePolicy returns the policy exactly as stored. A secret without
// one has no ResourcePolicy in the response.
func (h *Handler) GetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req SecretIdInput
	if !decode(w, r, &req) {
		return
	}
	_, rec, e := h.loadLive(util.NamespaceFromHeader(r), req.SecretId)
	if e != nil {
		writeError(w, e)
		return
	}
	writeSecretsJSON(w, http.StatusOK, GetResourcePolicyOutput{ARN: rec.ARN, Name: rec.Name, ResourcePolicy: rec.ResourcePolicy})
}

func validatePolicy(policy string) *smError {
	var doc map[string]any
	if policy == "" || json.Unmarshal([]byte(policy), &doc) != nil {
		return &smError{"MalformedPolicyDocumentException", "This resource policy contains a syntax error."}
	}
	return nil
}

func (h *Handler) PutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req PutResourcePolicyInput
	if !decode(w, r, &req) {
		return
	}
	if e := validatePolicy(req.ResourcePolicy); e != nil {
		writeError(w, e)
		return
	}
	h.update(w, r, req.SecretId, func(rec *secretRecord) *smError {
		rec.ResourcePolicy = req.ResourcePolicy
		return nil
	}, arnName)
}

func (h *Handler) DeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req SecretIdInput
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, req.SecretId, func(rec *secretRecord) *smError {
		rec.ResourcePolicy = ""
		return nil
	}, arnName)
}

// ValidateResourcePolicy only checks that the policy is a JSON object;
// opensnack does not evaluate policies.
func (h *Handler) ValidateResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req ValidateResourcePolicyInput
	if !decode(w, r, &req) {
		return
	}
	if req.SecretId != "" {
		if _, _, e := h.load(util.NamespaceFromHeader(r), req.SecretId); e != nil {
			writeError(w, e)
			return
		}
	}
	if e := validatePolicy(req.ResourcePolicy); e != nil {
		writeError(w, e)
		return
	}
	writeSecretsJSON(w, http.StatusOK, ValidateResourcePolicyOutput{PolicyValidationPassed: true, ValidationErrors: []ValidationErrorsEntry{}})
}

func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	var req TagResourceInput
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, req.SecretId, func(rec *secretRecord) *smError {
		for _, t := range req.Tags {
			found := false
			for i := range rec.Tags {
				if rec.Tags[i].Key == t.Key {
					rec.Tags[i].Value = t.Value
					found = true
				}
			}
			if !found {
				rec.Tags = append(rec.Tags, t)
			}
		}
		return nil
	}, func(*secretRecord) any { return struct{}{} })
}

func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	var req UntagResourceInput
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, req.SecretId, func(rec *secretRecord) *smError {
		kept := rec.Tags[:0]
		for _, t := range rec.Tags {
			if !hasStage(req.TagKeys, t.Key) {
				kept = append(kept, t)
			}
		}
		rec.Tags = kept
		return nil
	}, func(*secretRecord) any { return struct{}{} })
}

// GetRandomPassword generates a password from the requested character classes
func (h *Handler) GetRandomPassword(w http.ResponseWriter, r *http.Request) {
	var req GetRandomPasswordInput
	if !decode(w, r, &req) {
		return
	}
	length := int64(32)
	if req.PasswordLength != nil {
		length = *req.PasswordLength
	}
	if length < 1 || length > 4096 {
		writeError(w, errInvalidParam("PasswordLength must be between 1 and 4096."))
		return
	}
	strip := func(set string) string {
		return strings.Map(func(c rune) rune {
			if strings.ContainsRune(req.ExcludeCharacters, c) {
				return -1
			}
			return c
		}, set)
	}
	var classes []string
	add := func(exclude bool, set string) {
		if s := strip(set); !exclude && s != "" {
			classes = append(classes, s)
		}
	}
	add(req.ExcludeLowercase, "abcdefghijklmnopqrstuvwxyz")
	add(req.ExcludeUppercase, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	add(req.ExcludeNumbers, "0123456789")
	add(req.ExcludePunctuation, "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~")
	add(!req.IncludeSpace, " ")
	if len(classes) == 0 {
		writeError(w, errInvalidParam("The password can't be generated because every character class is excluded."))
		return
	}
	requireEach := req.RequireEachIncludedType == nil || *req.RequireEachIncludedType
	if requireEach && int64(len(classes)) > length {
		writeError(w, errInvalidParam("The password length is too short to include every character type."))
		return
	}

	pick := func(set string) byte {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		return set[v.Int64()]
	}
	all := strings.Join(classes, "")
	pw := make([]byte, length)
	for i := range pw {
		pw[i] = pick(all)
	}
	if requireEach {
		// Put one of each class at distinct random positions
		perm := make([]int, length)
		for i := range perm {
			perm[i] = i
		}
		for i := len(perm) - 1; i > 0; i-- {
			j, _ := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
			perm[i], perm[j.Int64()] = perm[j.Int64()], perm[i]
		}
		for i, set := range classes {
			pw[perm[i]] = pick(set)
		}
	}
	writeSecretsJSON(w, http.StatusOK, map[string]string{"RandomPassword": string(pw)})
}
