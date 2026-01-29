// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3_test

import (
	"encoding/json"
	"net/http/httptest"
	"opensnack/internal/api/s3"
	"opensnack/internal/resource"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestCreateBucket_AlreadyExists(t *testing.T) {
	store := NewMockStore()
	h := s3.NewHandler(store)
	e := echo.New()

	// Precreate bucket
	entry := s3.BucketEntry{Name: "dup", CreationDate: time.Now()}
	buf, _ := json.Marshal(entry)
	store.Create(&resource.Resource{
		ID:         "dup",
		Namespace:  "ns1",
		Service:    "s3",
		Type:       "bucket",
		Attributes: buf,
	})

	req := httptest.NewRequest("PUT", "/dup", nil)
	req.Header.Set("X-Opensnack-Namespace", "ns1")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	c.SetParamNames("bucket")
	c.SetParamValues("dup")

	err := h.CreateBucket(c)
	if err != nil {
		t.Fatal(err)
	}

	if rec.Code != 409 {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}

func TestHeadBucket_NotExists(t *testing.T) {
	store := NewMockStore()
	h := s3.NewHandler(store)
	e := echo.New()

	req := httptest.NewRequest("HEAD", "/ghost", nil)
	req.Header.Set("X-Opensnack-Namespace", "ns1")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	c.SetParamNames("bucket")
	c.SetParamValues("ghost")

	_ = h.HeadBucket(c)

	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
