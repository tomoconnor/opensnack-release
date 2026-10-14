// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"

	"strings"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{store}
}

// extractBucketKey extracts bucket and key from URL path
// Path format: /bucket or /bucket/key
func extractBucketKey(path string) (bucket, key string) {
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	bucket = parts[0]
	if len(parts) > 1 {
		key = parts[1]
	}
	return bucket, key
}

//
// ─── PUT BUCKET LIFECYCLE CONFIGURATION ────────────────────────────────────────
//

// PUT /:bucket?lifecycle
func (h *Handler) PutBucketLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	ns := util.GlobalNamespace(r)

	// Check bucket exists
	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusNotFound,
			"NoSuchBucket",
			"The specified bucket does not exist",
			bucket,
		)
		return
	}

	// Parse lifecycle configuration from XML body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		awsresponses.WriteS3ErrorXML(w, 400, "MalformedXML", "Failed to read request body", "")
		return
	}

	// Handle empty body - delete lifecycle configuration
	if len(bodyBytes) == 0 {
		attr := make(map[string]any)
		if len(res.Attributes) > 0 {
			json.Unmarshal(res.Attributes, &attr)
		}
		delete(attr, "lifecycle")
		buf, _ := json.Marshal(attr)
		res.Attributes = buf
		if err := h.Store.Update(res); err != nil {
			writeS3Error(w, 500, "InternalError", err.Error(), "")
			return
		}
		awsresponses.WriteEmpty200(w, nil)
		return
	}

	var lifecycleCfg struct {
		XMLName xml.Name `xml:"LifecycleConfiguration"`
		Rules   []struct {
			XMLName    xml.Name `xml:"Rule"`
			ID         string   `xml:"ID"`
			Status     string   `xml:"Status"`
			Expiration *struct {
				Days int `xml:"Days"`
			} `xml:"Expiration,omitempty"`
		} `xml:"Rule"`
	}
	if err := xml.Unmarshal(bodyBytes, &lifecycleCfg); err != nil {
		awsresponses.WriteS3ErrorXML(w, 400, "MalformedXML", "The XML you provided was not well-formed", "")
		return
	}

	// Handle empty rules - delete lifecycle configuration
	if len(lifecycleCfg.Rules) == 0 {
		attr := make(map[string]any)
		if len(res.Attributes) > 0 {
			json.Unmarshal(res.Attributes, &attr)
		}
		delete(attr, "lifecycle")
		buf, _ := json.Marshal(attr)
		res.Attributes = buf
		if err := h.Store.Update(res); err != nil {
			writeS3Error(w, 500, "InternalError", err.Error(), "")
			return
		}
		awsresponses.WriteEmpty200(w, nil)
		return
	}

	// Convert to a JSON-friendly format for storage - preserve exact values
	rulesList := make([]any, 0)
	for _, rule := range lifecycleCfg.Rules {
		ruleData := map[string]any{
			"id":     rule.ID,
			"status": rule.Status,
		}
		if rule.Expiration != nil {
			ruleData["expiration"] = map[string]any{
				"days": rule.Expiration.Days,
			}
		}
		rulesList = append(rulesList, ruleData)
	}
	lifecycleData := map[string]any{
		"rules": rulesList,
	}

	// Update bucket attributes
	attr := make(map[string]any)
	if len(res.Attributes) > 0 {
		json.Unmarshal(res.Attributes, &attr)
	}
	attr["lifecycle"] = lifecycleData

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		writeS3Error(w, 500, "InternalError", err.Error(), "")
		return
	}

	awsresponses.WriteEmpty200(w, nil)
}

//
// ─── GET BUCKET LIFECYCLE CONFIGURATION ───────────────────────────────────────
//

// GET /:bucket?lifecycle
func (h *Handler) GetBucketLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	ns := util.GlobalNamespace(r)

	// Check bucket exists
	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusNotFound,
			"NoSuchBucket",
			"The specified bucket does not exist",
			bucket,
		)
		return
	}

	attr := make(map[string]any)
	if len(res.Attributes) > 0 {
		json.Unmarshal(res.Attributes, &attr)
	}

	type LifecycleRule struct {
		XMLName    xml.Name `xml:"Rule"`
		ID         string   `xml:"ID"`
		Status     string   `xml:"Status"`
		Expiration *struct {
			Days int `xml:"Days"`
		} `xml:"Expiration,omitempty"`
	}

	lifecycle, exists := attr["lifecycle"]
	if !exists {
		// AWS returns NoSuchLifecycleConfiguration when no lifecycle config exists
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusNotFound,
			"NoSuchLifecycleConfiguration",
			"The lifecycle configuration does not exist",
			bucket,
		)
		return
	}

	// Convert stored lifecycle config back to XML
	lifecycleMap, ok := lifecycle.(map[string]any)
	if !ok {
		// Return empty if invalid format
		resp := struct {
			XMLName xml.Name        `xml:"LifecycleConfiguration"`
			Rules   []LifecycleRule `xml:"Rule"`
		}{
			Rules: []LifecycleRule{},
		}
		awsresponses.WriteXML(w, resp)
		return
	}

	rulesData, ok := lifecycleMap["rules"].([]any)
	if !ok {
		rulesData = []any{}
	}

	var rules []LifecycleRule

	for _, ruleAny := range rulesData {
		ruleMap, ok := ruleAny.(map[string]any)
		if !ok {
			continue
		}

		// Extract ID and Status as strings
		var id, status string
		if idVal, ok := ruleMap["id"]; ok {
			if idStr, ok := idVal.(string); ok {
				id = idStr
			} else {
				id = fmt.Sprintf("%v", idVal)
			}
		}
		if statusVal, ok := ruleMap["status"]; ok {
			if statusStr, ok := statusVal.(string); ok {
				status = statusStr
			} else {
				status = fmt.Sprintf("%v", statusVal)
			}
		}

		rule := LifecycleRule{
			ID:     id,
			Status: status,
		}

		if exp, ok := ruleMap["expiration"].(map[string]any); ok {
			if days, ok := exp["days"].(float64); ok {
				rule.Expiration = &struct {
					Days int `xml:"Days"`
				}{Days: int(days)}
			} else if days, ok := exp["days"].(int); ok {
				rule.Expiration = &struct {
					Days int `xml:"Days"`
				}{Days: days}
			} else if days, ok := exp["days"].(int64); ok {
				rule.Expiration = &struct {
					Days int `xml:"Days"`
				}{Days: int(days)}
			}
		}

		rules = append(rules, rule)
	}

	resp := struct {
		XMLName xml.Name        `xml:"LifecycleConfiguration"`
		Rules   []LifecycleRule `xml:"Rule"`
	}{
		Rules: rules,
	}

	awsresponses.WriteXML(w, resp)
}

//
// ─── PUT BUCKET ACL ───────────────────────────────────────────────────────────
//

// PUT /:bucket?acl
func (h *Handler) PutBucketAcl(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	ns := util.GlobalNamespace(r)

	// Check bucket exists
	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusNotFound,
			"NoSuchBucket",
			"The specified bucket does not exist",
			bucket,
		)
		return
	}

	// Parse ACL from x-amz-acl header first (simpler, used by Terraform)
	acl := r.Header.Get("x-amz-acl")
	if acl == "" {
		// Try to parse from XML body
		bodyBytes, _ := io.ReadAll(r.Body)
		if len(bodyBytes) > 0 {
			var aclCfg struct {
				XMLName           xml.Name `xml:"AccessControlPolicy"`
				AccessControlList struct {
					Grant []struct {
						Grantee struct {
							ID          string `xml:"ID"`
							DisplayName string `xml:"DisplayName"`
						} `xml:"Grantee"`
						Permission string `xml:"Permission"`
					} `xml:"Grant"`
				} `xml:"AccessControlList"`
			}
			if err := xml.Unmarshal(bodyBytes, &aclCfg); err == nil {
				// Find grant with owner or use first grant
				for _, grant := range aclCfg.AccessControlList.Grant {
					if grant.Grantee.ID == "FAKEOWNER" || grant.Grantee.ID != "" {
						acl = grant.Permission
						break
					}
				}
				if acl == "" && len(aclCfg.AccessControlList.Grant) > 0 {
					acl = aclCfg.AccessControlList.Grant[0].Permission
				}
			}
		}
	}
	if acl == "" {
		acl = "private" // Default
	}

	// Update bucket attributes
	attr := make(map[string]any)
	if len(res.Attributes) > 0 {
		json.Unmarshal(res.Attributes, &attr)
	}
	attr["acl"] = acl

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		writeS3Error(w, 500, "InternalError", err.Error(), "")
		return
	}

	awsresponses.WriteEmpty200(w, nil)
}

//
// ─── PUT BUCKET POLICY ───────────────────────────────────────────────────────────
//

// PUT /:bucket?policy
func (h *Handler) PutBucketPolicy(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	ns := util.GlobalNamespace(r)

	// Check bucket exists
	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		writeS3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist", bucket)
		return
	}

	// Read policy from body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeS3Error(w, http.StatusBadRequest, "MalformedPolicy", "Failed to read request body", bucket)
		return
	}

	// Validate JSON (policy is JSON, not XML)
	var policyJSON map[string]any
	if err := json.Unmarshal(bodyBytes, &policyJSON); err != nil {
		writeS3Error(w, http.StatusBadRequest, "MalformedPolicy", "Policies must be valid JSON and the first byte must be '{'", bucket)
		return
	}

	// Update bucket attributes
	attr := make(map[string]any)
	if len(res.Attributes) > 0 {
		json.Unmarshal(res.Attributes, &attr)
	}
	attr["policy"] = string(bodyBytes)

	buf, _ := json.Marshal(attr)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		writeS3Error(w, 500, "InternalError", err.Error(), "")
		return
	}

	awsresponses.WriteEmpty200(w, nil)
}

//
// ─── GET BUCKET POLICY ────────────────────────────────────────────────────────────
//

// GET /:bucket?policy
func (h *Handler) GetBucketPolicy(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	ns := util.GlobalNamespace(r)

	// Check bucket exists
	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusNotFound,
			"NoSuchBucket",
			"The specified bucket does not exist",
			bucket,
		)
		return
	}

	attr := make(map[string]any)
	if len(res.Attributes) > 0 {
		json.Unmarshal(res.Attributes, &attr)
	}

	policy, ok := attr["policy"].(string)
	if !ok || policy == "" {
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusNotFound,
			"NoSuchBucketPolicy",
			"The bucket policy does not exist",
			bucket,
		)
		return
	}

	// Return policy as JSON
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write([]byte(policy))
}

//
// ─── GET BUCKET ACL ────────────────────────────────────────────────────────────
//

// GET /:bucket?acl
func (h *Handler) GetBucketAcl(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	ns := util.GlobalNamespace(r)

	// Check bucket exists
	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusNotFound,
			"NoSuchBucket",
			"The specified bucket does not exist",
			bucket,
		)
		return
	}

	attr := make(map[string]any)
	if len(res.Attributes) > 0 {
		json.Unmarshal(res.Attributes, &attr)
	}

	acl, _ := attr["acl"].(string)
	if acl == "" {
		acl = "private" // Default
	}

	// Return ACL in AWS format
	resp := struct {
		XMLName xml.Name `xml:"AccessControlPolicy"`
		Owner   struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
		AccessControlList struct {
			Grant []struct {
				Grantee struct {
					ID          string `xml:"ID"`
					DisplayName string `xml:"DisplayName"`
				} `xml:"Grantee"`
				Permission string `xml:"Permission"`
			} `xml:"Grant"`
		} `xml:"AccessControlList"`
	}{
		Owner: struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		}{
			ID:          "FAKEOWNER",
			DisplayName: "local-snack",
		},
		AccessControlList: struct {
			Grant []struct {
				Grantee struct {
					ID          string `xml:"ID"`
					DisplayName string `xml:"DisplayName"`
				} `xml:"Grantee"`
				Permission string `xml:"Permission"`
			} `xml:"Grant"`
		}{
			Grant: []struct {
				Grantee struct {
					ID          string `xml:"ID"`
					DisplayName string `xml:"DisplayName"`
				} `xml:"Grantee"`
				Permission string `xml:"Permission"`
			}{
				{
					Grantee: struct {
						ID          string `xml:"ID"`
						DisplayName string `xml:"DisplayName"`
					}{
						ID:          "FAKEOWNER",
						DisplayName: "local-snack",
					},
					Permission: acl,
				},
			},
		},
	}

	awsresponses.WriteXML(w, resp)
}
