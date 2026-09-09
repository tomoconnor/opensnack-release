// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

const (
	APIVersion = "2014-11-01"
	kmsRegion  = "us-east-1"
	kmsAccount = "000000000000"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// Build KMS Key ARN
func keyArn(keyID string) string {
	return "arn:aws:kms:" + kmsRegion + ":" + kmsAccount + ":key/" + keyID
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
	case "Encrypt":
		h.Encrypt(w, r)
	case "Decrypt":
		h.Decrypt(w, r)
	case "GetKeyPolicy":
		h.GetKeyPolicy(w, r)
	case "GetKeyRotationStatus":
		h.GetKeyRotationStatus(w, r)
	case "ListResourceTags":
		h.ListResourceTags(w, r)
	case "ScheduleKeyDeletion":
		h.ScheduleKeyDeletion(w, r)
	default:
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Unknown operation: " + action,
		})
	}
}

// CreateKey creates a new KMS key
func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req CreateKeyInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	// Set defaults
	customerMasterKeySpec := req.CustomerMasterKeySpec
	if customerMasterKeySpec == "" {
		customerMasterKeySpec = "SYMMETRIC_DEFAULT"
	}

	keyUsage := req.KeyUsage
	if keyUsage == "" {
		keyUsage = "ENCRYPT_DECRYPT"
	}

	multiRegion := false
	if req.MultiRegion != nil {
		multiRegion = *req.MultiRegion
	}

	// Generate key ID (UUID format - AWS KMS uses UUID format)
	keyIDFormatted := uuid.New().String()

	now := time.Now().UTC()
	creationDate := float64(now.Unix())

	arn := keyArn(keyIDFormatted)

	// Build key metadata
	keyMetadata := KeyMetadata{
		AWSAccountID:          kmsAccount,
		ARN:                   arn,
		CreationDate:          creationDate,
		CustomerMasterKeySpec: customerMasterKeySpec,
		Description:           req.Description,
		Enabled:               true,
		KeyID:                 keyIDFormatted,
		KeyManager:            "CUSTOMER",
		KeySpec:               customerMasterKeySpec,
		KeyState:              "Enabled",
		KeyUsage:              keyUsage,
		MultiRegion:           multiRegion,
		Origin:                "AWS_KMS",
	}

	// Set encryption algorithms for symmetric keys
	if customerMasterKeySpec == "SYMMETRIC_DEFAULT" {
		keyMetadata.EncryptionAlgorithms = []string{"SYMMETRIC_DEFAULT"}
	}

	// Generate the key material the key encrypts with. Symmetric keys never
	// leave the store, so this is only ever read back by Encrypt/Decrypt.
	material, err := newKeyMaterial()
	if err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to generate key material: " + err.Error(),
		})
		return
	}

	// Store key metadata
	entry := map[string]any{
		"key_metadata":     keyMetadata,
		"key_material":     base64.StdEncoding.EncodeToString(material),
		"created_at":       now,
		"rotation_enabled": false, // Default to false, can be enabled via EnableKeyRotation
	}

	if req.Policy != "" {
		entry["policy"] = req.Policy
	}

	if len(req.Tags) > 0 {
		entry["tags"] = req.Tags
	}

	buf, _ := json.Marshal(entry)
	res := &resource.Resource{
		ID:         keyIDFormatted,
		Namespace:  ns,
		Service:    "kms",
		Type:       "key",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to create key: " + err.Error(),
		})
		return
	}

	writeKMSJSON(w, http.StatusOK, CreateKeyOutput{
		KeyMetadata: keyMetadata,
	})
}

// DescribeKey describes an existing KMS key
func (h *Handler) DescribeKey(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req DescribeKeyInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.KeyID == "" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "KeyId is required",
		})
		return
	}

	// Extract key ID from ARN if provided
	keyID := req.KeyID
	if strings.HasPrefix(keyID, "arn:aws:kms:") {
		parts := strings.Split(keyID, "/")
		if len(parts) > 0 {
			keyID = parts[len(parts)-1]
		}
	}

	// Get key from store
	res, err := h.Store.Get(keyID, "kms", "key", ns)
	if err != nil {
		writeKMSJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "NotFoundException",
			"message": "Key not found: " + keyID,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode key metadata",
		})
		return
	}

	keyMetadataBytes, _ := json.Marshal(entry["key_metadata"])
	var keyMetadata KeyMetadata
	json.Unmarshal(keyMetadataBytes, &keyMetadata)

	writeKMSJSON(w, http.StatusOK, DescribeKeyOutput{
		KeyMetadata: keyMetadata,
	})
}

// ListKeys lists all KMS keys
func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	keys, err := h.Store.List("kms", "key", ns)
	if err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to list keys: " + err.Error(),
		})
		return
	}

	keyList := make([]KeyMetadata, 0, len(keys))
	for _, keyRes := range keys {
		var entry map[string]any
		if err := json.Unmarshal(keyRes.Attributes, &entry); err != nil {
			continue
		}

		keyMetadataBytes, _ := json.Marshal(entry["key_metadata"])
		var keyMetadata KeyMetadata
		if err := json.Unmarshal(keyMetadataBytes, &keyMetadata); err != nil {
			continue
		}

		keyList = append(keyList, keyMetadata)
	}

	writeKMSJSON(w, http.StatusOK, map[string]any{
		"Keys":      keyList,
		"Truncated": false,
	})
}

// GetKeyPolicy retrieves the key policy for a KMS key
func (h *Handler) GetKeyPolicy(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req GetKeyPolicyInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.KeyID == "" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "KeyId is required",
		})
		return
	}

	// Extract key ID from ARN if provided
	keyID := req.KeyID
	if strings.HasPrefix(keyID, "arn:aws:kms:") {
		parts := strings.Split(keyID, "/")
		if len(parts) > 0 {
			keyID = parts[len(parts)-1]
		}
	}

	// Get key from store
	res, err := h.Store.Get(keyID, "kms", "key", ns)
	if err != nil {
		writeKMSJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "NotFoundException",
			"message": "Key not found: " + keyID,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode key metadata",
		})
		return
	}

	// Get policy, default to empty policy if not set
	policy := ""
	if p, ok := entry["policy"].(string); ok {
		policy = p
	}

	// If no policy is set, return default policy
	if policy == "" {
		policy = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Enable IAM User Permissions",
      "Effect": "Allow",
      "Principal": {
        "AWS": "arn:aws:iam::000000000000:root"
      },
      "Action": "kms:*",
      "Resource": "*"
    }
  ]
}`
	}

	writeKMSJSON(w, http.StatusOK, GetKeyPolicyOutput{
		Policy: policy,
	})
}

// GetKeyRotationStatus retrieves the key rotation status for a KMS key
func (h *Handler) GetKeyRotationStatus(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req GetKeyRotationStatusInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.KeyID == "" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "KeyId is required",
		})
		return
	}

	// Extract key ID from ARN if provided
	keyID := req.KeyID
	if strings.HasPrefix(keyID, "arn:aws:kms:") {
		parts := strings.Split(keyID, "/")
		if len(parts) > 0 {
			keyID = parts[len(parts)-1]
		}
	}

	// Get key from store
	res, err := h.Store.Get(keyID, "kms", "key", ns)
	if err != nil {
		writeKMSJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "NotFoundException",
			"message": "Key not found: " + keyID,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode key metadata",
		})
		return
	}

	// Get rotation status, default to false if not set
	rotationEnabled := false
	if re, ok := entry["rotation_enabled"].(bool); ok {
		rotationEnabled = re
	}

	writeKMSJSON(w, http.StatusOK, GetKeyRotationStatusOutput{
		KeyRotationEnabled: rotationEnabled,
		KeyID:              keyID,
	})
}

// ListResourceTags lists tags for a KMS key
func (h *Handler) ListResourceTags(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req ListResourceTagsInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.KeyID == "" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "KeyId is required",
		})
		return
	}

	// Extract key ID from ARN if provided
	keyID := req.KeyID
	if strings.HasPrefix(keyID, "arn:aws:kms:") {
		parts := strings.Split(keyID, "/")
		if len(parts) > 0 {
			keyID = parts[len(parts)-1]
		}
	}

	// Get key from store
	res, err := h.Store.Get(keyID, "kms", "key", ns)
	if err != nil {
		writeKMSJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "NotFoundException",
			"message": "Key not found: " + keyID,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode key metadata",
		})
		return
	}

	// Extract tags from entry
	var tags []Tag
	if tagsData, ok := entry["tags"]; ok {
		if tagsSlice, ok := tagsData.([]Tag); ok {
			// Tags stored as []Tag
			tags = tagsSlice
		} else if tagsSlice, ok := tagsData.([]interface{}); ok {
			// Handle case where tags are stored as []interface{}
			tags = make([]Tag, 0, len(tagsSlice))
			for _, t := range tagsSlice {
				if tagMap, ok := t.(map[string]interface{}); ok {
					tag := Tag{}
					if key, ok := tagMap["TagKey"].(string); ok {
						tag.TagKey = key
					}
					if value, ok := tagMap["TagValue"].(string); ok {
						tag.TagValue = value
					}
					tags = append(tags, tag)
				}
			}
		}
	}

	// If no tags, return empty list
	if tags == nil {
		tags = []Tag{}
	}

	writeKMSJSON(w, http.StatusOK, ListResourceTagsOutput{
		Tags:      tags,
		Truncated: false,
	})
}

// ScheduleKeyDeletion schedules deletion of a KMS key
func (h *Handler) ScheduleKeyDeletion(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req ScheduleKeyDeletionInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.KeyID == "" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "KeyId is required",
		})
		return
	}

	// Extract key ID from ARN if provided
	keyID := req.KeyID
	if strings.HasPrefix(keyID, "arn:aws:kms:") {
		parts := strings.Split(keyID, "/")
		if len(parts) > 0 {
			keyID = parts[len(parts)-1]
		}
	}

	// Get key from store
	res, err := h.Store.Get(keyID, "kms", "key", ns)
	if err != nil {
		writeKMSJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "NotFoundException",
			"message": "Key not found: " + keyID,
		})
		return
	}

	// Set default pending window in days if not provided
	pendingWindowInDays := req.PendingWindowInDays
	if pendingWindowInDays == 0 {
		pendingWindowInDays = 30 // AWS default
	}

	// Calculate deletion date
	now := time.Now().UTC()
	deletionDate := now.AddDate(0, 0, int(pendingWindowInDays))

	// Update key entry with deletion date
	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode key metadata",
		})
		return
	}

	entry["deletion_date"] = float64(deletionDate.Unix())
	entry["pending_window_in_days"] = pendingWindowInDays

	// Update key metadata state to PendingDeletion
	keyMetadataBytes, _ := json.Marshal(entry["key_metadata"])
	var keyMetadata KeyMetadata
	json.Unmarshal(keyMetadataBytes, &keyMetadata)
	keyMetadata.KeyState = "PendingDeletion"
	entry["key_metadata"] = keyMetadata

	buf, _ := json.Marshal(entry)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to schedule key deletion: " + err.Error(),
		})
		return
	}

	writeKMSJSON(w, http.StatusOK, ScheduleKeyDeletionOutput{
		KeyID:               keyID,
		DeletionDate:        float64(deletionDate.Unix()),
		KeyState:            "PendingDeletion",
		PendingWindowInDays: pendingWindowInDays,
	})
}

// kmsError is a KMS error response, so the crypto helpers can report a failure
// the caller turns into the right __type without writing the response themselves.
type kmsError struct {
	status  int
	errType string
	message string
}

func writeKMSError(w http.ResponseWriter, e *kmsError) {
	writeKMSJSON(w, e.status, map[string]any{
		"__type":  e.errType,
		"message": e.message,
	})
}

// normalizeKeyID accepts either a bare key ID or a key ARN and returns the key ID.
func normalizeKeyID(keyID string) string {
	if strings.HasPrefix(keyID, "arn:aws:kms:") {
		parts := strings.Split(keyID, "/")
		return parts[len(parts)-1]
	}
	return keyID
}

// keyMaterial returns the AES key material for a stored key. Keys created before
// key material was persisted at creation are backfilled on first use, so an
// existing store keeps working rather than failing every Encrypt.
func (h *Handler) keyMaterial(res *resource.Resource, entry map[string]any) ([]byte, *kmsError) {
	if encoded, ok := entry["key_material"].(string); ok && encoded != "" {
		material, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(material) != keyMaterialBytes {
			return nil, &kmsError{http.StatusInternalServerError, "InternalFailure",
				"Corrupt key material for key: " + res.ID}
		}
		return material, nil
	}

	material, err := newKeyMaterial()
	if err != nil {
		return nil, &kmsError{http.StatusInternalServerError, "InternalFailure",
			"Failed to generate key material: " + err.Error()}
	}

	entry["key_material"] = base64.StdEncoding.EncodeToString(material)

	buf, _ := json.Marshal(entry)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		return nil, &kmsError{http.StatusInternalServerError, "InternalFailure",
			"Failed to persist key material: " + err.Error()}
	}

	return material, nil
}

// loadKeyForCrypto resolves a key ID to its metadata and key material, rejecting
// keys that are not usable for encrypt/decrypt.
func (h *Handler) loadKeyForCrypto(keyID, ns string) (*KeyMetadata, []byte, *kmsError) {
	res, err := h.Store.Get(keyID, "kms", "key", ns)
	if err != nil {
		return nil, nil, &kmsError{http.StatusNotFound, "NotFoundException",
			"Key not found: " + keyID}
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		return nil, nil, &kmsError{http.StatusInternalServerError, "InternalFailure",
			"Failed to decode key metadata"}
	}

	keyMetadataBytes, _ := json.Marshal(entry["key_metadata"])
	var keyMetadata KeyMetadata
	if err := json.Unmarshal(keyMetadataBytes, &keyMetadata); err != nil {
		return nil, nil, &kmsError{http.StatusInternalServerError, "InternalFailure",
			"Failed to decode key metadata"}
	}

	if keyMetadata.KeyState != "Enabled" {
		return nil, nil, &kmsError{http.StatusBadRequest, "KMSInvalidStateException",
			keyMetadata.ARN + " is in state " + keyMetadata.KeyState +
				", which is not valid for this operation."}
	}

	if !keyMetadata.Enabled {
		return nil, nil, &kmsError{http.StatusBadRequest, "DisabledException",
			keyMetadata.ARN + " is disabled."}
	}

	if keyMetadata.KeyUsage != "ENCRYPT_DECRYPT" {
		return nil, nil, &kmsError{http.StatusBadRequest, "InvalidKeyUsageException",
			keyMetadata.ARN + " has KeyUsage " + keyMetadata.KeyUsage +
				", which cannot be used for this operation."}
	}

	material, kerr := h.keyMaterial(res, entry)
	if kerr != nil {
		return nil, nil, kerr
	}

	return &keyMetadata, material, nil
}

// Encrypt encrypts plaintext under a KMS key
func (h *Handler) Encrypt(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req EncryptInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.KeyID == "" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "KeyId is required",
		})
		return
	}

	if len(req.Plaintext) == 0 {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "Plaintext is required and must not be empty",
		})
		return
	}

	if len(req.Plaintext) > maxPlaintextBytes {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "Plaintext exceeds the maximum length for a symmetric key",
		})
		return
	}

	// Only symmetric encryption is backed by key material here
	encryptionAlgorithm := req.EncryptionAlgorithm
	if encryptionAlgorithm == "" {
		encryptionAlgorithm = "SYMMETRIC_DEFAULT"
	}
	if encryptionAlgorithm != "SYMMETRIC_DEFAULT" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Unsupported EncryptionAlgorithm: " + encryptionAlgorithm,
		})
		return
	}

	keyMetadata, material, kerr := h.loadKeyForCrypto(normalizeKeyID(req.KeyID), ns)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}

	blob, err := encryptBlob(keyMetadata.KeyID, material, req.Plaintext, req.EncryptionContext)
	if err != nil {
		writeKMSJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to encrypt: " + err.Error(),
		})
		return
	}

	writeKMSJSON(w, http.StatusOK, EncryptOutput{
		CiphertextBlob:      blob,
		KeyID:               keyMetadata.ARN,
		EncryptionAlgorithm: encryptionAlgorithm,
	})
}

// Decrypt decrypts a ciphertext blob produced by Encrypt
func (h *Handler) Decrypt(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req DecryptInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if len(req.CiphertextBlob) == 0 {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidCiphertextException",
			"message": "CiphertextBlob is required and must not be empty",
		})
		return
	}

	encryptionAlgorithm := req.EncryptionAlgorithm
	if encryptionAlgorithm == "" {
		encryptionAlgorithm = "SYMMETRIC_DEFAULT"
	}
	if encryptionAlgorithm != "SYMMETRIC_DEFAULT" {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Unsupported EncryptionAlgorithm: " + encryptionAlgorithm,
		})
		return
	}

	// The blob names the key it was sealed under, so KeyId is optional
	keyID, body, err := splitBlob(req.CiphertextBlob)
	if err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidCiphertextException",
			"message": "The ciphertext refers to a key that does not exist or is malformed",
		})
		return
	}

	if req.KeyID != "" && normalizeKeyID(req.KeyID) != keyID {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "IncorrectKeyException",
			"message": "The key specified does not match the key the ciphertext was encrypted under",
		})
		return
	}

	keyMetadata, material, kerr := h.loadKeyForCrypto(keyID, ns)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}

	plaintext, err := decryptBlob(material, body, req.EncryptionContext)
	if err != nil {
		writeKMSJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidCiphertextException",
			"message": "The ciphertext could not be decrypted with the supplied encryption context",
		})
		return
	}

	writeKMSJSON(w, http.StatusOK, DecryptOutput{
		KeyID:               keyMetadata.ARN,
		Plaintext:           plaintext,
		EncryptionAlgorithm: encryptionAlgorithm,
	})
}
