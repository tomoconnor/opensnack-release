// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ssm

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	APIVersion = "2014-11-06"
	ssmRegion  = "us-east-1"
	ssmAccount = "000000000000"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// Build SSM Parameter ARN
func parameterArn(name string) string {
	// AWS SSM Parameter ARN format: arn:aws:ssm:region:account:parameter/name
	return "arn:aws:ssm:" + ssmRegion + ":" + ssmAccount + ":parameter" + name
}

// writeSSMJSON writes JSON response with SSM-specific Content-Type
func writeSSMJSON(w http.ResponseWriter, status int, v interface{}) error {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("Server", "AmazonEC2")
	w.Header().Set("X-Amz-Request-Id", awsresponses.NextRequestID())
	w.Header().Set("X-Amz-Id-2", "opensnackfakeid")
	w.Header().Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	w.Header().Set("Connection", "close")

	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(v)
}

// Dispatch handles SSM JSON API requests
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")

	if target == "" {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingAuthenticationTokenException",
			"message": "Missing X-Amz-Target header",
		})
		return
	}

	// SSM uses AmazonSSM prefix
	if !strings.HasPrefix(target, "AmazonSSM.") {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Invalid action: " + target,
		})
		return
	}

	action := strings.TrimPrefix(target, "AmazonSSM.")
	switch action {
	case "PutParameter":
		h.PutParameter(w, r)
	case "GetParameter":
		h.GetParameter(w, r)
	case "GetParameters":
		h.GetParameters(w, r)
	case "DescribeParameters":
		h.DescribeParameters(w, r)
	case "ListTagsForResource":
		h.ListTagsForResource(w, r)
	case "DeleteParameter":
		h.DeleteParameter(w, r)
	default:
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Unknown operation: " + action,
		})
	}
}

// PutParameter creates or updates a parameter
func (h *Handler) PutParameter(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req PutParameterInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.Name == "" {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Name is required",
		})
		return
	}

	if req.Value == "" {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Value is required",
		})
		return
	}

	// Set default type if not provided
	paramType := req.Type
	if paramType == "" {
		paramType = "String"
	}

	// Check if parameter already exists
	existing, err := h.Store.Get(req.Name, "ssm", "parameter", ns)
	var version int64 = 1
	var createdDate float64

	now := time.Now().UTC()
	lastModifiedDate := float64(now.Unix())

	if err == nil {
		// Parameter exists
		if !req.Overwrite {
			writeSSMJSON(w, http.StatusBadRequest, map[string]any{
				"__type":  "ParameterAlreadyExists",
				"message": "Parameter already exists: " + req.Name,
			})
			return
		}

		// Get existing version and created_date
		var entry map[string]any
		if err := json.Unmarshal(existing.Attributes, &entry); err == nil {
			if v, ok := entry["version"].(float64); ok {
				version = int64(v) + 1
			}
			if cd, ok := entry["created_date"].(float64); ok {
				createdDate = cd
			}
		}
	} else {
		// New parameter - created_date equals last_modified_date
		createdDate = lastModifiedDate
	}

	// Build parameter metadata
	paramMetadata := map[string]any{
		"name":              req.Name,
		"value":             req.Value,
		"type":              paramType,
		"description":       req.Description,
		"key_id":            req.KeyId,
		"version":           float64(version),
		"last_modified_date": lastModifiedDate,
		"created_date":      createdDate,
		"arn":               parameterArn(req.Name),
		"data_type":         req.DataType,
		"tags":              req.Tags,
	}

	if req.Tier != "" {
		paramMetadata["tier"] = req.Tier
	} else {
		paramMetadata["tier"] = "Standard"
	}

	buf, _ := json.Marshal(paramMetadata)
	res := &resource.Resource{
		ID:         req.Name,
		Namespace:  ns,
		Service:    "ssm",
		Type:       "parameter",
		Attributes: buf,
	}

	if err == nil {
		// Update existing parameter
		if err := h.Store.Update(res); err != nil {
			writeSSMJSON(w, http.StatusInternalServerError, map[string]any{
				"__type":  "InternalFailure",
				"message": "Failed to update parameter: " + err.Error(),
			})
			return
		}
	} else {
		// Create new parameter
		if err := h.Store.Create(res); err != nil {
			writeSSMJSON(w, http.StatusInternalServerError, map[string]any{
				"__type":  "InternalFailure",
				"message": "Failed to create parameter: " + err.Error(),
			})
			return
		}
	}

	output := PutParameterOutput{
		Version: version,
		Tier:    paramMetadata["tier"].(string),
	}

	writeSSMJSON(w, http.StatusOK, output)
}

// GetParameter retrieves a parameter
func (h *Handler) GetParameter(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req GetParameterInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.Name == "" {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Name is required",
		})
		return
	}

	// Get parameter from store
	res, err := h.Store.Get(req.Name, "ssm", "parameter", ns)
	if err != nil {
		writeSSMJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "ParameterNotFound",
			"message": "Parameter not found: " + req.Name,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeSSMJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode parameter metadata",
		})
		return
	}

	// Extract values with type assertions
	param := Parameter{
		Name:        entry["name"].(string),
		Type:        entry["type"].(string),
		Value:       entry["value"].(string),
		ARN:         entry["arn"].(string),
		LastModifiedDate: entry["last_modified_date"].(float64),
	}

	if v, ok := entry["version"].(float64); ok {
		param.Version = int64(v)
	}

	if dataType, ok := entry["data_type"].(string); ok && dataType != "" {
		param.DataType = dataType
	}

	output := GetParameterOutput{
		Parameter: param,
	}

	writeSSMJSON(w, http.StatusOK, output)
}

// GetParameters retrieves multiple parameters
func (h *Handler) GetParameters(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req GetParametersInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if len(req.Names) == 0 {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Names is required",
		})
		return
	}

	var parameters []Parameter
	var invalidParameters []string

	for _, name := range req.Names {
		// Get parameter from store
		res, err := h.Store.Get(name, "ssm", "parameter", ns)
		if err != nil {
			invalidParameters = append(invalidParameters, name)
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal(res.Attributes, &entry); err != nil {
			invalidParameters = append(invalidParameters, name)
			continue
		}

		// Extract values with type assertions
		param := Parameter{
			Name:        entry["name"].(string),
			Type:        entry["type"].(string),
			Value:       entry["value"].(string),
			ARN:         entry["arn"].(string),
			LastModifiedDate: entry["last_modified_date"].(float64),
		}

		if v, ok := entry["version"].(float64); ok {
			param.Version = int64(v)
		}

		if dataType, ok := entry["data_type"].(string); ok && dataType != "" {
			param.DataType = dataType
		}

		parameters = append(parameters, param)
	}

	output := GetParametersOutput{
		Parameters:        parameters,
		InvalidParameters: invalidParameters,
	}

	writeSSMJSON(w, http.StatusOK, output)
}

// DescribeParameters describes parameters (returns metadata without values)
func (h *Handler) DescribeParameters(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req DescribeParametersInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	// Get all parameters from store
	allParams, err := h.Store.List("ssm", "parameter", ns)
	if err != nil {
		writeSSMJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to list parameters: " + err.Error(),
		})
		return
	}

	var metadataList []ParameterMetadata

	for _, paramRes := range allParams {
		var entry map[string]any
		if err := json.Unmarshal(paramRes.Attributes, &entry); err != nil {
			continue
		}

		// Apply filters if provided
		if len(req.ParameterFilters) > 0 || len(req.Filters) > 0 {
			filters := req.ParameterFilters
			if len(filters) == 0 {
				filters = req.Filters
			}
			
			matched := true
			for _, filter := range filters {
				if filter.Key == "Name" {
					paramName := entry["name"].(string)
					found := false
					for _, value := range filter.Values {
						if paramName == value {
							found = true
							break
						}
					}
					if !found {
						matched = false
						break
					}
				}
			}
			if !matched {
				continue
			}
		}

		metadata := ParameterMetadata{
			Name:        entry["name"].(string),
			Type:        entry["type"].(string),
			Description: getString(entry, "description"),
			KeyId:       getString(entry, "key_id"),
			ARN:         entry["arn"].(string),
			LastModifiedDate: entry["last_modified_date"].(float64),
		}

		if v, ok := entry["version"].(float64); ok {
			metadata.Version = int64(v)
		}

		if tier, ok := entry["tier"].(string); ok {
			metadata.Tier = tier
		} else {
			metadata.Tier = "Standard"
		}

		if dataType, ok := entry["data_type"].(string); ok && dataType != "" {
			metadata.DataType = dataType
		}

		metadataList = append(metadataList, metadata)
	}

	output := DescribeParametersOutput{
		Parameters: metadataList,
	}

	writeSSMJSON(w, http.StatusOK, output)
}

// ListTagsForResource lists tags for an SSM resource
func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req ListTagsForResourceInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.ResourceId == "" {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "ResourceId is required",
		})
		return
	}

	if req.ResourceType == "" {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "ResourceType is required",
		})
		return
	}

	// Only support Parameter resource type for now
	if req.ResourceType != "Parameter" {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Unsupported resource type: " + req.ResourceType,
		})
		return
	}

	// Extract parameter name from ARN if provided
	paramName := req.ResourceId
	if strings.HasPrefix(paramName, "arn:aws:ssm:") {
		// ARN format: arn:aws:ssm:region:account:parameter/name
		parts := strings.Split(paramName, ":")
		if len(parts) >= 6 {
			paramPart := parts[5] // parameter/name
			if strings.HasPrefix(paramPart, "parameter") {
				paramName = strings.TrimPrefix(paramPart, "parameter")
				if strings.HasPrefix(paramName, "/") {
					paramName = paramName[1:]
				}
			}
		}
	}

	// Get parameter from store
	res, err := h.Store.Get(paramName, "ssm", "parameter", ns)
	if err != nil {
		writeSSMJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "InvalidResourceId",
			"message": "Parameter not found: " + paramName,
		})
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		writeSSMJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to decode parameter metadata",
		})
		return
	}

	// Extract tags from entry
	var tags []Tag
	if tagsData, ok := entry["tags"]; ok {
		if tagsSlice, ok := tagsData.([]Tag); ok {
			tags = tagsSlice
		} else if tagsSlice, ok := tagsData.([]interface{}); ok {
			// Handle case where tags are stored as []interface{}
			tags = make([]Tag, 0, len(tagsSlice))
			for _, t := range tagsSlice {
				if tagMap, ok := t.(map[string]interface{}); ok {
					tag := Tag{}
					if key, ok := tagMap["Key"].(string); ok {
						tag.Key = key
					}
					if value, ok := tagMap["Value"].(string); ok {
						tag.Value = value
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

	output := ListTagsForResourceOutput{
		TagList: tags,
	}

	writeSSMJSON(w, http.StatusOK, output)
}

// DeleteParameter deletes an SSM parameter
func (h *Handler) DeleteParameter(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req DeleteParameterInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.Name == "" {
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidParameterException",
			"message": "Name is required",
		})
		return
	}

	// Verify parameter exists
	_, err := h.Store.Get(req.Name, "ssm", "parameter", ns)
	if err != nil {
		writeSSMJSON(w, http.StatusNotFound, map[string]any{
			"__type":  "ParameterNotFound",
			"message": "Parameter not found: " + req.Name,
		})
		return
	}

	// Delete the parameter
	if err := h.Store.Delete(req.Name, "ssm", "parameter", ns); err != nil {
		writeSSMJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalFailure",
			"message": "Failed to delete parameter: " + err.Error(),
		})
		return
	}

	writeSSMJSON(w, http.StatusOK, DeleteParameterOutput{})
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

