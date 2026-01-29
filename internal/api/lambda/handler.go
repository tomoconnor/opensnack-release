// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/util"

	"opensnack/internal/awsresponses"
)

const (
	lambdaRegion  = "us-east-1"
	lambdaAccount = "000000000000"
)

func functionArn(name string) string {
	return "arn:aws:lambda:" + lambdaRegion + ":" + lambdaAccount + ":function:" + name
}

// Helper to safely convert JSON numbers (float64) to int
func toInt(v any) int {
	if i, ok := v.(int); ok {
		return i
	}
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// Helper to safely get string values from map
func getString(attr map[string]any, key string) string {
	if v, ok := attr[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

//
// Dispatcher: matches X-Amz-Target
//

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")

	// AWS Lambda uses format: AWSLambda_20150331.OperationName
	// Also support AWSLambda.OperationName for compatibility
	switch target {
	case "AWSLambda_20150331.CreateFunction", "AWSLambda.CreateFunction", "AWSLambda.CreateFunction20150331":
		h.CreateFunction(w, r)
	case "AWSLambda_20150331.GetFunction", "AWSLambda.GetFunction", "AWSLambda.GetFunction20150331":
		h.GetFunction(w, r)
	case "AWSLambda_20150331.UpdateFunctionCode", "AWSLambda.UpdateFunctionCode", "AWSLambda.UpdateFunctionCode20150331":
		h.UpdateFunctionCode(w, r)
	case "AWSLambda_20150331.UpdateFunctionConfiguration", "AWSLambda.UpdateFunctionConfiguration", "AWSLambda.UpdateFunctionConfiguration20150331":
		h.UpdateFunctionConfiguration(w, r)
	case "AWSLambda_20150331.DeleteFunction", "AWSLambda.DeleteFunction", "AWSLambda.DeleteFunction20150331":
		h.DeleteFunction(w, r)
	case "AWSLambda_20150331.ListFunctions", "AWSLambda.ListFunctions", "AWSLambda.ListFunctions20150331":
		h.ListFunctions(w, r)
	case "AWSLambda_20150331.GetFunctionConfiguration", "AWSLambda.GetFunctionConfiguration", "AWSLambda.GetFunctionConfiguration20150331":
		h.GetFunctionConfiguration(w, r)
	case "AWSLambda_20150331.ListTags", "AWSLambda.ListTags", "AWSLambda.ListTags20150331":
		h.ListTags(w, r)
	case "AWSLambda_20150331.TagResource", "AWSLambda.TagResource", "AWSLambda.TagResource20150331":
		h.TagResource(w, r)
	case "AWSLambda_20150331.UntagResource", "AWSLambda.UntagResource", "AWSLambda.UntagResource20150331":
		h.UntagResource(w, r)
	case "AWSLambda_20150331.GetFunctionCodeSigningConfig", "AWSLambda.GetFunctionCodeSigningConfig", "AWSLambda.GetFunctionCodeSigningConfig20150331":
		h.GetFunctionCodeSigningConfig(w, r)
	case "AWSLambda_20150331.ListVersionsByFunction", "AWSLambda.ListVersionsByFunction", "AWSLambda.ListVersionsByFunction20150331":
		h.ListVersionsByFunction(w, r)

	default:
		// If target is empty, try to infer from path
		if target == "" {
			path := r.URL.Path
			if r.Method == "POST" && (path == "/lambda/2015-03-31/functions" || path == "/lambda") {
				h.CreateFunction(w, r)
				return
			}
			if r.Method == "GET" {
				h.GetFunction(w, r)
				return
			}
			if r.Method == "DELETE" {
				h.DeleteFunction(w, r)
				return
			}
		}
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "UnknownOperationException",
			"message": "Unknown Lambda operation: " + target,
		})
	}
}

//
// CreateFunction
//

func (h *Handler) CreateFunction(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		FunctionName string            `json:"FunctionName"`
		Runtime      string            `json:"Runtime"`
		Role         string            `json:"Role"`
		Handler      string            `json:"Handler"`
		Code         map[string]any    `json:"Code"`
		Description  string            `json:"Description,omitempty"`
		Timeout      int               `json:"Timeout,omitempty"`
		MemorySize   int               `json:"MemorySize,omitempty"`
		PackageType  string            `json:"PackageType,omitempty"`
		Environment  map[string]any    `json:"Environment,omitempty"`
		Tags         map[string]string `json:"Tags,omitempty"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.FunctionName == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "FunctionName is required",
		})
	}

	// Check if function already exists (idempotent)
	if existing, err := h.Store.Get(req.FunctionName, "lambda", "function", ns); err == nil {
		var attr map[string]any
		json.Unmarshal(existing.Attributes, &attr)

		packageType := getString(attr, "package_type")
		if packageType == "" {
			packageType = "Zip"
		}

		resp := map[string]any{
			"FunctionName":    req.FunctionName,
			"FunctionArn":     functionArn(req.FunctionName),
			"Runtime":         getString(attr, "runtime"),
			"Role":            getString(attr, "role"),
			"Handler":         getString(attr, "handler"),
			"CodeSize":        toInt(attr["code_size"]),
			"Description":     getString(attr, "description"),
			"Timeout":         toInt(attr["timeout"]),
			"MemorySize":      toInt(attr["memory_size"]),
			"PackageType":     packageType,
			"LastModified":    getString(attr, "last_modified"),
			"CodeSha256":      getString(attr, "code_sha256"),
			"Version":         "$LATEST",
			"State":           "Active",
			"StateReason":     "The function is ready.",
			"StateReasonCode": "OK",
			"Code": map[string]any{
				"RepositoryType": "S3",
				"Location":       "https://fake-s3-bucket.s3.amazonaws.com/fake-lambda-code.zip",
			},
		}

		if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
			resp["Environment"] = map[string]any{
				"Variables": env,
			}
		}

		awsresponses.WriteJSON(w, 200, resp)
	}

	// Extract code info
	codeSize := 0
	codeSha256 := ""
	if zipFile, ok := req.Code["ZipFile"].(string); ok {
		// Base64 encoded zip file
		codeSize = len(zipFile)
		codeSha256 = "fake-sha256-hash"
	}

	// Default values
	if req.Timeout == 0 {
		req.Timeout = 3
	}
	if req.MemorySize == 0 {
		req.MemorySize = 128
	}
	if req.PackageType == "" {
		req.PackageType = "Zip"
	}

	// Store function
	now := time.Now().UTC()
	entry := map[string]any{
		"function_name": req.FunctionName,
		"runtime":       req.Runtime,
		"role":          req.Role,
		"handler":       req.Handler,
		"description":   req.Description,
		"timeout":       req.Timeout,
		"memory_size":   req.MemorySize,
		"package_type":  req.PackageType,
		"code_size":     codeSize,
		"code_sha256":   codeSha256,
		"last_modified": now.Format(time.RFC3339),
		"created_at":    now.Format(time.RFC3339),
	}

	if req.Environment != nil && req.Environment["Variables"] != nil {
		if vars, ok := req.Environment["Variables"].(map[string]any); ok {
			entry["environment"] = vars
		}
	}

	if req.Tags != nil {
		entry["tags"] = req.Tags
	}

	buf, _ := json.Marshal(entry)

	res := &resource.Resource{
		ID:         req.FunctionName,
		Namespace:  ns,
		Service:    "lambda",
		Type:       "function",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		awsresponses.WriteJSON(w, 500, map[string]any{
			"__type":  "ServiceException",
			"message": "Failed to create function: " + err.Error(),
		})
	}

	lastModified := now.Format(time.RFC3339)
	resp := map[string]any{
		"FunctionName":    req.FunctionName,
		"FunctionArn":     functionArn(req.FunctionName),
		"Runtime":         req.Runtime,
		"Role":            req.Role,
		"Handler":         req.Handler,
		"CodeSize":        codeSize,
		"Description":     req.Description,
		"Timeout":         req.Timeout,
		"MemorySize":      req.MemorySize,
		"PackageType":     req.PackageType,
		"LastModified":    lastModified,
		"CodeSha256":      codeSha256,
		"Version":         "$LATEST",
		"State":           "Active",
		"StateReason":     "The function is ready.",
		"StateReasonCode": "OK",
		"Code": map[string]any{
			"RepositoryType": "S3",
			"Location":       "https://fake-s3-bucket.s3.amazonaws.com/fake-lambda-code.zip",
		},
	}

	if req.Environment != nil && req.Environment["Variables"] != nil {
		resp["Environment"] = req.Environment
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// GetFunction
//

func (h *Handler) GetFunction(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		FunctionName string `json:"FunctionName"`
	}

	// Try to get FunctionName from JSON body first
	if r.Body != nil && r.ContentLength > 0 {
		if err := util.DecodeAWSJSON(r, &req); err != nil {
			awsresponses.WriteJSON(w, 400, map[string]any{
				"__type":  "InvalidRequestContentException",
				"message": "Invalid request body: " + err.Error(),
			})
		}
	}

	// If FunctionName is still empty, try to get it from path parameter (REST API)
	if req.FunctionName == "" {
		// Extract function name from path
		parts := strings.Split(r.URL.Path, "/")
		for i, part := range parts {
			if part == "functions" && i+1 < len(parts) {
				req.FunctionName = parts[i+1]
				break
			}
		}
	}

	// If still empty, try query parameter
	if req.FunctionName == "" {
		req.FunctionName = r.URL.Query().Get("FunctionName")
	}

	if req.FunctionName == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "FunctionName is required",
		})
	}

	res, err := h.Store.Get(req.FunctionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + req.FunctionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	packageType := getString(attr, "package_type")
	if packageType == "" {
		packageType = "Zip"
	}

	configuration := map[string]any{
		"FunctionName":    req.FunctionName,
		"FunctionArn":     functionArn(req.FunctionName),
		"Runtime":         getString(attr, "runtime"),
		"Role":            getString(attr, "role"),
		"Handler":         getString(attr, "handler"),
		"CodeSize":        toInt(attr["code_size"]),
		"Description":     getString(attr, "description"),
		"Timeout":         toInt(attr["timeout"]),
		"MemorySize":      toInt(attr["memory_size"]),
		"PackageType":     packageType,
		"LastModified":    getString(attr, "last_modified"),
		"CodeSha256":      getString(attr, "code_sha256"),
		"Version":         "$LATEST",
		"State":           "Active",
		"StateReason":     "The function is ready.",
		"StateReasonCode": "OK",
	}

	if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
		configuration["Environment"] = map[string]any{
			"Variables": env,
		}
	}

	resp := map[string]any{
		"Configuration": configuration,
		"Code": map[string]any{
			"RepositoryType": "S3",
			"Location":       "https://fake-s3-bucket.s3.amazonaws.com/fake-lambda-code.zip",
		},
		"Tags": map[string]string{},
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// DeleteFunction
//

func (h *Handler) DeleteFunction(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		FunctionName string `json:"FunctionName"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.FunctionName == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "FunctionName is required",
		})
	}

	_ = h.Store.Delete(req.FunctionName, "lambda", "function", ns)

	w.WriteHeader(204)
}

//
// ListFunctions
//

func (h *Handler) ListFunctions(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	items, err := h.Store.List("lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 500, map[string]any{
			"__type":  "ServiceException",
			"message": "Failed to list functions: " + err.Error(),
		})
	}

	functions := make([]map[string]any, 0)
	for _, item := range items {
		var attr map[string]any
		json.Unmarshal(item.Attributes, &attr)

		packageType := getString(attr, "package_type")
		if packageType == "" {
			packageType = "Zip"
		}

		fn := map[string]any{
			"FunctionName":    item.ID,
			"FunctionArn":     functionArn(item.ID),
			"Runtime":         getString(attr, "runtime"),
			"Role":            getString(attr, "role"),
			"Handler":         getString(attr, "handler"),
			"CodeSize":        toInt(attr["code_size"]),
			"Description":     getString(attr, "description"),
			"Timeout":         toInt(attr["timeout"]),
			"MemorySize":      toInt(attr["memory_size"]),
			"PackageType":     packageType,
			"LastModified":    getString(attr, "last_modified"),
			"CodeSha256":      getString(attr, "code_sha256"),
			"Version":         "$LATEST",
			"State":           "Active",
			"StateReason":     "The function is ready.",
			"StateReasonCode": "OK",
		}

		if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
			fn["Environment"] = map[string]any{
				"Variables": env,
			}
		}

		functions = append(functions, fn)
	}

	resp := map[string]any{
		"Functions": functions,
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// UpdateFunctionCode
//

func (h *Handler) UpdateFunctionCode(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		FunctionName string         `json:"FunctionName"`
		ZipFile      string         `json:"ZipFile,omitempty"`
		Code         map[string]any `json:"Code,omitempty"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.FunctionName == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "FunctionName is required",
		})
	}

	res, err := h.Store.Get(req.FunctionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + req.FunctionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	// Update code info
	codeSize := 0
	codeSha256 := "fake-sha256-hash"
	if req.ZipFile != "" {
		codeSize = len(req.ZipFile)
	} else if req.Code != nil {
		if zipFile, ok := req.Code["ZipFile"].(string); ok {
			codeSize = len(zipFile)
		}
	}

	attr["code_size"] = codeSize
	attr["code_sha256"] = codeSha256
	attr["last_modified"] = time.Now().UTC().Format(time.RFC3339)

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		awsresponses.WriteJSON(w, 500, map[string]any{
			"__type":  "ServiceException",
			"message": "Failed to update function: " + err.Error(),
		})
	}

	packageType := getString(attr, "package_type")
	if packageType == "" {
		packageType = "Zip"
	}

	resp := map[string]any{
		"FunctionName":    req.FunctionName,
		"FunctionArn":     functionArn(req.FunctionName),
		"Runtime":         getString(attr, "runtime"),
		"Role":            getString(attr, "role"),
		"Handler":         getString(attr, "handler"),
		"CodeSize":        codeSize,
		"Description":     getString(attr, "description"),
		"Timeout":         toInt(attr["timeout"]),
		"MemorySize":      toInt(attr["memory_size"]),
		"PackageType":     packageType,
		"LastModified":    getString(attr, "last_modified"),
		"CodeSha256":      codeSha256,
		"Version":         "$LATEST",
		"State":           "Active",
		"StateReason":     "The function is ready.",
		"StateReasonCode": "OK",
	}

	if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
		resp["Environment"] = map[string]any{
			"Variables": env,
		}
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// UpdateFunctionConfiguration
//

func (h *Handler) UpdateFunctionConfiguration(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		FunctionName string         `json:"FunctionName"`
		Role         string         `json:"Role,omitempty"`
		Handler      string         `json:"Handler,omitempty"`
		Description  string         `json:"Description,omitempty"`
		Timeout      int            `json:"Timeout,omitempty"`
		MemorySize   int            `json:"MemorySize,omitempty"`
		Environment  map[string]any `json:"Environment,omitempty"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.FunctionName == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "FunctionName is required",
		})
	}

	res, err := h.Store.Get(req.FunctionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + req.FunctionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	// Update only provided fields
	if req.Role != "" {
		attr["role"] = req.Role
	}
	if req.Handler != "" {
		attr["handler"] = req.Handler
	}
	if req.Description != "" {
		attr["description"] = req.Description
	}
	if req.Timeout > 0 {
		attr["timeout"] = req.Timeout
	}
	if req.MemorySize > 0 {
		attr["memory_size"] = req.MemorySize
	}
	if req.Environment != nil && req.Environment["Variables"] != nil {
		if vars, ok := req.Environment["Variables"].(map[string]any); ok {
			attr["environment"] = vars
		}
	}

	attr["last_modified"] = time.Now().UTC().Format(time.RFC3339)

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		awsresponses.WriteJSON(w, 500, map[string]any{
			"__type":  "ServiceException",
			"message": "Failed to update function: " + err.Error(),
		})
	}

	packageType := getString(attr, "package_type")
	if packageType == "" {
		packageType = "Zip"
	}

	resp := map[string]any{
		"FunctionName":    req.FunctionName,
		"FunctionArn":     functionArn(req.FunctionName),
		"Runtime":         getString(attr, "runtime"),
		"Role":            getString(attr, "role"),
		"Handler":         getString(attr, "handler"),
		"CodeSize":        toInt(attr["code_size"]),
		"Description":     getString(attr, "description"),
		"Timeout":         toInt(attr["timeout"]),
		"MemorySize":      toInt(attr["memory_size"]),
		"PackageType":     packageType,
		"LastModified":    getString(attr, "last_modified"),
		"CodeSha256":      getString(attr, "code_sha256"),
		"Version":         "$LATEST",
		"State":           "Active",
		"StateReason":     "The function is ready.",
		"StateReasonCode": "OK",
		"Code": map[string]any{
			"RepositoryType": "S3",
			"Location":       "https://fake-s3-bucket.s3.amazonaws.com/fake-lambda-code.zip",
		},
	}

	if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
		resp["Environment"] = map[string]any{
			"Variables": env,
		}
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// GetFunctionConfiguration
//

func (h *Handler) GetFunctionConfiguration(w http.ResponseWriter, r *http.Request) {
	// Same as GetFunction but returns configuration only
	h.GetFunction(w, r)
}

//
// ListTags
//

func (h *Handler) ListTags(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		Resource string `json:"Resource"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.Resource == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "Resource is required",
		})
	}

	// Extract function name from ARN
	functionName := req.Resource
	if strings.Contains(req.Resource, ":function:") {
		parts := strings.Split(req.Resource, ":function:")
		if len(parts) > 1 {
			functionName = parts[1]
		}
	}

	res, err := h.Store.Get(functionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + functionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	tags := make(map[string]string)
	if tagMap, ok := attr["tags"].(map[string]any); ok {
		for k, v := range tagMap {
			if str, ok := v.(string); ok {
				tags[k] = str
			} else {
				tags[k] = fmt.Sprintf("%v", v)
			}
		}
	}

	resp := map[string]any{
		"Tags": tags,
	}

	awsresponses.WriteJSON(w, 200, resp)
}

//
// TagResource
//

func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		Resource string            `json:"Resource"`
		Tags     map[string]string `json:"Tags"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.Resource == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "Resource is required",
		})
	}

	// Extract function name from ARN
	functionName := req.Resource
	if strings.Contains(req.Resource, ":function:") {
		parts := strings.Split(req.Resource, ":function:")
		if len(parts) > 1 {
			functionName = parts[1]
		}
	}

	res, err := h.Store.Get(functionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + functionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	// Merge tags
	existingTags, _ := attr["tags"].(map[string]any)
	if existingTags == nil {
		existingTags = make(map[string]any)
	}

	for k, v := range req.Tags {
		existingTags[k] = v
	}

	attr["tags"] = existingTags

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		awsresponses.WriteJSON(w, 500, map[string]any{
			"__type":  "ServiceException",
			"message": "Failed to update function: " + err.Error(),
		})
	}

	w.WriteHeader(200)
}

//
// UntagResource
//

func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		Resource string   `json:"Resource"`
		TagKeys  []string `json:"TagKeys"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.Resource == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "Resource is required",
		})
	}

	// Extract function name from ARN
	functionName := req.Resource
	if strings.Contains(req.Resource, ":function:") {
		parts := strings.Split(req.Resource, ":function:")
		if len(parts) > 1 {
			functionName = parts[1]
		}
	}

	res, err := h.Store.Get(functionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + functionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	// Remove tags
	existingTags, _ := attr["tags"].(map[string]any)
	if existingTags != nil {
		for _, key := range req.TagKeys {
			delete(existingTags, key)
		}
		attr["tags"] = existingTags
	}

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		awsresponses.WriteJSON(w, 500, map[string]any{
			"__type":  "ServiceException",
			"message": "Failed to update function: " + err.Error(),
		})
	}

	w.WriteHeader(200)
}

//
// REST API handlers (for GET/DELETE requests)
//

// GetFunctionREST handles GET /lambda/2015-03-31/functions/:functionName
// AWS GetFunction returns {"Configuration": {...}, "Code": {...}, "Tags": {...}}
func (h *Handler) GetFunctionREST(w http.ResponseWriter, r *http.Request) {
	// Extract function name from path
	parts := strings.Split(r.URL.Path, "/")
	var functionName string
	for i, part := range parts {
		if part == "functions" && i+1 < len(parts) {
			functionName = parts[i+1]
			break
		}
	}
	ns := util.NamespaceFromHeader(r)

	res, err := h.Store.Get(functionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + functionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	packageType := getString(attr, "package_type")
	if packageType == "" {
		packageType = "Zip"
	}

	configuration := map[string]any{
		"FunctionName":    functionName,
		"FunctionArn":     functionArn(functionName),
		"Runtime":         getString(attr, "runtime"),
		"Role":            getString(attr, "role"),
		"Handler":         getString(attr, "handler"),
		"CodeSize":        toInt(attr["code_size"]),
		"Description":     getString(attr, "description"),
		"Timeout":         toInt(attr["timeout"]),
		"MemorySize":      toInt(attr["memory_size"]),
		"PackageType":     packageType,
		"LastModified":    getString(attr, "last_modified"),
		"CodeSha256":      getString(attr, "code_sha256"),
		"Version":         "$LATEST",
		"State":           "Active",
		"StateReason":     "The function is ready.",
		"StateReasonCode": "OK",
	}

	if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
		configuration["Environment"] = map[string]any{
			"Variables": env,
		}
	}

	// AWS GetFunction response structure has Configuration and Code as separate top-level keys
	resp := map[string]any{
		"Configuration": configuration,
		"Code": map[string]any{
			"RepositoryType": "S3",
			"Location":       "https://fake-s3-bucket.s3.amazonaws.com/fake-lambda-code.zip",
		},
		"Tags": map[string]string{},
	}

	awsresponses.WriteJSON(w, 200, resp)
}

// GetFunctionConfigurationREST handles GET /lambda/2015-03-31/functions/:functionName/configuration
func (h *Handler) GetFunctionConfigurationREST(w http.ResponseWriter, r *http.Request) {
	// Same as GetFunctionREST but without Code
	// Extract function name from path
	parts := strings.Split(r.URL.Path, "/")
	var functionName string
	for i, part := range parts {
		if part == "functions" && i+1 < len(parts) {
			functionName = parts[i+1]
			break
		}
	}
	ns := util.NamespaceFromHeader(r)

	res, err := h.Store.Get(functionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + functionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	packageType := getString(attr, "package_type")
	if packageType == "" {
		packageType = "Zip"
	}

	resp := map[string]any{
		"FunctionName":    functionName,
		"FunctionArn":     functionArn(functionName),
		"Runtime":         getString(attr, "runtime"),
		"Role":            getString(attr, "role"),
		"Handler":         getString(attr, "handler"),
		"CodeSize":        toInt(attr["code_size"]),
		"Description":     getString(attr, "description"),
		"Timeout":         toInt(attr["timeout"]),
		"MemorySize":      toInt(attr["memory_size"]),
		"PackageType":     packageType,
		"LastModified":    getString(attr, "last_modified"),
		"CodeSha256":      getString(attr, "code_sha256"),
		"Version":         "$LATEST",
		"State":           "Active",
		"StateReason":     "The function is ready.",
		"StateReasonCode": "OK",
	}

	if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
		resp["Environment"] = map[string]any{
			"Variables": env,
		}
	}

	awsresponses.WriteJSON(w, 200, resp)
}

// DeleteFunctionREST handles DELETE /lambda/2015-03-31/functions/:functionName
func (h *Handler) DeleteFunctionREST(w http.ResponseWriter, r *http.Request) {
	// Extract function name from path
	parts := strings.Split(r.URL.Path, "/")
	var functionName string
	for i, part := range parts {
		if part == "functions" && i+1 < len(parts) {
			functionName = parts[i+1]
			break
		}
	}
	ns := util.NamespaceFromHeader(r)

	_ = h.Store.Delete(functionName, "lambda", "function", ns)

	w.WriteHeader(204)
}

// ListVersionsByFunctionREST handles GET /lambda/2015-03-31/functions/:functionName/versions
// Returns a list of versions for the given function. For simplicity, we only return $LATEST.
func (h *Handler) ListVersionsByFunctionREST(w http.ResponseWriter, r *http.Request) {
	// Extract function name from path
	parts := strings.Split(r.URL.Path, "/")
	var functionName string
	for i, part := range parts {
		if part == "functions" && i+1 < len(parts) {
			functionName = parts[i+1]
			break
		}
	}
	ns := util.NamespaceFromHeader(r)

	res, err := h.Store.Get(functionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + functionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	packageType := getString(attr, "package_type")
	if packageType == "" {
		packageType = "Zip"
	}

	// Return $LATEST version
	version := map[string]any{
		"FunctionName":    functionName,
		"FunctionArn":     functionArn(functionName),
		"Runtime":         getString(attr, "runtime"),
		"Role":            getString(attr, "role"),
		"Handler":         getString(attr, "handler"),
		"CodeSize":        toInt(attr["code_size"]),
		"Description":     getString(attr, "description"),
		"Timeout":         toInt(attr["timeout"]),
		"MemorySize":      toInt(attr["memory_size"]),
		"PackageType":     packageType,
		"LastModified":    getString(attr, "last_modified"),
		"CodeSha256":      getString(attr, "code_sha256"),
		"Version":         "$LATEST",
		"State":           "Active",
		"StateReason":     "The function is ready.",
		"StateReasonCode": "OK",
	}

	if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
		version["Environment"] = map[string]any{
			"Variables": env,
		}
	}

	resp := map[string]any{
		"Versions": []map[string]any{version},
	}

	awsresponses.WriteJSON(w, 200, resp)
}

// GetFunctionCodeSigningConfigREST handles GET /lambda/2015-03-31/functions/:functionName/code-signing-config
// Returns 200 with empty CodeSigningConfig when no config is attached (this is normal for functions without signing).
func (h *Handler) GetFunctionCodeSigningConfigREST(w http.ResponseWriter, r *http.Request) {
	// Extract function name from path
	parts := strings.Split(r.URL.Path, "/")
	var functionName string
	for i, part := range parts {
		if part == "functions" && i+1 < len(parts) {
			functionName = parts[i+1]
			break
		}
	}
	ns := util.NamespaceFromHeader(r)

	// First check if function exists
	_, err := h.Store.Get(functionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + functionName,
		})
	}

	// Return 200 with empty CodeSigningConfig - no code signing config attached
	awsresponses.WriteJSON(w, 200, map[string]any{
		"CodeSigningConfig": nil,
	})
}

//
// GetFunctionCodeSigningConfig (JSON API)
//

func (h *Handler) GetFunctionCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		FunctionName string `json:"FunctionName"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.FunctionName == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "FunctionName is required",
		})
	}

	// First check if function exists
	_, err := h.Store.Get(req.FunctionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + req.FunctionName,
		})
	}

	// Return 200 with empty CodeSigningConfig - no code signing config attached
	awsresponses.WriteJSON(w, 200, map[string]any{
		"CodeSigningConfig": nil,
	})
}

//
// ListVersionsByFunction (JSON API)
//

func (h *Handler) ListVersionsByFunction(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		FunctionName string `json:"FunctionName"`
		MaxItems     int    `json:"MaxItems,omitempty"`
		Marker       string `json:"Marker,omitempty"`
	}

	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidRequestContentException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.FunctionName == "" {
		awsresponses.WriteJSON(w, 400, map[string]any{
			"__type":  "InvalidParameterValueException",
			"message": "FunctionName is required",
		})
	}

	res, err := h.Store.Get(req.FunctionName, "lambda", "function", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 404, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Function not found: " + req.FunctionName,
		})
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	packageType := getString(attr, "package_type")
	if packageType == "" {
		packageType = "Zip"
	}

	// Return $LATEST version
	version := map[string]any{
		"FunctionName":    req.FunctionName,
		"FunctionArn":     functionArn(req.FunctionName),
		"Runtime":         getString(attr, "runtime"),
		"Role":            getString(attr, "role"),
		"Handler":         getString(attr, "handler"),
		"CodeSize":        toInt(attr["code_size"]),
		"Description":     getString(attr, "description"),
		"Timeout":         toInt(attr["timeout"]),
		"MemorySize":      toInt(attr["memory_size"]),
		"PackageType":     packageType,
		"LastModified":    getString(attr, "last_modified"),
		"CodeSha256":      getString(attr, "code_sha256"),
		"Version":         "$LATEST",
		"State":           "Active",
		"StateReason":     "The function is ready.",
		"StateReasonCode": "OK",
	}

	if env, ok := attr["environment"].(map[string]any); ok && len(env) > 0 {
		version["Environment"] = map[string]any{
			"Variables": env,
		}
	}

	resp := map[string]any{
		"Versions": []map[string]any{version},
	}

	awsresponses.WriteJSON(w, 200, resp)
}
