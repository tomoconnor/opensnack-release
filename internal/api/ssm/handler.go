// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ssm

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"opensnack/internal/api/kms"
	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	APIVersion = "2014-11-06"

	defaultKeyID = "alias/aws/ssm"

	// maxVersions is how many versions of a parameter SSM keeps
	maxVersions = 100
)

type Handler struct {
	Store resource.Store
	// KMS encrypts SecureString values, in the caller's tenant and region.
	KMS *kms.Handler
	// mu serialises read-modify-write of parameters; the store has no
	// compare-and-swap.
	mu sync.Mutex
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store, KMS: kms.NewHandler(store)}
}

// ssmError is an SSM error response
type ssmError struct {
	errType string
	message string
}

func errValidation(msg string) *ssmError { return &ssmError{"ValidationException", msg} }

func errParamNotFound(name string) *ssmError {
	return &ssmError{"ParameterNotFound", "Parameter " + name + " not found."}
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

func writeError(w http.ResponseWriter, e *ssmError) {
	status := http.StatusBadRequest
	if e.errType == "InternalServerError" {
		status = http.StatusInternalServerError
	}
	writeSSMJSON(w, status, map[string]any{"__type": e.errType, "message": e.message})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := util.DecodeAWSJSON(r, v); err != nil {
		writeError(w, errValidation("Invalid request body: "+err.Error()))
		return false
	}
	return true
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
	case "GetParametersByPath":
		h.GetParametersByPath(w, r)
	case "DescribeParameters":
		h.DescribeParameters(w, r)
	case "GetParameterHistory":
		h.GetParameterHistory(w, r)
	case "LabelParameterVersion":
		h.LabelParameterVersion(w, r)
	case "UnlabelParameterVersion":
		h.UnlabelParameterVersion(w, r)
	case "DeleteParameter":
		h.DeleteParameter(w, r)
	case "DeleteParameters":
		h.DeleteParameters(w, r)
	case "AddTagsToResource":
		h.AddTagsToResource(w, r)
	case "RemoveTagsFromResource":
		h.RemoveTagsFromResource(w, r)
	case "ListTagsForResource":
		h.ListTagsForResource(w, r)
	default:
		writeSSMJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "InvalidAction",
			"message": "Unknown operation: " + action,
		})
	}
}
