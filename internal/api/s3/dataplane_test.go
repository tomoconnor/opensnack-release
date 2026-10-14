// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/s3"
	"opensnack/internal/resource"
)

//
// Mock Store (same pattern used for SNS/SQS/IAM)
//

// type MockStore struct {
// 	data map[string]resource.Resource
// }

// func NewMockStore() *MockStore {
// 	return &MockStore{
// 		data: map[string]resource.Resource{},
// 	}
// }

func key(id, ns string) string {
	return ns + "|" + id
}

func (m *MockStore) Create(r *resource.Resource) error {
	m.data[key(r.ID, r.Namespace)] = *r
	return nil
}

func (m *MockStore) Update(r *resource.Resource) error {
	m.data[key(r.ID, r.Namespace)] = *r
	return nil
}

func (m *MockStore) Get(id, service, typ, ns string) (*resource.Resource, error) {
	v, ok := m.data[key(id, ns)]
	if !ok {
		return nil, errors.New("not found")
	}
	return &v, nil
}

func (m *MockStore) List(service, typ, ns string) ([]resource.Resource, error) {
	var out []resource.Resource
	for _, v := range m.data {
		if v.Service == service && v.Type == typ && v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *MockStore) Delete(id, service, typ, ns string) error {
	delete(m.data, key(id, ns))
	return nil
}

//
// Test helpers
//

func newCtx(method, path string, body []byte) (*httptest.ResponseRecorder, *http.Request) {
	var rdr io.Reader
	if body == nil {
		rdr = strings.NewReader("")
	} else {
		rdr = bytes.NewReader(body)
	}

	// The handlers derive bucket and key from r.URL.Path via extractBucketKey,
	// so the path alone is enough here.
	req := httptest.NewRequest(method, path, rdr)
	// Namespace isolation travels in the User-Agent (see k6/README.md):
	// Terraform cannot set custom headers, so a "custom-<ns>" suffix carries it.
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")

	return httptest.NewRecorder(), req
}

//
// Test root directory (temporary)
//

func tempObjectRoot(t *testing.T) string {
	dir, err := os.MkdirTemp("", "opensnack_s3_test_*")
	if err != nil {
		t.Fatalf("mktemp failed: %v", err)
	}
	t.Setenv("OPENSNACK_OBJECT_ROOT", dir)
	return dir
}

//
// Tests
//

func TestPutAndGetObject(t *testing.T) {
	tempObjectRoot(t)

	store := NewMockStore()

	// Create bucket metadata
	store.Create(&resource.Resource{
		ID:        "mybucket",
		Namespace: "ns1",
		Service:   "s3",
		Type:      "bucket",
	})

	h := s3.NewHandler(store)

	// PUT object
	body := []byte("hello world")
	rec, req := newCtx("PUT", "/mybucket/hello.txt", body)

	h.PutObject(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if rec.Header().Get("ETag") == "" {
		t.Fatalf("ETag missing")
	}

	// Verify the bytes were stored
	if got, err := h.ReadObject("ns1", "mybucket", "hello.txt"); err != nil || string(got) != "hello world" {
		t.Fatalf("object not stored: %q %v", got, err)
	}

	// GET object
	rec2, req2 := newCtx("GET", "/mybucket/hello.txt", nil)
	h.GetObject(rec2, req2)

	if rec2.Code != 200 {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}

	if string(rec2.Body.Bytes()) != "hello world" {
		t.Fatalf("wrong body: %s", rec2.Body.String())
	}
}

func TestHeadObject(t *testing.T) {
	tempObjectRoot(t)
	store := NewMockStore()

	store.Create(&resource.Resource{
		ID:        "bucket1",
		Namespace: "ns1",
		Service:   "s3",
		Type:      "bucket",
	})

	h := s3.NewHandler(store)

	// PUT first
	body := []byte("abc123")
	rec, req := newCtx("PUT", "/bucket1/x.txt", body)
	h.PutObject(rec, req)

	// HEAD now
	rec2, req2 := newCtx("HEAD", "/bucket1/x.txt", nil)
	h.HeadObject(rec2, req2)

	if rec2.Code != 200 {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}

	if rec2.Body.Len() != 0 {
		t.Fatalf("HEAD should return no body")
	}

	if rec2.Header().Get("ETag") == "" {
		t.Fatalf("missing ETag")
	}
}

func TestDeleteObject(t *testing.T) {
	root := tempObjectRoot(t)
	store := NewMockStore()

	store.Create(&resource.Resource{
		ID:        "b1",
		Namespace: "ns1",
		Service:   "s3",
		Type:      "bucket",
	})

	h := s3.NewHandler(store)

	// PUT object
	body := []byte("zzz")
	rec, req := newCtx("PUT", "/b1/a/b/c.txt", body)
	h.PutObject(rec, req)

	if _, err := h.ReadObject("ns1", "b1", "a/b/c.txt"); err != nil {
		t.Fatalf("object not created: %v", err)
	}

	// DELETE
	rec2, req2 := newCtx("DELETE", "/b1/a/b/c.txt", nil)
	h.DeleteObject(rec2, req2)

	if rec2.Code != 204 {
		t.Fatalf("expected 204, got %d", rec2.Code)
	}

	// Object gone, blob file included?
	if _, err := h.ReadObject("ns1", "b1", "a/b/c.txt"); !os.IsNotExist(err) {
		t.Fatalf("object still exists after DELETE: %v", err)
	}
	if n := countBlobs(t, root); n != 0 {
		t.Fatalf("%d blob files left after DELETE", n)
	}
}

func TestNoSuchBucket(t *testing.T) {
	tempObjectRoot(t)
	store := NewMockStore()
	h := s3.NewHandler(store)

	body := []byte("abc")

	rec, req := newCtx("PUT", "/idontexist/k.txt", body)
	h.PutObject(rec, req)

	if rec.Code != 404 {
		t.Fatalf("expected 404 for NoSuchBucket; got %d", rec.Code)
	}
}

func TestNoSuchKey(t *testing.T) {
	tempObjectRoot(t)
	store := NewMockStore()

	// Create bucket only
	store.Create(&resource.Resource{
		ID:        "b2",
		Namespace: "ns1",
		Service:   "s3",
		Type:      "bucket",
	})

	h := s3.NewHandler(store)

	rec, req := newCtx("GET", "/b2/nothing/here.txt", nil)
	h.GetObject(rec, req)

	if rec.Code != 404 {
		t.Fatalf("expected 404 for missing key, got %d", rec.Code)
	}
}

func TestBinaryUpload(t *testing.T) {
	tempObjectRoot(t)

	store := NewMockStore()
	store.Create(&resource.Resource{
		ID:        "binbucket",
		Namespace: "ns1",
		Service:   "s3",
		Type:      "bucket",
	})

	h := s3.NewHandler(store)

	// Binary body
	body := []byte{0x00, 0xFF, 0xAA, 0x55}
	rec, req := newCtx("PUT", "/binbucket/file.bin", body)
	h.PutObject(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200")
	}

	// Verify file contents exactly
	got, _ := h.ReadObject("ns1", "binbucket", "file.bin")

	if !bytes.Equal(got, body) {
		t.Fatalf("binary mismatch: got %v want %v", got, body)
	}

	// GET and compare
	rec2, req2 := newCtx("GET", "/binbucket/file.bin", nil)
	h.GetObject(rec2, req2)

	if !bytes.Equal(rec2.Body.Bytes(), body) {
		t.Fatalf("GET returned wrong binary data")
	}
}

// Regression: object responses omitted Last-Modified, which the AWS CLI requires
// — `aws s3 cp` down failed with "fatal error: 'LastModified'".
func TestObjectResponsesCarryLastModified(t *testing.T) {
	tempObjectRoot(t)
	store := NewMockStore()
	store.Create(&resource.Resource{
		ID: "lmb", Namespace: "ns1", Service: "s3", Type: "bucket",
	})
	h := s3.NewHandler(store)

	rec, req := newCtx("PUT", "/lmb/f.txt", []byte("data"))
	h.PutObject(rec, req)

	for name, call := range map[string]http.HandlerFunc{
		"GetObject":  h.GetObject,
		"HeadObject": h.HeadObject,
	} {
		t.Run(name, func(t *testing.T) {
			rec, req := newCtx("GET", "/lmb/f.txt", nil)
			call(rec, req)

			if rec.Code != 200 {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			lm := rec.Header().Get("Last-Modified")
			if lm == "" {
				t.Fatal("Last-Modified header missing")
			}
			if _, err := time.Parse(http.TimeFormat, lm); err != nil {
				t.Fatalf("Last-Modified is not an HTTP date: %q", lm)
			}
		})
	}
}

func countBlobs(t *testing.T, root string) int {
	t.Helper()
	n := 0
	filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func newBucket(t *testing.T, store *MockStore, name string) {
	t.Helper()
	store.Create(&resource.Resource{ID: name, Namespace: "ns1", Service: "s3", Type: "bucket"})
}

// The state backend rewrites the same key on every apply.
func TestPutObjectOverwrite(t *testing.T) {
	root := tempObjectRoot(t)
	store := NewMockStore()
	newBucket(t, store, "state")
	h := s3.NewHandler(store)

	for _, body := range []string{`{"serial":1}`, `{"serial":2,"longer":true}`} {
		rec, req := newCtx("PUT", "/state/env/terraform.tfstate", []byte(body))
		req.Header.Set("Content-Type", "application/json")
		h.PutObject(rec, req)
		if rec.Code != 200 {
			t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
		}
	}

	rec, req := newCtx("GET", "/state/env/terraform.tfstate", nil)
	h.GetObject(rec, req)
	if rec.Body.String() != `{"serial":2,"longer":true}` {
		t.Fatalf("stale body: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != "26" {
		t.Fatalf("stale Content-Length %s", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type %q, want the one the client sent", got)
	}
	if n := countBlobs(t, root); n != 1 {
		t.Fatalf("expected the old blob to be removed, found %d", n)
	}
}

// OpenTofu's use_lockfile takes the lock with a create-only conditional put.
func TestPutObjectConditional(t *testing.T) {
	tempObjectRoot(t)
	store := NewMockStore()
	newBucket(t, store, "state")
	h := s3.NewHandler(store)

	put := func(hdr, val, body string) *httptest.ResponseRecorder {
		rec, req := newCtx("PUT", "/state/terraform.tfstate.tflock", []byte(body))
		if hdr != "" {
			req.Header.Set(hdr, val)
		}
		h.PutObject(rec, req)
		return rec
	}

	if rec := put("If-None-Match", "*", "lock-a"); rec.Code != 200 {
		t.Fatalf("first lock: %d", rec.Code)
	}
	if rec := put("If-None-Match", "*", "lock-b"); rec.Code != 412 || !strings.Contains(rec.Body.String(), "PreconditionFailed") {
		t.Fatalf("second lock should fail: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := h.ReadObject("ns1", "state", "terraform.tfstate.tflock"); string(got) != "lock-a" {
		t.Fatalf("lock overwritten: %s", got)
	}

	if rec := put("If-Match", `"nope"`, "x"); rec.Code != 412 {
		t.Fatalf("If-Match mismatch: %d", rec.Code)
	}
	etag := put("", "", "v2").Header().Get("ETag")
	if rec := put("If-Match", etag, "v3"); rec.Code != 200 {
		t.Fatalf("If-Match with the current ETag: %d", rec.Code)
	}

	// Unlock, then the lock can be taken again.
	rec, req := newCtx("DELETE", "/state/terraform.tfstate.tflock", nil)
	h.DeleteObject(rec, req)
	if rec := put("If-None-Match", "*", "lock-c"); rec.Code != 200 {
		t.Fatalf("relock: %d", rec.Code)
	}
}

// SDKs stream uploads with aws-chunked framing over TLS.
func TestPutObjectAWSChunked(t *testing.T) {
	tempObjectRoot(t)
	store := NewMockStore()
	newBucket(t, store, "b")
	h := s3.NewHandler(store)

	for name, tc := range map[string]struct{ body, sha string }{
		"signed": {"5;chunk-signature=abc\r\nhello\r\n6;chunk-signature=def\r\n world\r\n0;chunk-signature=fff\r\n\r\n",
			"STREAMING-AWS4-HMAC-SHA256-PAYLOAD"},
		"unsigned trailer": {"b\r\nhello world\r\n0\r\nx-amz-checksum-crc32:DUoRhQ==\r\n\r\n",
			"STREAMING-UNSIGNED-PAYLOAD-TRAILER"},
	} {
		rec, req := newCtx("PUT", "/b/"+strings.ReplaceAll(name, " ", "-"), []byte(tc.body))
		req.Header.Set("X-Amz-Content-Sha256", tc.sha)
		req.Header.Set("Content-Encoding", "aws-chunked")
		req.Header.Set("X-Amz-Decoded-Content-Length", "11")
		h.PutObject(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		got, _ := h.ReadObject("ns1", "b", strings.ReplaceAll(name, " ", "-"))
		if string(got) != "hello world" {
			t.Fatalf("%s: decoded %q", name, got)
		}

		rec, req = newCtx("HEAD", "/b/"+strings.ReplaceAll(name, " ", "-"), nil)
		h.HeadObject(rec, req)
		if ce := rec.Header().Get("Content-Encoding"); ce != "" {
			t.Fatalf("%s: aws-chunked leaked into Content-Encoding: %q", name, ce)
		}
	}
}

// A key can't climb out of the namespace's directory into someone else's.
func TestPutObjectKeyTraversal(t *testing.T) {
	root := tempObjectRoot(t)
	store := NewMockStore()
	newBucket(t, store, "b")
	h := s3.NewHandler(store)

	rec, req := newCtx("PUT", "/b/../../victim/b/secret", []byte("pwned"))
	h.PutObject(rec, req)

	nsDir := s3.NamespaceDir("ns1")
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasPrefix(p, nsDir+string(os.PathSeparator)) {
			t.Fatalf("object written outside the namespace directory: %s", p)
		}
		return nil
	})
}

func TestGetObjectRange(t *testing.T) {
	tempObjectRoot(t)
	store := NewMockStore()
	newBucket(t, store, "b")
	h := s3.NewHandler(store)

	rec, req := newCtx("PUT", "/b/k", []byte("0123456789"))
	h.PutObject(rec, req)

	rec, req = newCtx("GET", "/b/k", nil)
	req.Header.Set("Range", "bytes=2-4")
	h.GetObject(rec, req)
	if rec.Code != 206 || rec.Body.String() != "234" {
		t.Fatalf("range: %d %q", rec.Code, rec.Body.String())
	}
}

func TestDeleteBucketNotEmpty(t *testing.T) {
	tempObjectRoot(t)
	store := NewMockStore()
	newBucket(t, store, "b")
	h := s3.NewHandler(store)

	rec, req := newCtx("PUT", "/b/k", []byte("x"))
	h.PutObject(rec, req)

	rec, req = newCtx("DELETE", "/b", nil)
	h.DeleteBucket(rec, req)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "BucketNotEmpty") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}

	rec, req = newCtx("DELETE", "/b/k", nil)
	h.DeleteObject(rec, req)
	rec, req = newCtx("DELETE", "/b", nil)
	h.DeleteBucket(rec, req)
	if rec.Code != 204 {
		t.Fatalf("delete emptied bucket: %d %s", rec.Code, rec.Body.String())
	}

	rec, req = newCtx("DELETE", "/b", nil)
	h.DeleteBucket(rec, req)
	if rec.Code != 404 {
		t.Fatalf("delete missing bucket: %d", rec.Code)
	}
}
