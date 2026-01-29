// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package secretsmanager

import (
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
	APIVersion     = "2017-10-17"
	secretsRegion  = "us-east-1"
	secretsAccount = "000000000000"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// Build SecretsManager ARN
func secretArn(secretName string) string {
	// AWS SecretsManager ARN format: arn:aws:secretsmanager:region:account:secret:name-6RandomChars
	// For simplicity, we'll use a deterministic approach based on the name
	suffix := util.RandomHex(3)
	return "arn:aws:secretsmanager:" + secretsRegion + ":" + secretsAccount + ":secret:" + secretName + "-" + suffix
}

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
	case "GetSecretValue":
		h.GetSecretValue(w, r)
	case "PutSecretValue":
		h.PutSecretValue(w, r)
	case "ListSecrets":
		h.ListSecrets(w, r)
	case "DeleteSecret":
		h.DeleteSecret(w, r)
	case "GetResourcePolicy":
		h.GetResourcePolicy(w, r)
	default:
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Unknown operation: " + action,
		})
	}
}

// extractSecretName extracts secret name from ARN or returns name as-is
func extractSecretName(arnOrName string) string {
	if strings.HasPrefix(arnOrName, "arn:aws:secretsmanager:") {
		// ARN format: arn:aws:secretsmanager:region:account:secret:name-suffix
		parts := strings.Split(arnOrName, ":")
		if len(parts) >= 7 {
			secretPart := parts[6] // secret:name-suffix
			secretParts := strings.Split(secretPart, "-")
			if len(secretParts) > 1 {
				// Remove the random suffix (last part)
				return strings.Join(secretParts[:len(secretParts)-1], "-")
			}
			return secretPart
		}
	}
	return arnOrName
}

// CreateSecret creates a new secret
func (h *Handler) CreateSecret(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req CreateSecretInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.Name == "" {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Name is required",
		})
		return
	}

	// Check if secret already exists
	_, err := h.Store.Get(req.Name, "secretsmanager", "secret", ns)
	if err == nil {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceExistsException",
			"message": "Secret already exists: " + req.Name,
		})
		return
	}

	now := time.Now().UTC()
	createdDate := float64(now.Unix())
	arn := secretArn(req.Name)
	versionId := uuid.New().String()

	// Store secret metadata
	secretMetadata := map[string]any{
		"arn":          arn,
		"name":         req.Name,
		"description":  req.Description,
		"kms_key_id":   req.KmsKeyId,
		"created_date": createdDate,
		"tags":         req.Tags,
	}

	// Store resource policy if provided (not in CreateSecretInput, but may be set via PutResourcePolicy)
	// For now, initialize empty policy
	secretMetadata["resource_policy"] = ""

	// Store secret value if provided
	if req.SecretString != "" || req.SecretBinary != "" {
		versionEntry := map[string]any{
			"version_id":     versionId,
			"secret_string":  req.SecretString,
			"secret_binary":  req.SecretBinary,
			"created_date":   createdDate,
			"version_stages": []string{"AWSCURRENT"},
		}
		secretMetadata["current_version"] = versionEntry
		secretMetadata["versions"] = map[string]any{
			versionId: versionEntry,
		}
		secretMetadata["version_ids_to_stages"] = map[string][]string{
			versionId: {"AWSCURRENT"},
		}
	}

	buf, _ := json.Marshal(secretMetadata)
	res := &resource.Resource{
		ID:         req.Name,
		Namespace:  ns,
		Service:    "secretsmanager",
		Type:       "secret",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to create secret: " + err.Error(),
		})
		return
	}

	output := CreateSecretOutput{
		ARN:       arn,
		Name:      req.Name,
		VersionId: versionId,
	}

	writeSecretsJSON(w, http.StatusOK, output)
}

// DescribeSecret describes an existing secret
func (h *Handler) DescribeSecret(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req DescribeSecretInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.SecretId == "" {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "SecretId is required",
		})
		return
	}

	secretName := extractSecretName(req.SecretId)

	// Get secret from store
	res, err := h.Store.Get(secretName, "secretsmanager", "secret", ns)
	if err != nil {
		writeSecretsJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Secret not found: " + secretName,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode secret metadata",
		})
		return
	}

	output := DescribeSecretOutput{
		ARN:         entry["arn"].(string),
		Name:        entry["name"].(string),
		Description: getString(entry, "description"),
		KmsKeyId:    getString(entry, "kms_key_id"),
		CreatedDate: entry["created_date"].(float64),
	}

	if tags, ok := entry["tags"].([]Tag); ok {
		output.Tags = tags
	}

	if versionIdsToStages, ok := entry["version_ids_to_stages"].(map[string][]string); ok {
		output.VersionIdsToStages = versionIdsToStages
	}

	writeSecretsJSON(w, http.StatusOK, output)
}

// GetSecretValue retrieves the secret value
func (h *Handler) GetSecretValue(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req GetSecretValueInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.SecretId == "" {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "SecretId is required",
		})
		return
	}

	secretName := extractSecretName(req.SecretId)

	// Get secret from store
	res, err := h.Store.Get(secretName, "secretsmanager", "secret", ns)
	if err != nil {
		writeSecretsJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Secret not found: " + secretName,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode secret metadata",
		})
		return
	}

	// Get current version or specified version
	var versionEntry map[string]any
	if req.VersionId != "" {
		// Look for specific version
		versions, ok := entry["versions"].(map[string]any)
		if !ok {
			writeSecretsJSON(w, http.StatusNotFound, map[string]any{
				"__type":  "ResourceNotFoundException",
				"message": "Version not found: " + req.VersionId,
			})
			return
		}
		versionData, ok := versions[req.VersionId]
		if !ok {
			writeSecretsJSON(w, http.StatusNotFound, map[string]any{
				"__type":  "ResourceNotFoundException",
				"message": "Version not found: " + req.VersionId,
			})
			return
		}
		versionEntry = versionData.(map[string]any)
	} else {
		// Get current version
		currentVersion, ok := entry["current_version"].(map[string]any)
		if !ok {
			writeSecretsJSON(w, http.StatusNotFound, map[string]any{
				"__type":  "ResourceNotFoundException",
				"message": "No version found for secret",
			})
			return
		}
		versionEntry = currentVersion
	}

	output := GetSecretValueOutput{
		ARN:          entry["arn"].(string),
		Name:         entry["name"].(string),
		VersionId:    versionEntry["version_id"].(string),
		SecretString: getString(versionEntry, "secret_string"),
		SecretBinary: getString(versionEntry, "secret_binary"),
		CreatedDate:  versionEntry["created_date"].(float64),
	}

	if stages, ok := versionEntry["version_stages"].([]string); ok {
		output.VersionStages = stages
	} else if stages, ok := versionEntry["version_stages"].([]interface{}); ok {
		output.VersionStages = make([]string, len(stages))
		for i, s := range stages {
			output.VersionStages[i] = s.(string)
		}
	}

	writeSecretsJSON(w, http.StatusOK, output)
}

// PutSecretValue creates a new version of a secret
func (h *Handler) PutSecretValue(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req PutSecretValueInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.SecretId == "" {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "SecretId is required",
		})
		return
	}

	if req.SecretString == "" && req.SecretBinary == "" {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Either SecretString or SecretBinary must be provided",
		})
		return
	}

	secretName := extractSecretName(req.SecretId)

	// Get secret from store
	res, err := h.Store.Get(secretName, "secretsmanager", "secret", ns)
	if err != nil {
		writeSecretsJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Secret not found: " + secretName,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode secret metadata",
		})
		return
	}

	now := time.Now().UTC()
	versionId := uuid.New().String()
	versionStages := req.VersionStages
	if len(versionStages) == 0 {
		versionStages = []string{"AWSCURRENT"}
	}

	// Create new version entry
	versionEntry := map[string]any{
		"version_id":     versionId,
		"secret_string":  req.SecretString,
		"secret_binary":  req.SecretBinary,
		"created_date":   float64(now.Unix()),
		"version_stages": versionStages,
	}

	// Update versions map
	versions, ok := entry["versions"].(map[string]any)
	if !ok {
		versions = make(map[string]any)
		entry["versions"] = versions
	}
	versions[versionId] = versionEntry

	// Update current version
	entry["current_version"] = versionEntry

	// Update version_ids_to_stages
	versionIdsToStages, ok := entry["version_ids_to_stages"].(map[string][]string)
	if !ok {
		versionIdsToStages = make(map[string][]string)
		entry["version_ids_to_stages"] = versionIdsToStages
	}
	versionIdsToStages[versionId] = versionStages

	// Update last changed date
	entry["last_changed_date"] = float64(now.Unix())

	// Save updated secret
	buf, _ := json.Marshal(entry)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to update secret: " + err.Error(),
		})
		return
	}

	output := PutSecretValueOutput{
		ARN:           entry["arn"].(string),
		Name:          entry["name"].(string),
		VersionId:     versionId,
		VersionStages: versionStages,
	}

	writeSecretsJSON(w, http.StatusOK, output)
}

// ListSecrets lists all secrets
func (h *Handler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	secrets, err := h.Store.List("secretsmanager", "secret", ns)
	if err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to list secrets: " + err.Error(),
		})
		return
	}

	secretList := make([]SecretListEntry, 0, len(secrets))
	for _, secretRes := range secrets {
		var entry map[string]any
		if err := json.Unmarshal(secretRes.Attributes, &entry); err != nil {
			continue
		}

		secretEntry := SecretListEntry{
			ARN:         entry["arn"].(string),
			Name:        entry["name"].(string),
			Description: getString(entry, "description"),
			KmsKeyId:    getString(entry, "kms_key_id"),
			CreatedDate: entry["created_date"].(float64),
		}

		if tags, ok := entry["tags"].([]Tag); ok {
			secretEntry.Tags = tags
		}

		if versionIdsToStages, ok := entry["version_ids_to_stages"].(map[string][]string); ok {
			secretEntry.SecretVersionsToStages = versionIdsToStages
		}

		secretList = append(secretList, secretEntry)
	}

	writeSecretsJSON(w, http.StatusOK, ListSecretsOutput{
		SecretList: secretList,
	})
}

// DeleteSecret deletes a secret
func (h *Handler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req DeleteSecretInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.SecretId == "" {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "SecretId is required",
		})
		return
	}

	secretName := extractSecretName(req.SecretId)

	// Get secret from store to verify it exists
	res, err := h.Store.Get(secretName, "secretsmanager", "secret", ns)
	if err != nil {
		writeSecretsJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Secret not found: " + secretName,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode secret metadata",
		})
		return
	}

	// Delete the secret
	if err := h.Store.Delete(secretName, "secretsmanager", "secret", ns); err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to delete secret: " + err.Error(),
		})
		return
	}

	now := time.Now().UTC()
	output := DeleteSecretOutput{
		ARN:          entry["arn"].(string),
		Name:         entry["name"].(string),
		DeletionDate: float64(now.Unix()),
	}

	writeSecretsJSON(w, http.StatusOK, output)
}

// GetResourcePolicy retrieves the resource policy for a secret
func (h *Handler) GetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req GetResourcePolicyInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.SecretId == "" {
		writeSecretsJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "SecretId is required",
		})
		return
	}

	secretName := extractSecretName(req.SecretId)

	// Get secret from store
	res, err := h.Store.Get(secretName, "secretsmanager", "secret", ns)
	if err != nil {
		writeSecretsJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Secret not found: " + secretName,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeSecretsJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode secret metadata",
		})
		return
	}

	// Get resource policy, default to empty policy if not set
	policy := ""
	if p, ok := entry["resource_policy"].(string); ok && p != "" {
		policy = p
	}

	// If no policy is set, return default policy
	if policy == "" {
		policy = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "AWS": "arn:aws:iam::000000000000:root"
      },
      "Action": "secretsmanager:*",
      "Resource": "*"
    }
  ]
}`
	}

	output := GetResourcePolicyOutput{
		ARN:    entry["arn"].(string),
		Name:   entry["name"].(string),
		Policy: policy,
	}

	writeSecretsJSON(w, http.StatusOK, output)
}

// Helper function to safely get string from map
func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
