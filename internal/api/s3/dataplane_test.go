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
	root := tempObjectRoot(t)

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

	// Verify file exists
	path := filepath.Join(root, "ns1", "mybucket", "hello.txt")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not written: %v", err)
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

	path := filepath.Join(root, "ns1", "b1", "a", "b", "c.txt")

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created")
	}

	// DELETE
	rec2, req2 := newCtx("DELETE", "/b1/a/b/c.txt", nil)
	h.DeleteObject(rec2, req2)

	if rec2.Code != 204 {
		t.Fatalf("expected 204, got %d", rec2.Code)
	}

	// File removed?
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after DELETE")
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
	root := tempObjectRoot(t)

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
	path := filepath.Join(root, "ns1", "binbucket", "file.bin")
	got, _ := os.ReadFile(path)

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
