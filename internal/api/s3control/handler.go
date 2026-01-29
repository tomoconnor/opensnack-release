// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3control

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"opensnack/internal/api/s3"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"opensnack/internal/awsresponses"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

//
// --- XML Structures ---
//

type Tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type TagSet struct {
	Tags []Tag `xml:"Tag"`
}

type GetTaggingResult struct {
	XMLName struct{} `xml:"Tagging"`
	TagSet  TagSet   `xml:"TagSet"`
}

type ListTagsForResourceResult struct {
	XMLName struct{} `xml:"ListTagsForResourceResult"`
	Tags    TagSet   `xml:"Tags"`
}

type PutBucketTaggingRequest struct {
	XMLName struct{} `xml:"Tagging"`
	TagSet  TagSet   `xml:"TagSet"`
}

type listTagsForResourceRequest struct {
	XMLName     struct{} `xml:"ListTagsForResourceRequest"`
	ResourceArn string   `xml:"ResourceArn"`
}

//
// --- REQUIRED BY TERRAFORM ---
// ListTagsForResource (S3Control)
//

func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	// Endpoint looks like:
	// POST /s3-control/v20180820/tags/arn%3Aaws%3As3%3A%3A%3Abucket
	// Older clients may also send the ARN in the XML body.
	arnStr := extractARNFromRequest(r)
	if arnStr == "" {
		// Mirror AWS by returning empty tagset on missing resource
		w.WriteHeader(200)
		awsresponses.WriteXML(w, ListTagsForResourceResult{})
		return
	}

	bucket := util.ExtractBucketFromARN(arnStr)

	ns := util.NamespaceFromHeader(r)

	// Look up tags in the bucket’s resource entry
	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		// Return empty tagset — AWS does this
		w.WriteHeader(200)
		awsresponses.WriteXML(w, ListTagsForResourceResult{})
		return
	}

	var attrs map[string]any
	if err := xml.Unmarshal(res.Attributes, &attrs); err != nil {
		attrs = map[string]any{}
	}

	tagMap, _ := attrs["tags"].(map[string]any)
	var tags []Tag
	for k, v := range tagMap {
		tags = append(tags, Tag{Key: k, Value: fmt.Sprintf("%v", v)})
	}

	w.WriteHeader(200)
	awsresponses.WriteXML(w, ListTagsForResourceResult{
		Tags: TagSet{Tags: tags},
	})
}

// extractARNFromRequest tolerates both path-encoded and body-encoded ARNs.
func extractARNFromRequest(r *http.Request) string {
	// 1) Raw path suffix after /tags/
	if idx := strings.Index(r.URL.Path, "/tags/"); idx != -1 {
		raw := r.URL.Path[idx+len("/tags/"):]
		if decoded, err := url.PathUnescape(raw); err == nil {
			return decoded
		}
	}

	// 2) XML body
	body, _ := io.ReadAll(r.Body)
	// Restore body for downstream reuse
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	var req listTagsForResourceRequest
	if err := xml.Unmarshal(body, &req); err == nil && req.ResourceArn != "" {
		return req.ResourceArn
	}

	return ""
}

//
// --- Optional but recommended: PUT / GET bucket tagging ---
// Matches: PUT /bucket?tagging
//          GET /bucket?tagging
//

func (h *Handler) PutBucketTagging(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	// Extract bucket from path
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	bucket := parts[0]

	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, s3.NoSuchBucket(bucket))
		return
	}

	body, _ := io.ReadAll(r.Body)
	req := PutBucketTaggingRequest{}
	xml.Unmarshal(body, &req)

	// Convert tags to map[string]any
	tagMap := map[string]any{}
	for _, t := range req.TagSet.Tags {
		tagMap[t.Key] = t.Value
	}

	// Load attributes
	var attrs map[string]any
	json.Unmarshal(res.Attributes, &attrs)
	attrs["tags"] = tagMap

	buf, _ := json.Marshal(attrs)
	res.Attributes = buf
	if err := h.Store.Update(res); err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	w.WriteHeader(200)
}

func (h *Handler) GetBucketTagging(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	// Extract bucket from path
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	bucket := parts[0]

	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, s3.NoSuchBucket(bucket))
		return
	}

	var attrs map[string]any
	json.Unmarshal(res.Attributes, &attrs)

	tagMap, ok := attrs["tags"].(map[string]any)
	if !ok {
		// Return empty tagset if no tags stored
		w.WriteHeader(200)
		awsresponses.WriteXML(w, GetTaggingResult{})
		return
	}

	var tags []Tag
	for k, v := range tagMap {
		tags = append(tags, Tag{Key: k, Value: fmt.Sprintf("%v", v)})
	}

	w.WriteHeader(200)
	awsresponses.WriteXML(w, GetTaggingResult{
		TagSet: TagSet{Tags: tags},
	})
}
