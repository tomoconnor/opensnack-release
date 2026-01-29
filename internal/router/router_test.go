// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/s3"
	"opensnack/internal/resource"
	"opensnack/internal/router"

	"github.com/labstack/echo/v4"
)

type MockStore struct {
	data map[string]resource.Resource
}

func NewMockStore() *MockStore { return &MockStore{data: map[string]resource.Resource{}} }

func (m *MockStore) Create(r *resource.Resource) error {
	m.data[r.Namespace+"|"+r.ID] = *r
	return nil
}

func (m *MockStore) Update(r *resource.Resource) error {
	m.data[r.Namespace+"|"+r.ID] = *r
	return nil
}

func (m *MockStore) Get(id, service, typ, namespace string) (*resource.Resource, error) {
	r, ok := m.data[namespace+"|"+id]
	if !ok {
		return nil, echo.NewHTTPError(404)
	}
	return &r, nil
}

func (m *MockStore) List(service, typ, namespace string) ([]resource.Resource, error) {
	var out []resource.Resource
	for _, v := range m.data {
		if v.Service == service && v.Type == typ && v.Namespace == namespace {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *MockStore) Delete(id, service, typ, namespace string) error {
	delete(m.data, namespace+"|"+id)
	return nil
}

// ───────────────────────────────────────────────────────────
// ROUTING TESTS
// ───────────────────────────────────────────────────────────

func TestRouter_ListBucketsRoute(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Opensnack-Namespace", "ns")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRouter_CreateBucketRoute(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)

	req := httptest.NewRequest("PUT", "/abc", nil)
	req.Header.Set("X-Opensnack-Namespace", "ns")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRouter_DeleteBucketRoute(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)

	req := httptest.NewRequest("DELETE", "/dead", nil)
	req.Header.Set("X-Opensnack-Namespace", "ns")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
}

func TestRouter_HeadBucketRoute(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)

	// Create bucket
	h := s3.NewHandler(store)
	_ = h
	entry := s3.BucketEntry{Name: "head", CreationDate: time.Now()}
	buf, _ := json.Marshal(entry)
	store.Create(&resource.Resource{
		ID:         "head",
		Namespace:  "ns",
		Service:    "s3",
		Type:       "bucket",
		Attributes: buf,
	})

	req := httptest.NewRequest("HEAD", "/head", nil)
	req.Header.Set("X-Opensnack-Namespace", "ns")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRouter_LocationQueryRoute(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)

	// Create bucket
	entry := s3.BucketEntry{Name: "loc", CreationDate: time.Now()}
	buf, _ := json.Marshal(entry)
	store.Create(&resource.Resource{
		ID:         "loc",
		Namespace:  "ns1",
		Service:    "s3",
		Type:       "bucket",
		Attributes: buf,
	})

	req := httptest.NewRequest("GET", "/loc?location", nil)
	req.Header.Set("X-Opensnack-Namespace", "ns1")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<LocationConstraint>") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
