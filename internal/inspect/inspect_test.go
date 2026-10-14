// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package inspect_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensnack/internal/auth"
	"opensnack/internal/inspect"
	"opensnack/internal/resource"
	"opensnack/internal/router"
)

func TestInspectionAPI(t *testing.T) {
	t.Setenv("OPENSNACK_OBJECT_ROOT", t.TempDir())
	store := resource.NewMemStore()
	h := router.NewWithConfig(store, router.Config{
		Auth:          &auth.Authenticator{Mode: auth.ModeDev, Now: time.Now},
		InternalToken: "checker-token",
	})

	// Tenant traffic, as the AWS API sees it.
	aws := func(method, host, path, body string, hdr ...string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = host
		req.Header.Set("User-Agent", "test custom-tenant1")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			t.Fatalf("%s %s%s: %d %s", method, host, path, rec.Code, rec.Body.String())
		}
	}
	json10 := []string{"Content-Type", "application/x-amz-json-1.0"}
	aws("POST", "sqs.us-east-1.amazonaws.com", "/", `{"QueueName":"orders"}`, append(json10, "X-Amz-Target", "AmazonSQS.CreateQueue")...)
	aws("POST", "sqs.eu-west-1.amazonaws.com", "/", `{"QueueName":"orders"}`, append(json10, "X-Amz-Target", "AmazonSQS.CreateQueue")...)
	aws("PUT", "assets.s3.amazonaws.com", "/", "")
	aws("PUT", "assets.s3.amazonaws.com", "/list.txt", "widget-a, widget-b")

	call := func(method, path, token string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, _ := call("GET", "/_opensnack/v1/namespaces/tenant1", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := call("GET", "/_opensnack/v1/namespaces/tenant1", "wrong"); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}

	code, out := call("GET", "/_opensnack/v1/namespaces/tenant1", "checker-token")
	if code != 200 || out["total"].(float64) != 4 {
		t.Fatalf("count: %d %v", code, out)
	}

	_, out = call("GET", "/_opensnack/v1/namespaces/tenant1/resources?service=sqs&region=eu-west-1", "checker-token")
	items := out["resources"].([]any)
	if len(items) != 1 {
		t.Fatalf("region filter: %v", out)
	}
	q := items[0].(map[string]any)
	if q["region"] != "eu-west-1" || q["id"] != "orders" || q["created_at"] == "" || q["attributes"] == nil {
		t.Fatalf("item: %v", q)
	}

	// The same id in two regions needs ?region= to pick one.
	if code, _ := call("GET", "/_opensnack/v1/namespaces/tenant1/resources/sqs/queue/orders", "checker-token"); code != 409 {
		t.Fatalf("ambiguous get: %d", code)
	}
	code, one := call("GET", "/_opensnack/v1/namespaces/tenant1/resources/sqs/queue/orders?region=us-east-1", "checker-token")
	if code != 200 || one["region"] != "us-east-1" {
		t.Fatalf("get: %d %v", code, one)
	}
	created := one["created_at"]

	// An in-place update moves updated_at but not created_at.
	time.Sleep(5 * time.Millisecond)
	aws("POST", "sqs.us-east-1.amazonaws.com", "/",
		`{"QueueUrl":"https://sqs.us-east-1.amazonaws.com/000000000000/orders","Attributes":{"VisibilityTimeout":"60"}}`,
		append(json10, "X-Amz-Target", "AmazonSQS.SetQueueAttributes")...)
	_, after := call("GET", "/_opensnack/v1/namespaces/tenant1/resources/sqs/queue/orders?region=us-east-1", "checker-token")
	if after["created_at"] != created || after["updated_at"] == created {
		t.Fatalf("timestamps after update: created %v→%v updated %v", created, after["created_at"], after["updated_at"])
	}

	// Global resources have region "".
	_, out = call("GET", "/_opensnack/v1/namespaces/tenant1/resources?service=s3&type=bucket&region=", "checker-token")
	if n := len(out["resources"].([]any)); n != 1 {
		t.Fatalf("global filter: %v", out)
	}

	// Object bytes.
	req := httptest.NewRequest("GET", "/_opensnack/v1/namespaces/tenant1/s3/assets/list.txt", nil)
	req.Header.Set("Authorization", "Bearer checker-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "widget-a, widget-b" {
		t.Fatalf("object: %d %s", rec.Code, rec.Body.String())
	}

	// Delete the namespace: rows and object bytes go.
	code, out = call("DELETE", "/_opensnack/v1/namespaces/tenant1", "checker-token")
	if code != 200 || out["deleted"].(float64) != 4 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if _, out = call("GET", "/_opensnack/v1/namespaces/tenant1", "checker-token"); out["total"].(float64) != 0 {
		t.Fatalf("after delete: %v", out)
	}
	req = httptest.NewRequest("GET", "/_opensnack/v1/namespaces/tenant1/s3/assets/list.txt", nil)
	req.Header.Set("Authorization", "Bearer checker-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("object after delete: %d", rec.Code)
	}
}

// Strict mode without a token doesn't expose the API at all.
func TestInspectionAPIOffWithoutToken(t *testing.T) {
	h := router.NewWithConfig(resource.NewMemStore(), router.Config{
		Auth: &auth.Authenticator{Mode: auth.ModeStrict, Source: auth.NewTableSource(nil), Now: time.Now},
	})
	req := httptest.NewRequest("GET", "/_opensnack/v1/namespaces", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("got %d", rec.Code)
	}
	_ = inspect.Prefix
}
