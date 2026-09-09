// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"opensnack/internal/api/s3"
	"opensnack/internal/resource"
)

func TestCreateBucket_AlreadyExists(t *testing.T) {
	store := NewMockStore()
	h := s3.NewHandler(store)

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
	// Namespace isolation travels in the User-Agent (see k6/README.md):
	// Terraform cannot set custom headers, so a "custom-<ns>" suffix carries it.
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()

	h.CreateBucket(rec, req)

	if rec.Code != 409 {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}

func TestHeadBucket_NotExists(t *testing.T) {
	store := NewMockStore()
	h := s3.NewHandler(store)

	req := httptest.NewRequest("HEAD", "/ghost", nil)
	// Namespace isolation travels in the User-Agent (see k6/README.md):
	// Terraform cannot set custom headers, so a "custom-<ns>" suffix carries it.
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()

	h.HeadBucket(rec, req)

	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// Regression: these handlers wrote a 404 but carried on to dereference the nil
// resource, panicking the connection instead of returning NoSuchBucket.
func TestMissingBucketDoesNotPanic(t *testing.T) {
	for name, tc := range map[string]struct {
		method string
		path   string
	}{
		"GetBucketVersioning":      {"GET", "/ghost?versioning"},
		"GetBucketLifecycleConfig": {"GET", "/ghost?lifecycle"},
		"GetBucketAcl":             {"GET", "/ghost?acl"},
		"PutBucketLifecycleConfig": {"PUT", "/ghost?lifecycle"},
	} {
		t.Run(name, func(t *testing.T) {
			h := s3.NewHandler(NewMockStore())

			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("User-Agent", "opensnack-test custom-ns1")
			rec := httptest.NewRecorder()

			switch name {
			case "GetBucketVersioning":
				h.GetBucketVersioning(rec, req)
			case "GetBucketLifecycleConfig":
				h.GetBucketLifecycleConfiguration(rec, req)
			case "GetBucketAcl":
				h.GetBucketAcl(rec, req)
			case "PutBucketLifecycleConfig":
				h.PutBucketLifecycleConfiguration(rec, req)
			}

			if rec.Code == 200 {
				t.Fatalf("expected an error status, got 200: %s", rec.Body.String())
			}
		})
	}
}

// Regression: the no-body branch of CreateBucket wrote its 200 and then fell
// through into XML mode, creating the bucket a second time and appending a 500
// plus an XML body to the committed response. The AWS CLI saw an unparseable
// 200, retried, and reported BucketAlreadyExists for a brand-new bucket.
func TestCreateBucket_NoBodyWritesCleanResponse(t *testing.T) {
	store := NewMockStore()
	h := s3.NewHandler(store)

	req := httptest.NewRequest("PUT", "/fresh", nil)
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()

	h.CreateBucket(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty body, got %d bytes: %q", rec.Body.Len(), rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/fresh" {
		t.Fatalf("expected Location /fresh, got %q", got)
	}
	if buckets, _ := store.List("s3", "bucket", "ns1"); len(buckets) != 1 {
		t.Fatalf("expected exactly 1 bucket created, got %d", len(buckets))
	}
}
