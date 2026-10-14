// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/util"
)

//
// OBJECT STORAGE
//
// Object bytes live in blob files named by random IDs, under a directory per
// namespace:
//
//	$OPENSNACK_OBJECT_ROOT/<sha256(namespace)[:32]>/<id[:2]>/<id>
//
// The metadata row names the blob, so neither a key ("../../other/x") nor a
// namespace can steer a write outside its own directory, and a concurrent
// overwrite can never pair one write's metadata with another's bytes. Each
// write gets a new blob; the old one is removed once the metadata points at
// the new one.
//

func objectRoot() string {
	root := os.Getenv("OPENSNACK_OBJECT_ROOT")
	if root == "" {
		root = "/tmp/opensnack/objects"
	}
	return root
}

// NamespaceDir is where a namespace's object blobs live.
func NamespaceDir(namespace string) string {
	sum := sha256.Sum256([]byte(namespace))
	return filepath.Join(objectRoot(), hex.EncodeToString(sum[:16]))
}

func blobPath(namespace, blob string) string {
	return filepath.Join(NamespaceDir(namespace), blob[:2], blob)
}

// DeleteNamespaceObjects removes every object blob a namespace owns.
func DeleteNamespaceObjects(namespace string) error {
	return os.RemoveAll(NamespaceDir(namespace))
}

// writeBlob stores body under a fresh blob ID.
func writeBlob(namespace string, body []byte) (string, error) {
	blob := util.RandomHex(16)
	path := blobPath(namespace, blob)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	return blob, os.Rename(tmp, path)
}

func removeBlob(namespace, blob string) {
	if blob != "" {
		_ = os.Remove(blobPath(namespace, blob))
	}
}

// ReadObject returns an object's bytes, for the internal inspection API.
func (h *Handler) ReadObject(namespace, bucket, key string) ([]byte, error) {
	info, err := h.ReadObjectInfo(namespace, bucket, key)
	if err != nil {
		return nil, err
	}
	return info.Body, nil
}

// ObjectInfo is an object's current bytes and the metadata other services
// (CloudFront, Lambda) need to serve or use it.
type ObjectInfo struct {
	Body         []byte
	ContentType  string
	ETag         string
	LastModified time.Time
	CacheControl string
}

// ReadObjectInfo reads the current version of an object. It returns
// os.ErrNotExist for a missing object or one whose current version is a
// delete marker.
func (h *Handler) ReadObjectInfo(namespace, bucket, key string) (*ObjectInfo, error) {
	res, err := h.Store.Get(bucket+"/"+key, "s3", "object", namespace)
	if err != nil {
		return nil, os.ErrNotExist
	}
	var meta objectMeta
	if err := json.Unmarshal(res.Attributes, &meta); err != nil || meta.Blob == "" || meta.DeleteMarker {
		return nil, os.ErrNotExist
	}
	body, err := os.ReadFile(blobPath(namespace, meta.Blob))
	if err != nil {
		return nil, err
	}
	return &ObjectInfo{
		Body:         body,
		ContentType:  meta.ContentType,
		ETag:         meta.ETag,
		LastModified: meta.modTime(),
		CacheControl: meta.Headers["Cache-Control"],
	}, nil
}

//
// METADATA
//

// objectMeta is what the store keeps for each object.
type objectMeta struct {
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	ETag         string            `json:"etag"`
	ContentType  string            `json:"content_type"`
	Size         int64             `json:"size"`
	Blob         string            `json:"blob"`
	LastModified string            `json:"last_modified"`
	CreatedAt    string            `json:"created_at"` // kept equal to last_modified for older readers
	Headers      map[string]string `json:"headers,omitempty"`
	UserMetadata map[string]string `json:"user_metadata,omitempty"`
	VersionID    string            `json:"version_id,omitempty"`
	DeleteMarker bool              `json:"delete_marker,omitempty"`
	Seq          int64             `json:"seq,omitempty"`
	Tags         []Tag             `json:"tags,omitempty"`
}

func (m *objectMeta) modTime() time.Time {
	for _, s := range []string{m.LastModified, m.CreatedAt} {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// storedHeaders are request headers S3 keeps with the object and returns on
// GET and HEAD.
var storedHeaders = []string{
	"Cache-Control",
	"Content-Disposition",
	"Content-Encoding",
	"Content-Language",
	"Expires",
	"X-Amz-Server-Side-Encryption",
	"X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id",
	"X-Amz-Server-Side-Encryption-Bucket-Key-Enabled",
	"X-Amz-Storage-Class",
	"X-Amz-Website-Redirect-Location",
}

func (m *objectMeta) writeHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("ETag", m.ETag)
	h.Set("Content-Type", m.ContentType)
	h.Set("Last-Modified", m.modTime().Format(http.TimeFormat))
	h.Set("Accept-Ranges", "bytes")
	for k, v := range m.Headers {
		h.Set(k, v)
	}
	for k, v := range m.UserMetadata {
		h.Set("X-Amz-Meta-"+k, v)
	}
	if m.VersionID != "" {
		h.Set("X-Amz-Version-Id", m.VersionID)
	}
	if len(m.Tags) > 0 {
		h.Set("X-Amz-Tagging-Count", strconv.Itoa(len(m.Tags)))
	}
}

//
// HELPERS
//

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

// readBody returns the object bytes, decoding the aws-chunked framing SDKs
// use for streaming uploads (signed chunks, or unsigned chunks with a
// trailing checksum).
func readBody(r *http.Request) ([]byte, error) {
	sha := r.Header.Get("X-Amz-Content-Sha256")
	chunked := strings.HasPrefix(sha, "STREAMING-")
	for _, enc := range r.Header.Values("Content-Encoding") {
		for _, e := range strings.Split(enc, ",") {
			if strings.TrimSpace(e) == "aws-chunked" {
				chunked = true
			}
		}
	}
	if !chunked {
		return io.ReadAll(r.Body)
	}
	return decodeAWSChunked(r.Body)
}

func decodeAWSChunked(body io.Reader) ([]byte, error) {
	br := bufio.NewReader(body)
	var out bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("aws-chunked: reading chunk header: %w", err)
		}
		sizeHex, _, _ := strings.Cut(strings.TrimRight(line, "\r\n"), ";")
		size, err := strconv.ParseInt(strings.TrimSpace(sizeHex), 16, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("aws-chunked: bad chunk size %q", sizeHex)
		}
		if size == 0 {
			// Trailers (x-amz-checksum-*) and the final CRLF follow; nothing
			// in them changes the object bytes.
			return out.Bytes(), nil
		}
		if _, err := io.CopyN(&out, br, size); err != nil {
			return nil, fmt.Errorf("aws-chunked: short chunk: %w", err)
		}
		if _, err := br.Discard(2); err != nil { // CRLF after the chunk data
			return nil, fmt.Errorf("aws-chunked: %w", err)
		}
	}
}

// contentEncodingWithoutChunked drops the transport-only aws-chunked token.
func contentEncodingWithoutChunked(v string) string {
	var keep []string
	for _, e := range strings.Split(v, ",") {
		if e = strings.TrimSpace(e); e != "" && e != "aws-chunked" {
			keep = append(keep, e)
		}
	}
	return strings.Join(keep, ",")
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

func writeS3Error(w http.ResponseWriter, status int, code, msg, resource string) {
	awsresponses.WriteS3ErrorXML(w, status, code, msg, resource)
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

// PutObject stores an object, as a new version when the bucket is
// versioned. It honours the conditional-write headers: If-None-Match: *
// (create only, which is how OpenTofu's use_lockfile takes the state lock)
// and If-Match: <etag>.
func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	bucket, key := extractBucketKey(r.URL.Path)

	_, bmeta, err := h.loadBucket(ns, bucket)
	if err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchBucket(bucket))
		return
	}
	if key == "" {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchKey(bucket, key))
		return
	}

	body, err := readBody(r)
	if err != nil {
		writeS3Error(w, http.StatusBadRequest, "IncompleteBody", err.Error(), "/"+bucket+"/"+key)
		return
	}
	if want := r.Header.Get("Content-MD5"); want != "" {
		sum := md5.Sum(body)
		if base64.StdEncoding.EncodeToString(sum[:]) != want {
			writeS3Error(w, http.StatusBadRequest, "BadDigest",
				"The Content-MD5 you specified did not match what we received.", "/"+bucket+"/"+key)
			return
		}
	}
	tags, ok := tagsFromHeader(w, r)
	if !ok {
		return
	}

	existing, getErr := h.Store.Get(bucket+"/"+key, "s3", "object", ns)
	exists := getErr == nil
	var old objectMeta
	if exists {
		_ = json.Unmarshal(existing.Attributes, &old)
	}

	ifNoneMatch := r.Header.Get("If-None-Match")
	ifMatch := r.Header.Get("If-Match")
	if ifNoneMatch == "*" && exists {
		preconditionFailed(w, "If-None-Match")
		return
	}
	if ifMatch != "" && (!exists || !etagMatches(ifMatch, old.ETag)) {
		if !exists {
			w.WriteHeader(404)
			awsresponses.WriteXML(w, NoSuchKey(bucket, key))
			return
		}
		preconditionFailed(w, "If-Match")
		return
	}

	blob, err := writeBlob(ns, body)
	if err != nil {
		writeS3Error(w, 500, "InternalError", err.Error(), "")
		return
	}

	meta := metaFromRequest(r, bucket, key, body)
	meta.Blob = blob
	meta.Tags = tags
	applyDefaultEncryption(meta.Headers, bmeta, r)

	if err := h.storeObject(ns, bmeta.Versioning, &meta, ifNoneMatch == "*"); err != nil {
		removeBlob(ns, blob)
		if errors.Is(err, errPrecondition) {
			// Lost a race with another create-only writer: the unique index
			// is the real lock.
			preconditionFailed(w, "If-None-Match")
			return
		}
		writeS3Error(w, 500, "InternalError", err.Error(), "")
		return
	}

	w.Header().Set("ETag", meta.ETag)
	writeWriteHeaders(w, &meta)
	w.WriteHeader(200)
}

// metaFromRequest builds an object's metadata from the content headers of a
// PUT (or of a CopyObject that replaces metadata).
func metaFromRequest(r *http.Request, bucket, key string, body []byte) objectMeta {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := objectMeta{
		Bucket:       bucket,
		Key:          key,
		ETag:         computeETag(body),
		ContentType:  r.Header.Get("Content-Type"),
		Size:         int64(len(body)),
		LastModified: now,
		CreatedAt:    now,
		Headers:      map[string]string{},
		UserMetadata: map[string]string{},
	}
	if meta.ContentType == "" {
		meta.ContentType = detectContentType(body, key)
	}
	for _, name := range storedHeaders {
		if v := r.Header.Get(name); v != "" {
			meta.Headers[name] = v
		}
	}
	if v := contentEncodingWithoutChunked(meta.Headers["Content-Encoding"]); v != "" {
		meta.Headers["Content-Encoding"] = v
	} else {
		delete(meta.Headers, "Content-Encoding")
	}
	for name, vals := range r.Header {
		if k, ok := strings.CutPrefix(strings.ToLower(name), "x-amz-meta-"); ok && len(vals) > 0 {
			meta.UserMetadata[k] = vals[0]
		}
	}
	return meta
}

// writeWriteHeaders sets the headers a write returns: the SSE settings the
// object got and its version ID.
func writeWriteHeaders(w http.ResponseWriter, meta *objectMeta) {
	for k, v := range meta.Headers {
		if strings.HasPrefix(k, "X-Amz-Server-Side-Encryption") {
			w.Header().Set(k, v)
		}
	}
	if meta.VersionID != "" {
		w.Header().Set("X-Amz-Version-Id", meta.VersionID)
	}
}

func etagMatches(header, etag string) bool {
	for _, want := range strings.Split(header, ",") {
		want = strings.TrimSpace(want)
		if want == "*" || want == etag || `"`+want+`"` == etag {
			return true
		}
	}
	return false
}

func preconditionFailed(w http.ResponseWriter, condition string) {
	writeS3Error(w, http.StatusPreconditionFailed, "PreconditionFailed",
		"At least one of the pre-conditions you specified did not hold", condition)
}

//
// GET / HEAD OBJECT
//

// lastModified renders the stored modification time as an HTTP date. The AWS
// CLI requires Last-Modified on object responses and fails the transfer
// without it.
func lastModified(meta map[string]any) string {
	for _, k := range []string{"last_modified", "created_at"} {
		s, _ := meta[k].(string)
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC().Format(http.TimeFormat)
		}
	}
	return time.Now().UTC().Format(http.TimeFormat)
}

// lookupObject finds the version a request addresses (?versionId, else the
// current one). It writes the error itself, including the delete-marker
// responses, and returns nil when there is nothing to serve.
func (h *Handler) lookupObject(w http.ResponseWriter, r *http.Request) (string, *version) {
	ns := util.GlobalNamespace(r)
	bucket, key := extractBucketKey(r.URL.Path)
	head := r.Method == http.MethodHead
	fail := func(status int, body any) {
		w.WriteHeader(status)
		if !head {
			awsresponses.WriteXML(w, body)
		}
	}

	if _, err := h.Store.Get(bucket, "s3", "bucket", ns); err != nil {
		fail(404, NoSuchBucket(bucket))
		return ns, nil
	}
	vid := r.URL.Query().Get("versionId")

	// The common case, the current version, needs no scan of the versions.
	if vid == "" {
		if res, err := h.Store.Get(bucket+"/"+key, "s3", "object", ns); err == nil {
			if m, ok := decodeMeta(*res); ok {
				m.Bucket, m.Key = bucket, key
				return ns, &version{m, true}
			}
		}
	}
	v, err := h.findVersion(ns, bucket, key, vid)
	if err != nil {
		writeS3Error(w, 500, "InternalError", err.Error(), "")
		return ns, nil
	}
	if v == nil {
		if vid == "" {
			fail(404, NoSuchKey(bucket, key))
			return ns, nil
		}
		if head {
			w.WriteHeader(404)
			return ns, nil
		}
		writeS3Error(w, 404, "NoSuchVersion",
			"The specified version does not exist.", "/"+bucket+"/"+key)
		return ns, nil
	}
	if v.DeleteMarker {
		w.Header().Set("X-Amz-Delete-Marker", "true")
		w.Header().Set("X-Amz-Version-Id", v.vid())
		w.Header().Set("Last-Modified", v.modTime().Format(http.TimeFormat))
		if vid == "" {
			fail(404, NoSuchKey(bucket, key))
			return ns, nil
		}
		w.Header().Set("Allow", "DELETE")
		if head {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return ns, nil
		}
		writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed",
			"The specified method is not allowed against this resource.", "/"+bucket+"/"+key)
		return ns, nil
	}
	return ns, v
}

// GetObject serves the object through http.ServeContent, which gives us
// Range requests and the If-Match / If-None-Match / If-Modified-Since
// conditions for free, and answers HEAD the same way.
func (h *Handler) GetObject(w http.ResponseWriter, r *http.Request) {
	ns, v := h.lookupObject(w, r)
	if v == nil {
		return
	}
	meta := &v.objectMeta

	if meta.Blob == "" {
		// Metadata written before blobs existed has no bytes we can find.
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchKey(meta.Bucket, meta.Key))
		return
	}
	f, err := os.Open(blobPath(ns, meta.Blob))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(404)
			awsresponses.WriteXML(w, NoSuchKey(meta.Bucket, meta.Key))
			return
		}
		writeS3Error(w, 500, "InternalError", err.Error(), "")
		return
	}
	defer f.Close()

	meta.writeHeaders(w)
	http.ServeContent(w, r, "", meta.modTime(), f)
}

func (h *Handler) HeadObject(w http.ResponseWriter, r *http.Request) {
	_, v := h.lookupObject(w, r)
	if v == nil {
		return
	}
	v.writeHeaders(w)
	w.Header().Set("Content-Length", strconv.FormatInt(v.Size, 10))
	w.WriteHeader(200)
}

//
// DELETE OBJECT
//

func (h *Handler) DeleteObject(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	bucket, key := extractBucketKey(r.URL.Path)

	_, bmeta, err := h.loadBucket(ns, bucket)
	if err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchBucket(bucket))
		return
	}

	var res deleteResult
	if vid := r.URL.Query().Get("versionId"); vid != "" {
		res, err = h.deleteVersion(ns, bucket, key, vid)
	} else {
		res, err = h.deleteKey(ns, bmeta.Versioning, bucket, key)
	}
	if err != nil {
		writeS3Error(w, 500, "InternalError", err.Error(), "")
		return
	}
	if res.VersionID != "" {
		w.Header().Set("X-Amz-Version-Id", res.VersionID)
	}
	if res.DeleteMarker {
		w.Header().Set("X-Amz-Delete-Marker", "true")
	}
	w.WriteHeader(204)
}
