// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package limits_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/limits"
	"opensnack/internal/resource"
	"opensnack/internal/router"
)

type client struct {
	t *testing.T
	h http.Handler
}

func (c client) do(method, host, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("User-Agent", "test custom-p1")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func sqsJSON(op, body string) []string {
	return []string{"X-Amz-Target", "AmazonSQS." + op, "Content-Type", "application/x-amz-json-1.0"}
}

func TestResourceCapBlocksCreatesOnly(t *testing.T) {
	c := client{t, router.NewWithConfig(resource.NewMemStore(), router.Config{Limits: limits.Config{MaxResources: 2}})}

	for _, q := range []string{"a", "b"} {
		if rec := c.do("POST", "sqs.us-east-1.amazonaws.com", "/", `{"QueueName":"`+q+`"}`, sqsJSON("CreateQueue", "")...); rec.Code != 200 {
			t.Fatalf("create %s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	rec := c.do("POST", "sqs.us-east-1.amazonaws.com", "/", `{"QueueName":"c"}`, sqsJSON("CreateQueue", "")...)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "LimitExceededException") {
		t.Fatalf("third create: %d %s", rec.Code, rec.Body.String())
	}

	// Query protocol, same cap, its own error code.
	rec = c.do("POST", "sqs.us-east-1.amazonaws.com", "/", "Action=CreateQueue&QueueName=d&Version=2012-11-05",
		"Content-Type", "application/x-www-form-urlencoded")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "<Code>LimitExceeded</Code>") {
		t.Fatalf("query create: %d %s", rec.Code, rec.Body.String())
	}

	// S3 bucket creation at the cap is TooManyBuckets.
	rec = c.do("PUT", "assets.s3.amazonaws.com", "/", "")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "TooManyBuckets") {
		t.Fatalf("bucket at cap: %d %s", rec.Code, rec.Body.String())
	}

	// Reads and deletes still work at the cap.
	if rec := c.do("POST", "sqs.us-east-1.amazonaws.com", "/", `{}`, sqsJSON("ListQueues", "")...); rec.Code != 200 {
		t.Fatalf("list at cap: %d", rec.Code)
	}
	rec = c.do("POST", "sqs.us-east-1.amazonaws.com", "/",
		`{"QueueUrl":"https://sqs.us-east-1.amazonaws.com/000000000000/a"}`, sqsJSON("DeleteQueue", "")...)
	if rec.Code != 200 {
		t.Fatalf("delete at cap: %d %s", rec.Code, rec.Body.String())
	}

	// Below the cap again, creating works.
	if rec := c.do("PUT", "assets.s3.amazonaws.com", "/", ""); rec.Code != 200 {
		t.Fatalf("create after delete: %d %s", rec.Code, rec.Body.String())
	}

}

func TestSizeCaps(t *testing.T) {
	t.Setenv("OPENSNACK_OBJECT_ROOT", t.TempDir())
	c := client{t, router.NewWithConfig(resource.NewMemStore(), router.Config{Limits: limits.Config{
		MaxRequestBytes: 100, MaxObjectBytes: 1000, MaxS3Bytes: 1500,
	}})}

	big := strings.Repeat("x", 200)
	rec := c.do("POST", "sqs.us-east-1.amazonaws.com", "/", `{"QueueName":"`+big+`"}`, sqsJSON("CreateQueue", "")...)
	if rec.Code != 413 {
		t.Fatalf("oversized request: %d %s", rec.Code, rec.Body.String())
	}

	if rec := c.do("PUT", "b.s3.amazonaws.com", "/", ""); rec.Code != 200 {
		t.Fatalf("bucket: %d", rec.Code)
	}
	// Objects get the larger object cap, not the request cap.
	if rec := c.do("PUT", "b.s3.amazonaws.com", "/one", strings.Repeat("y", 900)); rec.Code != 200 {
		t.Fatalf("900-byte object: %d %s", rec.Code, rec.Body.String())
	}
	rec = c.do("PUT", "b.s3.amazonaws.com", "/huge", strings.Repeat("y", 1001))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "EntityTooLarge") {
		t.Fatalf("oversized object: %d %s", rec.Code, rec.Body.String())
	}
	// 900 stored + 700 more would pass the 1500-byte quota.
	rec = c.do("PUT", "b.s3.amazonaws.com", "/two", strings.Repeat("z", 700))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "QuotaExceeded") {
		t.Fatalf("over quota: %d %s", rec.Code, rec.Body.String())
	}
}

func TestThrottling(t *testing.T) {
	c := client{t, router.NewWithConfig(resource.NewMemStore(), router.Config{Limits: limits.Config{RatePerSecond: 1, Burst: 3}})}
	codes := map[int]int{}
	for range 5 {
		codes[c.do("POST", "sqs.us-east-1.amazonaws.com", "/", `{}`, sqsJSON("ListQueues", "")...).Code]++
	}
	if codes[200] != 3 || codes[400] != 2 {
		t.Fatalf("expected 3 allowed and 2 throttled, got %v", codes)
	}
	rec := c.do("GET", "b.s3.amazonaws.com", "/", "")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "SlowDown") {
		t.Fatalf("S3 throttle: %d %s", rec.Code, rec.Body.String())
	}

	// Another namespace has its own bucket.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("User-Agent", "test custom-p2")
	rec = httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code == 503 {
		t.Fatalf("p2 throttled by p1's traffic")
	}
}
