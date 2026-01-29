// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3_test

import (
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

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
		return nil, echo.NewHTTPError(404)
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

func newCtx(method, path string, body []byte) (echo.Context, *httptest.ResponseRecorder, *echo.Echo) {
	e := echo.New()

	var rdr io.Reader
	if body == nil {
		rdr = strings.NewReader("")
	} else {
		rdr = bytes.NewReader(body)
	}

	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	req.Header.Set("X-Opensnack-Namespace", "ns1")

	c := e.NewContext(req, rec)

	// Infer bucket and key from the path: /bucket/key...
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)

	bucket := parts[0]
	key := ""
	if len(parts) > 1 {
		key = parts[1]
	}

	c.SetParamNames("bucket", "*")
	c.SetParamValues(bucket, key)

	return c, rec, e
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
	c, rec, _ := newCtx("PUT", "/mybucket/hello.txt", body)

	err := h.PutObject(c)
	if err != nil {
		t.Fatalf("PutObject error: %v", err)
	}

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
	c2, rec2, _ := newCtx("GET", "/mybucket/hello.txt", nil)
	err = h.GetObject(c2)
	if err != nil {
		t.Fatalf("GetObject error: %v", err)
	}

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
	c, _, _ := newCtx("PUT", "/bucket1/x.txt", body)
	h.PutObject(c)

	// HEAD now
	c2, rec2, _ := newCtx("HEAD", "/bucket1/x.txt", nil)
	err := h.HeadObject(c2)
	if err != nil {
		t.Fatalf("HeadObject error: %v", err)
	}

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
	c, _, _ := newCtx("PUT", "/b1/a/b/c.txt", body)
	h.PutObject(c)

	path := filepath.Join(root, "ns1", "b1", "a", "b", "c.txt")

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created")
	}

	// DELETE
	c2, rec2, _ := newCtx("DELETE", "/b1/a/b/c.txt", nil)
	h.DeleteObject(c2)

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

	c, rec, _ := newCtx("PUT", "/idontexist/k.txt", body)
	err := h.PutObject(c)

	if err == nil {
		// handler returned XML directly, not an error
		if rec.Code != 404 {
			t.Fatalf("expected 404 for NoSuchBucket; got %d", rec.Code)
		}
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

	c, rec, _ := newCtx("GET", "/b2/nothing/here.txt", nil)
	err := h.GetObject(c)

	if err == nil {
		if rec.Code != 404 {
			t.Fatalf("expected 404 for missing key, got %d", rec.Code)
		}
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
	c, rec, _ := newCtx("PUT", "/binbucket/file.bin", body)
	h.PutObject(c)

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
	c2, rec2, _ := newCtx("GET", "/binbucket/file.bin", nil)
	h.GetObject(c2)

	if !bytes.Equal(rec2.Body.Bytes(), body) {
		t.Fatalf("GET returned wrong binary data")
	}
}
