// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

//
// OBJECT ROOT
//

func objectRoot() string {
	root := os.Getenv("OPENSNACK_OBJECT_ROOT")
	if root == "" {
		root = "/tmp/opensnack/objects"
	}
	return root
}

func objectPath(namespace, bucket, key string) string {
	return filepath.Join(objectRoot(), namespace, bucket, key)
}

//
// HELPERS
//

func ensureParentDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

func computeETag(body []byte) string {
	sum := md5.Sum(body)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func detectContentType(body []byte, key string) string {
	ext := filepath.Ext(key)
	if ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	return http.DetectContentType(body)
}

//
// XML ERRORS
//

type ErrorResponse struct {
	XMLName   struct{} `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource"`
	RequestID string   `xml:"RequestId"`
}

func NoSuchBucket(bucket string) ErrorResponse {
	return ErrorResponse{
		Code:      "NoSuchBucket",
		Message:   "The specified bucket does not exist",
		Resource:  bucket,
		RequestID: util.RandomHex(16),
	}
}

func NoSuchKey(bucket, key string) ErrorResponse {
	return ErrorResponse{
		Code:      "NoSuchKey",
		Message:   "The specified key does not exist",
		Resource:  bucket + "/" + key,
		RequestID: util.RandomHex(16),
	}
}

//
// JSON helpers
//

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

//
// PUT OBJECT
// PUT /:bucket/*
//

func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	bucket, key := extractBucketKey(r.URL.Path)

	// 1️⃣ Check bucket exists FIRST (AWS behavior)
	if _, err := h.Store.Get(bucket, "s3", "bucket", ns); err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchBucket(bucket))
		return
	}

	// 2️⃣ Validate key
	if key == "" {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchKey(bucket, key))
		return
	}

	// 3️⃣ Read body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	// 4️⃣ Write file
	path := objectPath(ns, bucket, key)

	if err := ensureParentDir(path); err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	if err := os.WriteFile(path, bodyBytes, 0o644); err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	etag := computeETag(bodyBytes)
	contentType := detectContentType(bodyBytes, key)

	// 5️⃣ Store metadata
	meta := map[string]any{
		"bucket":       bucket,
		"key":          key,
		"etag":         etag,
		"content_type": contentType,
		"size":         len(bodyBytes),
		"created_at":   time.Now().Format(time.RFC3339),
	}

	buf, _ := jsonMarshal(meta)

	h.Store.Create(&resource.Resource{
		ID:         bucket + "/" + key,
		Namespace:  ns,
		Service:    "s3",
		Type:       "object",
		Attributes: buf,
	})

	w.Header().Set("ETag", etag)
	w.WriteHeader(200)
}

//
// GET OBJECT
//

func (h *Handler) GetObject(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	bucket, key := extractBucketKey(r.URL.Path)

	// Bucket exists?
	if _, err := h.Store.Get(bucket, "s3", "bucket", ns); err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchBucket(bucket))
		return
	}

	res, err := h.Store.Get(bucket+"/"+key, "s3", "object", ns)
	if err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchKey(bucket, key))
		return
	}

	var meta map[string]any
	jsonUnmarshal(res.Attributes, &meta)

	path := objectPath(ns, bucket, key)
	bodyBytes, err := os.ReadFile(path)
	if err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchKey(bucket, key))
		return
	}

	etag := meta["etag"].(string)
	contentType := meta["content_type"].(string)
	size := int(meta["size"].(float64))

	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.WriteHeader(200)
	w.Write(bodyBytes)
}

//
// HEAD OBJECT
//

func (h *Handler) HeadObject(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	bucket, key := extractBucketKey(r.URL.Path)

	if _, err := h.Store.Get(bucket, "s3", "bucket", ns); err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchBucket(bucket))
		return
	}

	res, err := h.Store.Get(bucket+"/"+key, "s3", "object", ns)
	if err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchKey(bucket, key))
		return
	}

	var meta map[string]any
	jsonUnmarshal(res.Attributes, &meta)

	etag := meta["etag"].(string)
	contentType := meta["content_type"].(string)
	size := int(meta["size"].(float64))

	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.WriteHeader(200)
}

//
// DELETE OBJECT
//

func (h *Handler) DeleteObject(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)
	bucket, key := extractBucketKey(r.URL.Path)

	// S3: deleting missing bucket is NOT an error
	// but deleting missing key in existing bucket returns 204 silently

	// If bucket missing, S3 returns 404 NoSuchBucket for DELETE
	if _, err := h.Store.Get(bucket, "s3", "bucket", ns); err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchBucket(bucket))
		return
	}

	path := objectPath(ns, bucket, key)
	_ = os.Remove(path)

	// Delete metadata even if missing
	h.Store.Delete(bucket+"/"+key, "s3", "object", ns)

	w.WriteHeader(204)
}
