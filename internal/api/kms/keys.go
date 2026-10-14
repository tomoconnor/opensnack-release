// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

// Error is a KMS error. It is also what the in-process API (EncryptFor and
// friends) returns, so other services can map it to their own error shapes.
type Error struct {
	Status  int
	Type    string
	Message string
}

func (e *Error) Error() string { return e.Type + ": " + e.Message }

func errNotFound(msg string) *Error {
	return &Error{http.StatusBadRequest, "NotFoundException", msg}
}

func errInvalidState(msg string) *Error {
	return &Error{http.StatusBadRequest, "KMSInvalidStateException", msg}
}

func errValidation(msg string) *Error {
	return &Error{http.StatusBadRequest, "ValidationException", msg}
}

func errInternal(msg string) *Error {
	return &Error{http.StatusInternalServerError, "KMSInternalException", msg}
}

// writeMu serialises read-modify-write of keys and aliases. The store has no
// compare-and-swap, and Terraform issues concurrent calls against one key.
// It is package level because SSM holds its own Handler on the same store.
var writeMu sync.Mutex

// keyRecord is what a key looks like in the store.
type keyRecord struct {
	Metadata             KeyMetadata `json:"key_metadata"`
	Material             string      `json:"key_material,omitempty"`
	RotationEnabled      bool        `json:"rotation_enabled"`
	RotationPeriodInDays int32       `json:"rotation_period_in_days,omitempty"`
	NextRotationDate     float64     `json:"next_rotation_date,omitempty"`
	Policy               string      `json:"policy,omitempty"`
	Tags                 []Tag       `json:"tags,omitempty"`
}

// aliasRecord is what an alias looks like in the store; its ID is the alias name.
type aliasRecord struct {
	AliasName       string  `json:"alias_name"`
	TargetKeyID     string  `json:"target_key_id"`
	CreationDate    float64 `json:"creation_date"`
	LastUpdatedDate float64 `json:"last_updated_date"`
}

func keyArn(r *http.Request, keyID string) string {
	return "arn:aws:kms:" + util.Region(r) + ":" + util.AccountID(r) + ":key/" + keyID
}

func aliasArn(r *http.Request, name string) string {
	return "arn:aws:kms:" + util.Region(r) + ":" + util.AccountID(r) + ":" + name
}

func epoch(t time.Time) float64 { return float64(t.UnixNano()/int64(time.Millisecond)) / 1000 }

// parseKeyRef splits a KeyId parameter into a key ID or an alias name. KMS
// accepts a key ID, key ARN, alias name or alias ARN.
func parseKeyRef(ref string) (keyID, alias string) {
	if strings.HasPrefix(ref, "arn:") {
		parts := strings.SplitN(ref, ":", 6)
		if len(parts) == 6 {
			ref = parts[5]
			if strings.HasPrefix(ref, "key/") {
				return strings.TrimPrefix(ref, "key/"), ""
			}
		}
	}
	if strings.HasPrefix(ref, "alias/") {
		return "", ref
	}
	return ref, ""
}

// normalizeKeyID accepts either a bare key ID or a key ARN and returns the key ID.
func normalizeKeyID(ref string) string {
	id, alias := parseKeyRef(ref)
	if alias != "" {
		return alias
	}
	return id
}

func defaultKeyPolicy(accountID string) string {
	return `{
  "Version" : "2012-10-17",
  "Id" : "key-default-1",
  "Statement" : [ {
    "Sid" : "Enable IAM User Permissions",
    "Effect" : "Allow",
    "Principal" : {
      "AWS" : "arn:aws:iam::` + accountID + `:root"
    },
    "Action" : "kms:*",
    "Resource" : "*"
  } ]
}`
}

func (h *Handler) getKey(ns, keyID string) (*resource.Resource, *keyRecord, *Error) {
	res, err := h.Store.Get(keyID, "kms", "key", ns)
	if err != nil {
		return nil, nil, errNotFound("Key '" + keyID + "' does not exist")
	}
	var rec keyRecord
	if err := json.Unmarshal(res.Attributes, &rec); err != nil {
		return nil, nil, errInternal("Failed to decode key " + keyID)
	}
	// A key whose waiting period has passed is gone, along with its aliases.
	if rec.Metadata.KeyState == "PendingDeletion" && rec.Metadata.DeletionDate > 0 &&
		float64(time.Now().Unix()) >= rec.Metadata.DeletionDate {
		h.purgeKey(ns, keyID)
		return nil, nil, errNotFound("Key '" + keyID + "' does not exist")
	}
	return res, &rec, nil
}

func (h *Handler) purgeKey(ns, keyID string) {
	_ = h.Store.Delete(keyID, "kms", "key", ns)
	aliases, _ := h.Store.List("kms", "alias", ns)
	for _, a := range aliases {
		var ar aliasRecord
		if json.Unmarshal(a.Attributes, &ar) == nil && ar.TargetKeyID == keyID {
			_ = h.Store.Delete(a.ID, "kms", "alias", ns)
		}
	}
}

func (h *Handler) saveKey(res *resource.Resource, rec *keyRecord) *Error {
	buf, _ := json.Marshal(rec)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		return errInternal("Failed to update key: " + err.Error())
	}
	return nil
}

func (h *Handler) getAlias(ns, name string) (*resource.Resource, *aliasRecord, *Error) {
	res, err := h.Store.Get(name, "kms", "alias", ns)
	if err != nil {
		return nil, nil, errNotFound("Alias " + name + " is not found.")
	}
	var ar aliasRecord
	if err := json.Unmarshal(res.Attributes, &ar); err != nil {
		return nil, nil, errInternal("Failed to decode alias " + name)
	}
	return res, &ar, nil
}

// resolveKey loads the key a KeyId parameter names. Aliases are followed only
// where KMS accepts them (allowAlias); elsewhere an alias is a NotFoundException.
// The AWS managed aliases (alias/aws/<service>) are created on first use, as
// they are in AWS.
func (h *Handler) resolveKey(r *http.Request, ref string, allowAlias bool) (*resource.Resource, *keyRecord, *Error) {
	if ref == "" {
		return nil, nil, errValidation("1 validation error detected: Value null at 'keyId' failed to satisfy constraint: Member must not be null")
	}
	ns := util.NamespaceFromHeader(r)
	keyID, alias := parseKeyRef(ref)
	if alias == "" {
		return h.getKey(ns, keyID)
	}
	if !allowAlias {
		return nil, nil, errNotFound("Invalid keyId " + ref)
	}
	_, ar, kerr := h.getAlias(ns, alias)
	if kerr != nil {
		if strings.HasPrefix(alias, "alias/aws/") {
			return h.createManagedKey(r, alias)
		}
		return nil, nil, errNotFound("Alias " + aliasArn(r, alias) + " is not found.")
	}
	return h.getKey(ns, ar.TargetKeyID)
}

type keyOptions struct {
	description string
	spec        string
	usage       string
	origin      string
	manager     string
	policy      string
	multiRegion bool
	tags        []Tag
}

func (h *Handler) newKey(r *http.Request, o keyOptions) (*resource.Resource, *keyRecord, *Error) {
	keyID := uuid.New().String()
	if o.multiRegion {
		keyID = "mrk-" + strings.ReplaceAll(uuid.New().String(), "-", "")
	}
	arn := keyArn(r, keyID)

	md := KeyMetadata{
		AWSAccountID:          util.AccountID(r),
		ARN:                   arn,
		CreationDate:          epoch(time.Now()),
		CustomerMasterKeySpec: o.spec,
		Description:           o.description,
		Enabled:               true,
		KeyID:                 keyID,
		KeyManager:            o.manager,
		KeySpec:               o.spec,
		KeyState:              "Enabled",
		KeyUsage:              o.usage,
		MultiRegion:           o.multiRegion,
		Origin:                o.origin,
	}
	switch {
	case o.spec == "SYMMETRIC_DEFAULT":
		md.EncryptionAlgorithms = []string{"SYMMETRIC_DEFAULT"}
	case strings.HasPrefix(o.spec, "HMAC_"):
		md.MacAlgorithms = []string{"HMAC_SHA_" + strings.TrimPrefix(o.spec, "HMAC_")}
	case strings.HasPrefix(o.spec, "RSA_") && o.usage == "ENCRYPT_DECRYPT":
		md.EncryptionAlgorithms = []string{"RSAES_OAEP_SHA_1", "RSAES_OAEP_SHA_256"}
	case strings.HasPrefix(o.spec, "RSA_"):
		md.SigningAlgorithms = []string{"RSASSA_PKCS1_V1_5_SHA_256", "RSASSA_PKCS1_V1_5_SHA_384",
			"RSASSA_PKCS1_V1_5_SHA_512", "RSASSA_PSS_SHA_256", "RSASSA_PSS_SHA_384", "RSASSA_PSS_SHA_512"}
	case strings.HasPrefix(o.spec, "ECC_"):
		md.SigningAlgorithms = []string{"ECDSA_SHA_256"}
	}
	if o.multiRegion {
		md.MultiRegionConfiguration = &MultiRegionConfiguration{
			MultiRegionKeyType: "PRIMARY",
			PrimaryKey:         MultiRegionKey{Arn: arn, Region: util.Region(r)},
			ReplicaKeys:        []MultiRegionKey{},
		}
	}

	rec := &keyRecord{Metadata: md, Policy: o.policy, Tags: o.tags}
	if rec.Policy == "" {
		rec.Policy = defaultKeyPolicy(util.AccountID(r))
	}
	// Symmetric keys get the material Encrypt/Decrypt use. It never leaves the store.
	if o.spec == "SYMMETRIC_DEFAULT" && o.origin == "AWS_KMS" {
		material, err := newKeyMaterial()
		if err != nil {
			return nil, nil, errInternal("Failed to generate key material: " + err.Error())
		}
		rec.Material = base64.StdEncoding.EncodeToString(material)
	}

	buf, _ := json.Marshal(rec)
	res := &resource.Resource{
		ID:         keyID,
		Namespace:  util.NamespaceFromHeader(r),
		Service:    "kms",
		Type:       "key",
		Attributes: buf,
	}
	if err := h.Store.Create(res); err != nil {
		return nil, nil, errInternal("Failed to create key: " + err.Error())
	}
	return res, rec, nil
}

// createManagedKey creates the AWS managed key behind alias/aws/<service>.
// AWS managed keys rotate every year and cannot be changed by the account.
func (h *Handler) createManagedKey(r *http.Request, alias string) (*resource.Resource, *keyRecord, *Error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	ns := util.NamespaceFromHeader(r)
	if _, ar, kerr := h.getAlias(ns, alias); kerr == nil {
		return h.getKey(ns, ar.TargetKeyID)
	}
	svc := strings.TrimPrefix(alias, "alias/aws/")
	res, rec, kerr := h.newKey(r, keyOptions{
		description: "Default key that protects my " + svc + " data when no other key is defined",
		spec:        "SYMMETRIC_DEFAULT",
		usage:       "ENCRYPT_DECRYPT",
		origin:      "AWS_KMS",
		manager:     "AWS",
	})
	if kerr != nil {
		return nil, nil, kerr
	}
	rec.RotationEnabled = true
	rec.RotationPeriodInDays = 365
	if kerr := h.saveKey(res, rec); kerr != nil {
		return nil, nil, kerr
	}
	now := epoch(time.Now())
	if kerr := h.putAlias(ns, alias, &aliasRecord{
		AliasName: alias, TargetKeyID: rec.Metadata.KeyID, CreationDate: now, LastUpdatedDate: now,
	}, true); kerr != nil {
		return nil, nil, kerr
	}
	return res, rec, nil
}

func (h *Handler) putAlias(ns, name string, ar *aliasRecord, create bool) *Error {
	buf, _ := json.Marshal(ar)
	res := &resource.Resource{ID: name, Namespace: ns, Service: "kms", Type: "alias", Attributes: buf}
	var err error
	if create {
		err = h.Store.Create(res)
	} else {
		err = h.Store.Update(res)
	}
	if err != nil {
		return errInternal("Failed to store alias: " + err.Error())
	}
	return nil
}

// keyMaterial returns the AES key material for a stored key. Keys created before
// key material was persisted at creation are backfilled on first use, so an
// existing store keeps working rather than failing every Encrypt.
func (h *Handler) keyMaterial(res *resource.Resource, rec *keyRecord) ([]byte, *Error) {
	if rec.Material != "" {
		return decodeMaterial(res.ID, rec.Material)
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	// Reload under the lock: another request may have backfilled or changed it
	res, rec, kerr := h.getKey(res.Namespace, res.ID)
	if kerr != nil {
		return nil, kerr
	}
	if rec.Material != "" {
		return decodeMaterial(res.ID, rec.Material)
	}
	material, err := newKeyMaterial()
	if err != nil {
		return nil, errInternal("Failed to generate key material: " + err.Error())
	}
	rec.Material = base64.StdEncoding.EncodeToString(material)
	if kerr := h.saveKey(res, rec); kerr != nil {
		return nil, kerr
	}
	return material, nil
}

func decodeMaterial(keyID, encoded string) ([]byte, *Error) {
	material, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(material) != keyMaterialBytes {
		return nil, errInternal("Corrupt key material for key: " + keyID)
	}
	return material, nil
}

// usableForCrypto rejects keys that cannot encrypt or decrypt right now.
func usableForCrypto(md *KeyMetadata) *Error {
	switch md.KeyState {
	case "Enabled":
	case "Disabled":
		return &Error{http.StatusBadRequest, "DisabledException", md.ARN + " is disabled."}
	default:
		return errInvalidState(md.ARN + " is " + stateText(md.KeyState) + ".")
	}
	if md.KeyUsage != "ENCRYPT_DECRYPT" || md.KeySpec != "SYMMETRIC_DEFAULT" {
		return &Error{http.StatusBadRequest, "InvalidKeyUsageException",
			md.ARN + " has KeyUsage " + md.KeyUsage + " and KeySpec " + md.KeySpec +
				", which cannot be used for this operation."}
	}
	return nil
}

// stateText is how KMS words a key state in error messages ("is pending deletion").
func stateText(state string) string {
	switch state {
	case "PendingDeletion":
		return "pending deletion"
	case "PendingImport":
		return "pending import"
	}
	return strings.ToLower(state)
}

// mutable rejects changes to a key that is pending deletion or AWS managed.
func mutable(md *KeyMetadata) *Error {
	if md.KeyState == "PendingDeletion" || md.KeyState == "PendingReplicaDeletion" {
		return errInvalidState(md.ARN + " is pending deletion.")
	}
	if md.KeyManager == "AWS" {
		return &Error{http.StatusBadRequest, "AccessDeniedException",
			"You cannot change an AWS managed key: " + md.ARN}
	}
	return nil
}

// loadForCrypto resolves a key reference and its material for an encrypt-type call.
func (h *Handler) loadForCrypto(r *http.Request, ref string) (*KeyMetadata, []byte, *Error) {
	res, rec, kerr := h.resolveKey(r, ref, true)
	if kerr != nil {
		return nil, nil, kerr
	}
	if kerr := usableForCrypto(&rec.Metadata); kerr != nil {
		return nil, nil, kerr
	}
	material, kerr := h.keyMaterial(res, rec)
	if kerr != nil {
		return nil, nil, kerr
	}
	return &rec.Metadata, material, nil
}

// paginate applies Limit/Marker paging to a sorted list of n items, using
// the index as the marker.
func paginate(n int, limit int32, marker string, def int) (start, end int, next string, err error) {
	if marker != "" {
		start = -1
		if i, e := parseMarker(marker); e == nil && i <= n {
			start = i
		}
		if start < 0 {
			return 0, 0, "", errors.New("invalid marker")
		}
	}
	size := def
	if limit > 0 {
		size = int(limit)
	}
	end = start + size
	if end >= n {
		return start, n, "", nil
	}
	return start, end, encodeMarker(end), nil
}

func encodeMarker(i int) string {
	return base64.RawURLEncoding.EncodeToString([]byte{byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i)})
}

func parseMarker(m string) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(m)
	if err != nil || len(b) != 4 {
		return 0, errors.New("invalid marker")
	}
	return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]), nil
}

func sortedTags(tags []Tag) []Tag {
	out := append([]Tag(nil), tags...)
	sort.Slice(out, func(i, j int) bool { return out[i].TagKey < out[j].TagKey })
	return out
}
