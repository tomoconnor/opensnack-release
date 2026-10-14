// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	APIVersion = "2014-11-01"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// writeKMSJSON writes JSON response with KMS-specific Content-Type
func writeKMSJSON(w http.ResponseWriter, status int, v interface{}) error {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("Server", "AmazonEC2")
	w.Header().Set("X-Amz-Request-Id", awsresponses.NextRequestID())
	w.Header().Set("X-Amz-Id-2", "opensnackfakeid")
	w.Header().Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	w.Header().Set("Connection", "close")

	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(v)
}

func writeKMSError(w http.ResponseWriter, e *Error) {
	writeKMSJSON(w, e.Status, map[string]any{
		"__type":  e.Type,
		"message": e.Message,
	})
}

// decode reads the request body, writing the error itself when it fails.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := util.DecodeAWSJSON(r, v); err != nil {
		writeKMSError(w, &Error{http.StatusBadRequest, "SerializationException", "Invalid request body: " + err.Error()})
		return false
	}
	return true
}

// Dispatch handles KMS JSON API requests
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")

	if target == "" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingAuthenticationTokenException",
			"message": "Missing X-Amz-Target header",
		})
		return
	}

	// KMS uses TrentService prefix
	if !strings.HasPrefix(target, "TrentService.") {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Invalid action: " + target,
		})
		return
	}

	action := strings.TrimPrefix(target, "TrentService.")
	switch action {
	case "CreateKey":
		h.CreateKey(w, r)
	case "DescribeKey":
		h.DescribeKey(w, r)
	case "ListKeys":
		h.ListKeys(w, r)
	case "UpdateKeyDescription":
		h.UpdateKeyDescription(w, r)
	case "EnableKey":
		h.setEnabled(w, r, true)
	case "DisableKey":
		h.setEnabled(w, r, false)
	case "ScheduleKeyDeletion":
		h.ScheduleKeyDeletion(w, r)
	case "CancelKeyDeletion":
		h.CancelKeyDeletion(w, r)
	case "GetKeyPolicy":
		h.GetKeyPolicy(w, r)
	case "PutKeyPolicy":
		h.PutKeyPolicy(w, r)
	case "ListKeyPolicies":
		h.ListKeyPolicies(w, r)
	case "EnableKeyRotation":
		h.EnableKeyRotation(w, r)
	case "DisableKeyRotation":
		h.DisableKeyRotation(w, r)
	case "GetKeyRotationStatus":
		h.GetKeyRotationStatus(w, r)
	case "TagResource":
		h.TagResource(w, r)
	case "UntagResource":
		h.UntagResource(w, r)
	case "ListResourceTags":
		h.ListResourceTags(w, r)
	case "CreateAlias":
		h.CreateAlias(w, r)
	case "UpdateAlias":
		h.UpdateAlias(w, r)
	case "DeleteAlias":
		h.DeleteAlias(w, r)
	case "ListAliases":
		h.ListAliases(w, r)
	case "Encrypt":
		h.Encrypt(w, r)
	case "Decrypt":
		h.Decrypt(w, r)
	case "GenerateDataKey":
		h.GenerateDataKey(w, r, true)
	case "GenerateDataKeyWithoutPlaintext":
		h.GenerateDataKey(w, r, false)
	default:
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Unknown operation: " + action,
		})
	}
}

// CreateKey creates a new KMS key
func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	var req CreateKeyInput
	if !decode(w, r, &req) {
		return
	}

	spec := req.KeySpec
	if spec == "" {
		spec = req.CustomerMasterKeySpec
	}
	if spec == "" {
		spec = "SYMMETRIC_DEFAULT"
	}
	usage := req.KeyUsage
	if usage == "" {
		usage = "ENCRYPT_DECRYPT"
		if strings.HasPrefix(spec, "HMAC_") {
			usage = "GENERATE_VERIFY_MAC"
		}
	}
	origin := req.Origin
	if origin == "" {
		origin = "AWS_KMS"
	}
	if req.Policy != "" && !json.Valid([]byte(req.Policy)) {
		writeKMSError(w, &Error{http.StatusBadRequest, "MalformedPolicyDocumentException", "The policy is not valid JSON."})
		return
	}
	if kerr := validateTags(req.Tags); kerr != nil {
		writeKMSError(w, kerr)
		return
	}

	_, rec, kerr := h.newKey(r, keyOptions{
		description: req.Description,
		spec:        spec,
		usage:       usage,
		origin:      origin,
		manager:     "CUSTOMER",
		policy:      req.Policy,
		multiRegion: req.MultiRegion != nil && *req.MultiRegion,
		tags:        req.Tags,
	})
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}

	writeKMSJSON(w, http.StatusOK, CreateKeyOutput{KeyMetadata: rec.Metadata})
}

// DescribeKey describes an existing KMS key
func (h *Handler) DescribeKey(w http.ResponseWriter, r *http.Request) {
	var req DescribeKeyInput
	if !decode(w, r, &req) {
		return
	}
	_, rec, kerr := h.resolveKey(r, req.KeyID, true)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	writeKMSJSON(w, http.StatusOK, DescribeKeyOutput{KeyMetadata: rec.Metadata})
}

// ListKeys lists all KMS keys
func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	var req ListKeysInput
	if !decode(w, r, &req) {
		return
	}
	ns := util.NamespaceFromHeader(r)

	keys, err := h.Store.List("kms", "key", ns)
	if err != nil {
		writeKMSError(w, errInternal("Failed to list keys: "+err.Error()))
		return
	}
	entries := make([]KeyListEntry, 0, len(keys))
	for _, k := range keys {
		var rec keyRecord
		if json.Unmarshal(k.Attributes, &rec) != nil {
			continue
		}
		entries = append(entries, KeyListEntry{KeyID: rec.Metadata.KeyID, KeyArn: rec.Metadata.ARN})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].KeyID < entries[j].KeyID })

	start, end, next, perr := paginate(len(entries), req.Limit, req.Marker, 1000)
	if perr != nil {
		writeKMSError(w, &Error{http.StatusBadRequest, "InvalidMarkerException", "Invalid marker"})
		return
	}
	writeKMSJSON(w, http.StatusOK, ListKeysOutput{Keys: entries[start:end], NextMarker: next, Truncated: next != ""})
}

// UpdateKeyDescription replaces a key's description
func (h *Handler) UpdateKeyDescription(w http.ResponseWriter, r *http.Request) {
	var req UpdateKeyDescriptionInput
	if !decode(w, r, &req) {
		return
	}
	h.mutate(w, r, req.KeyID, func(rec *keyRecord) *Error {
		rec.Metadata.Description = req.Description
		return nil
	}, struct{}{})
}

// setEnabled implements EnableKey and DisableKey
func (h *Handler) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	var req KeyIDInput
	if !decode(w, r, &req) {
		return
	}
	h.mutate(w, r, req.KeyID, func(rec *keyRecord) *Error {
		rec.Metadata.Enabled = enabled
		rec.Metadata.KeyState = "Disabled"
		if enabled {
			rec.Metadata.KeyState = "Enabled"
		}
		return nil
	}, struct{}{})
}

// mutate loads a customer managed key by key ID or ARN, applies fn, saves the
// key and writes out. Keys pending deletion and AWS managed keys are refused.
func (h *Handler) mutate(w http.ResponseWriter, r *http.Request, ref string, fn func(*keyRecord) *Error, out any) {
	writeMu.Lock()
	defer writeMu.Unlock()
	res, rec, kerr := h.resolveKey(r, ref, false)
	if kerr == nil {
		kerr = mutable(&rec.Metadata)
	}
	if kerr == nil {
		kerr = fn(rec)
	}
	if kerr == nil {
		kerr = h.saveKey(res, rec)
	}
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	writeKMSJSON(w, http.StatusOK, out)
}

// ScheduleKeyDeletion schedules deletion of a KMS key
func (h *Handler) ScheduleKeyDeletion(w http.ResponseWriter, r *http.Request) {
	var req ScheduleKeyDeletionInput
	if !decode(w, r, &req) {
		return
	}
	window := req.PendingWindowInDays
	if window == 0 {
		window = 30
	}
	if window < 7 || window > 30 {
		writeKMSError(w, errValidation("PendingWindowInDays must be between 7 and 30"))
		return
	}
	var out ScheduleKeyDeletionOutput
	h.mutate(w, r, req.KeyID, func(rec *keyRecord) *Error {
		deletion := float64(time.Now().UTC().AddDate(0, 0, int(window)).Unix())
		rec.Metadata.KeyState = "PendingDeletion"
		rec.Metadata.Enabled = false
		rec.Metadata.DeletionDate = deletion
		rec.Metadata.PendingDeletionWindowInDays = window
		out = ScheduleKeyDeletionOutput{
			KeyID:               rec.Metadata.ARN,
			DeletionDate:        deletion,
			KeyState:            "PendingDeletion",
			PendingWindowInDays: window,
		}
		return nil
	}, &out)
}

// CancelKeyDeletion takes a key out of PendingDeletion. Like AWS, the key
// comes back disabled.
func (h *Handler) CancelKeyDeletion(w http.ResponseWriter, r *http.Request) {
	var req KeyIDInput
	if !decode(w, r, &req) {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	res, rec, kerr := h.resolveKey(r, req.KeyID, false)
	if kerr == nil && rec.Metadata.KeyState != "PendingDeletion" {
		kerr = errInvalidState(rec.Metadata.ARN + " is " + stateText(rec.Metadata.KeyState) + ".")
	}
	if kerr == nil {
		rec.Metadata.KeyState = "Disabled"
		rec.Metadata.Enabled = false
		rec.Metadata.DeletionDate = 0
		rec.Metadata.PendingDeletionWindowInDays = 0
		kerr = h.saveKey(res, rec)
	}
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	writeKMSJSON(w, http.StatusOK, CancelKeyDeletionOutput{KeyID: rec.Metadata.ARN})
}

func checkPolicyName(name string) *Error {
	if name != "" && name != "default" {
		return errNotFound("No such policy exists: " + name)
	}
	return nil
}

// GetKeyPolicy returns the key policy exactly as it was stored
func (h *Handler) GetKeyPolicy(w http.ResponseWriter, r *http.Request) {
	var req GetKeyPolicyInput
	if !decode(w, r, &req) {
		return
	}
	_, rec, kerr := h.resolveKey(r, req.KeyID, false)
	if kerr == nil {
		kerr = checkPolicyName(req.PolicyName)
	}
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	policy := rec.Policy
	if policy == "" {
		policy = defaultKeyPolicy(rec.Metadata.AWSAccountID)
	}
	writeKMSJSON(w, http.StatusOK, GetKeyPolicyOutput{Policy: policy, PolicyName: "default"})
}

// PutKeyPolicy replaces the key policy
func (h *Handler) PutKeyPolicy(w http.ResponseWriter, r *http.Request) {
	var req PutKeyPolicyInput
	if !decode(w, r, &req) {
		return
	}
	if kerr := checkPolicyName(req.PolicyName); kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	if req.Policy == "" || !json.Valid([]byte(req.Policy)) {
		writeKMSError(w, &Error{http.StatusBadRequest, "MalformedPolicyDocumentException", "The policy is not valid JSON."})
		return
	}
	h.mutate(w, r, req.KeyID, func(rec *keyRecord) *Error {
		rec.Policy = req.Policy
		return nil
	}, struct{}{})
}

func (h *Handler) ListKeyPolicies(w http.ResponseWriter, r *http.Request) {
	var req KeyIDInput
	if !decode(w, r, &req) {
		return
	}
	if _, _, kerr := h.resolveKey(r, req.KeyID, false); kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	writeKMSJSON(w, http.StatusOK, ListKeyPoliciesOutput{PolicyNames: []string{"default"}})
}

func rotationSupported(md *KeyMetadata) *Error {
	if md.KeySpec != "SYMMETRIC_DEFAULT" || md.Origin != "AWS_KMS" {
		return &Error{http.StatusBadRequest, "UnsupportedOperationException",
			md.ARN + " key rotation is only supported for symmetric encryption keys with key material generated by KMS."}
	}
	if md.KeyState == "Disabled" {
		return &Error{http.StatusBadRequest, "DisabledException", md.ARN + " is disabled."}
	}
	return nil
}

// EnableKeyRotation turns on automatic rotation, every RotationPeriodInDays
// (365 by default).
func (h *Handler) EnableKeyRotation(w http.ResponseWriter, r *http.Request) {
	var req EnableKeyRotationInput
	if !decode(w, r, &req) {
		return
	}
	period := int32(365)
	if req.RotationPeriodInDays != nil {
		period = *req.RotationPeriodInDays
	}
	if period < 90 || period > 2560 {
		writeKMSError(w, errValidation("RotationPeriodInDays must be between 90 and 2560"))
		return
	}
	h.mutate(w, r, req.KeyID, func(rec *keyRecord) *Error {
		if kerr := rotationSupported(&rec.Metadata); kerr != nil {
			return kerr
		}
		rec.RotationEnabled = true
		rec.RotationPeriodInDays = period
		rec.NextRotationDate = float64(time.Now().UTC().AddDate(0, 0, int(period)).Unix())
		return nil
	}, struct{}{})
}

func (h *Handler) DisableKeyRotation(w http.ResponseWriter, r *http.Request) {
	var req KeyIDInput
	if !decode(w, r, &req) {
		return
	}
	h.mutate(w, r, req.KeyID, func(rec *keyRecord) *Error {
		if kerr := rotationSupported(&rec.Metadata); kerr != nil {
			return kerr
		}
		rec.RotationEnabled = false
		rec.RotationPeriodInDays = 0
		rec.NextRotationDate = 0
		return nil
	}, struct{}{})
}

// GetKeyRotationStatus reports rotation. The period and next rotation date
// are only present while rotation is enabled.
func (h *Handler) GetKeyRotationStatus(w http.ResponseWriter, r *http.Request) {
	var req GetKeyRotationStatusInput
	if !decode(w, r, &req) {
		return
	}
	_, rec, kerr := h.resolveKey(r, req.KeyID, false)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	out := GetKeyRotationStatusOutput{KeyRotationEnabled: rec.RotationEnabled, KeyID: rec.Metadata.ARN}
	if rec.RotationEnabled {
		period := rec.RotationPeriodInDays
		if period == 0 {
			period = 365
		}
		out.RotationPeriodInDays = &period
		out.NextRotationDate = rec.NextRotationDate
	}
	writeKMSJSON(w, http.StatusOK, out)
}

func validateTags(tags []Tag) *Error {
	for _, t := range tags {
		if t.TagKey == "" || len(t.TagKey) > 128 || len(t.TagValue) > 256 {
			return &Error{http.StatusBadRequest, "TagException", "Invalid tag: " + t.TagKey}
		}
	}
	return nil
}

// TagResource adds or overwrites tags on a key
func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	var req TagResourceInput
	if !decode(w, r, &req) {
		return
	}
	if kerr := validateTags(req.Tags); kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	h.mutate(w, r, req.KeyID, func(rec *keyRecord) *Error {
		for _, t := range req.Tags {
			found := false
			for i := range rec.Tags {
				if rec.Tags[i].TagKey == t.TagKey {
					rec.Tags[i].TagValue = t.TagValue
					found = true
				}
			}
			if !found {
				rec.Tags = append(rec.Tags, t)
			}
		}
		return nil
	}, struct{}{})
}

// UntagResource removes tags from a key by key
func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	var req UntagResourceInput
	if !decode(w, r, &req) {
		return
	}
	h.mutate(w, r, req.KeyID, func(rec *keyRecord) *Error {
		drop := map[string]bool{}
		for _, k := range req.TagKeys {
			drop[k] = true
		}
		kept := rec.Tags[:0]
		for _, t := range rec.Tags {
			if !drop[t.TagKey] {
				kept = append(kept, t)
			}
		}
		rec.Tags = kept
		return nil
	}, struct{}{})
}

// ListResourceTags lists tags for a KMS key
func (h *Handler) ListResourceTags(w http.ResponseWriter, r *http.Request) {
	var req ListResourceTagsInput
	if !decode(w, r, &req) {
		return
	}
	_, rec, kerr := h.resolveKey(r, req.KeyID, false)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	tags := sortedTags(rec.Tags)
	start, end, next, err := paginate(len(tags), req.Limit, req.Marker, 50)
	if err != nil {
		writeKMSError(w, &Error{http.StatusBadRequest, "InvalidMarkerException", "Invalid marker"})
		return
	}
	writeKMSJSON(w, http.StatusOK, ListResourceTagsOutput{
		Tags:       append([]Tag{}, tags[start:end]...),
		Truncated:  next != "",
		NextMarker: next,
	})
}

// GenerateDataKey returns a fresh data key, in plaintext and sealed under the
// KMS key in the same blob format Encrypt produces, so Decrypt opens it.
func (h *Handler) GenerateDataKey(w http.ResponseWriter, r *http.Request, withPlaintext bool) {
	var req GenerateDataKeyInput
	if !decode(w, r, &req) {
		return
	}
	n, kerr := dataKeySize(req.KeySpec, req.NumberOfBytes)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	plaintext, blob, arn, err := h.GenerateDataKeyFor(r, req.KeyID, n, req.EncryptionContext)
	if err != nil {
		writeKMSError(w, asError(err))
		return
	}
	out := GenerateDataKeyOutput{CiphertextBlob: blob, KeyID: arn}
	if withPlaintext {
		out.Plaintext = plaintext
	}
	writeKMSJSON(w, http.StatusOK, out)
}

func dataKeySize(spec string, numberOfBytes *int32) (int, *Error) {
	switch {
	case spec != "" && numberOfBytes != nil:
		return 0, errValidation("Please specify either number of bytes or key spec.")
	case spec == "AES_256":
		return 32, nil
	case spec == "AES_128":
		return 16, nil
	case spec != "":
		return 0, errValidation("1 validation error detected: Value '" + spec + "' at 'keySpec' failed to satisfy constraint: Member must satisfy enum value set: [AES_256, AES_128]")
	case numberOfBytes == nil:
		return 0, errValidation("Please specify either number of bytes or key spec.")
	case *numberOfBytes < 1 || *numberOfBytes > 1024:
		return 0, errValidation("NumberOfBytes must be between 1 and 1024")
	}
	return int(*numberOfBytes), nil
}

// Encrypt encrypts plaintext under a KMS key
func (h *Handler) Encrypt(w http.ResponseWriter, r *http.Request) {
	var req EncryptInput
	if !decode(w, r, &req) {
		return
	}
	if req.KeyID == "" {
		writeKMSError(w, errValidation("KeyId is required"))
		return
	}
	if len(req.Plaintext) == 0 {
		writeKMSError(w, errValidation("Plaintext is required and must not be empty"))
		return
	}
	if len(req.Plaintext) > maxPlaintextBytes {
		writeKMSError(w, errValidation("Plaintext exceeds the maximum length for a symmetric key"))
		return
	}
	// Only symmetric encryption is backed by key material here
	if req.EncryptionAlgorithm != "" && req.EncryptionAlgorithm != "SYMMETRIC_DEFAULT" {
		writeKMSError(w, &Error{http.StatusBadRequest, "InvalidParameterException", "Unsupported EncryptionAlgorithm: " + req.EncryptionAlgorithm})
		return
	}

	blob, arn, err := h.EncryptFor(r, req.KeyID, req.Plaintext, req.EncryptionContext)
	if err != nil {
		writeKMSError(w, asError(err))
		return
	}
	writeKMSJSON(w, http.StatusOK, EncryptOutput{CiphertextBlob: blob, KeyID: arn, EncryptionAlgorithm: "SYMMETRIC_DEFAULT"})
}

// Decrypt decrypts a ciphertext blob produced by Encrypt or GenerateDataKey
func (h *Handler) Decrypt(w http.ResponseWriter, r *http.Request) {
	var req DecryptInput
	if !decode(w, r, &req) {
		return
	}
	if len(req.CiphertextBlob) == 0 {
		writeKMSError(w, &Error{http.StatusBadRequest, "InvalidCiphertextException", "CiphertextBlob is required and must not be empty"})
		return
	}
	if req.EncryptionAlgorithm != "" && req.EncryptionAlgorithm != "SYMMETRIC_DEFAULT" {
		writeKMSError(w, &Error{http.StatusBadRequest, "InvalidParameterException", "Unsupported EncryptionAlgorithm: " + req.EncryptionAlgorithm})
		return
	}

	// The blob names the key it was sealed under, so KeyId is optional
	if req.KeyID != "" {
		keyID, _, err := splitBlob(req.CiphertextBlob)
		if err == nil {
			_, rec, kerr := h.resolveKey(r, req.KeyID, true)
			if kerr == nil && rec.Metadata.KeyID != keyID {
				writeKMSError(w, &Error{http.StatusBadRequest, "IncorrectKeyException",
					"The key specified does not match the key the ciphertext was encrypted under"})
				return
			}
		}
	}

	plaintext, arn, err := h.DecryptFor(r, req.CiphertextBlob, req.EncryptionContext)
	if err != nil {
		writeKMSError(w, asError(err))
		return
	}
	writeKMSJSON(w, http.StatusOK, DecryptOutput{KeyID: arn, Plaintext: plaintext, EncryptionAlgorithm: "SYMMETRIC_DEFAULT"})
}
