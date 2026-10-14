// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/s3"
	"opensnack/internal/api/sqs"
	"opensnack/internal/auth"
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
	req.Header.Set("User-Agent", "opensnack-test custom-ns")
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
	req.Header.Set("User-Agent", "opensnack-test custom-ns")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRouter_DeleteBucketRoute(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)
	store.Create(&resource.Resource{ID: "dead", Namespace: "ns", Service: "s3", Type: "bucket"})

	req := httptest.NewRequest("DELETE", "/dead", nil)
	req.Header.Set("User-Agent", "opensnack-test custom-ns")
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
	req.Header.Set("User-Agent", "opensnack-test custom-ns")
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
	// Namespace isolation travels in the User-Agent (see k6/README.md):
	// Terraform cannot set custom headers, so a "custom-<ns>" suffix carries it.
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<LocationConstraint") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

// Regression: JSON-protocol services are addressed by X-Amz-Target at the root,
// which is where every AWS SDK and the CLI send them. They used to 405 there and
// were only reachable at opensnack's own /kms, /dynamodb, ... paths.
func TestRouter_JSONTargetAtRoot(t *testing.T) {
	for name, target := range map[string]string{
		"kms":            "TrentService.ListKeys",
		"dynamodb":       "DynamoDB_20120810.ListTables",
		"logs":           "Logs_20140328.DescribeLogGroups",
		"secretsmanager": "secretsmanager.ListSecrets",
		"ssm":            "AmazonSSM.DescribeParameters",
		"sqs":            "AmazonSQS.ListQueues",
	} {
		t.Run(name, func(t *testing.T) {
			e := router.New(NewMockStore())

			req := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
			req.Header.Set("X-Amz-Target", target)
			req.Header.Set("Content-Type", "application/x-amz-json-1.1")
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s: root dispatch returned 405; X-Amz-Target not routed", target)
			}
		})
	}
}

// Regression: SQS moved from the Query protocol to JSON, so AWS CLI v2 posts
// AmazonSQS.* at the root with a JSON body. That 405'd, leaving only the legacy
// Action/Version form bodies working.
func TestRouter_SQSJSONCreateQueue(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"QueueName":"jobs"}`))
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("User-Agent", "aws-cli/2.15.0 custom-ns1")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		QueueUrl string `json:"QueueUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, rec.Body.String())
	}
	if !strings.HasSuffix(out.QueueUrl, "/jobs") {
		t.Fatalf("unexpected QueueUrl %q", out.QueueUrl)
	}
	if _, err := store.Get("jobs", "sqs", "queue", "ns1@us-east-1"); err != nil {
		t.Fatalf("queue was not persisted: %v", err)
	}
}

// The legacy Query protocol has to keep working alongside the JSON one.
func TestRouter_SQSQueryCreateQueueStillWorks(t *testing.T) {
	e := router.New(NewMockStore())

	body := "Action=CreateQueue&Version=" + sqs.APIVersion + "&QueueName=legacy"
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "<CreateQueueResponse") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

// Regression: `aws s3 ls s3://bucket/` sends GET /bucket?list-type=2, which the
// bucket-level routing rejected as "not valid" with a 405.
func TestRouter_ListObjectsRoute(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)

	buf, _ := json.Marshal(s3.BucketEntry{Name: "listme", CreationDate: time.Now()})
	store.Create(&resource.Resource{
		ID:         "listme",
		Namespace:  "ns1",
		Service:    "s3",
		Type:       "bucket",
		Attributes: buf,
	})
	store.Create(&resource.Resource{
		ID:         "listme/hello.txt",
		Namespace:  "ns1",
		Service:    "s3",
		Type:       "object",
		Attributes: []byte(`{"etag":"\"abc\"","size":5,"created_at":"2026-01-02T03:04:05Z"}`),
	})

	// Both the V2 form the CLI sends and the bare V1 GET.
	for _, path := range []string{"/listme?list-type=2&delimiter=%2F&encoding-type=url", "/listme"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("User-Agent", "aws-cli/2.15.0 custom-ns1")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("%s: expected 200, got %d: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "<Key>hello.txt</Key>") {
			t.Fatalf("%s: object missing from listing: %s", path, rec.Body.String())
		}
	}
}

// ───────────────────────────────────────────────────────────
// HOST-BASED ROUTING (real AWS endpoints)
// ───────────────────────────────────────────────────────────

func hostReq(method, host, target string, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = host
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	return req
}

func TestRouter_HostRouting(t *testing.T) {
	store := NewMockStore()
	e := router.New(store)

	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	// Virtual-hosted S3: create, put and list a bucket with dots in its name.
	if rec := serve(hostReq("PUT", "my.state.bucket.s3.eu-west-1.amazonaws.com", "/", "")); rec.Code != 200 {
		t.Fatalf("CreateBucket: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(hostReq("PUT", "my.state.bucket.s3.amazonaws.com", "/env/terraform.tfstate", "{}")); rec.Code != 200 {
		t.Fatalf("PutObject: %d %s", rec.Code, rec.Body.String())
	}
	rec := serve(hostReq("GET", "my.state.bucket.s3.eu-west-1.amazonaws.com", "/?list-type=2", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<Key>env/terraform.tfstate</Key>") {
		t.Fatalf("ListObjectsV2: %d %s", rec.Code, rec.Body.String())
	}
	// Path style at the regional endpoint still works.
	if rec := serve(hostReq("GET", "s3.eu-west-1.amazonaws.com", "/my.state.bucket/env/terraform.tfstate", "")); rec.Code != 200 {
		t.Fatalf("GetObject path style: %d %s", rec.Code, rec.Body.String())
	}

	// Lambda and Route 53 at their real paths used to fall through to S3.
	rec = serve(hostReq("GET", "lambda.us-east-1.amazonaws.com", "/2015-03-31/functions/missing", ""))
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "NoSuchBucket") {
		t.Fatalf("Lambda GetFunction: %d %s", rec.Code, rec.Body.String())
	}
	rec = serve(hostReq("GET", "route53.amazonaws.com", "/2013-04-01/hostedzone", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ListHostedZonesResponse") {
		t.Fatalf("Route53 ListHostedZones: %d %s", rec.Code, rec.Body.String())
	}

	// EC2 and ElastiCache at their own hosts, with Query bodies at "/".
	req := hostReq("POST", "ec2.us-east-1.amazonaws.com", "/", "Action=DescribeVpcs&Version=2016-11-15")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := serve(req); rec.Code != 200 || !strings.Contains(rec.Body.String(), "DescribeVpcsResponse") {
		t.Fatalf("EC2 DescribeVpcs: %d %s", rec.Code, rec.Body.String())
	}

	// SQS posting to a queue URL path (what Query-protocol clients do).
	req = hostReq("POST", "sqs.us-east-1.amazonaws.com", "/", "Action=CreateQueue&QueueName=jobs&Version=2012-11-05")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = serve(req)
	if !strings.Contains(rec.Body.String(), "<QueueUrl>https://sqs.us-east-1.amazonaws.com/000000000000/jobs</QueueUrl>") {
		t.Fatalf("SQS CreateQueue: %d %s", rec.Code, rec.Body.String())
	}
	req = hostReq("POST", "sqs.us-east-1.amazonaws.com", "/000000000000/jobs", "Action=ListQueues&Version=2012-11-05")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := serve(req); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ListQueuesResponse") {
		t.Fatalf("SQS at queue URL: %d %s", rec.Code, rec.Body.String())
	}

	// S3 Control at its account host.
	rec = serve(hostReq("GET", "000000000000.s3-control.us-east-1.amazonaws.com",
		"/v20180820/tags/arn%3Aaws%3As3%3A%3A%3Amy.state.bucket", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ListTagsForResourceResult") {
		t.Fatalf("S3 Control ListTagsForResource: %d %s", rec.Code, rec.Body.String())
	}

	// A service opensnack doesn't have says so rather than pretending to be S3.
	rec = serve(hostReq("POST", "kinesis.us-east-1.amazonaws.com", "/", "{}"))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "UnsupportedService") {
		t.Fatalf("unsupported service: %d %s", rec.Code, rec.Body.String())
	}
}

// Path-style requests to localhost route by the SigV4 signing service.
func TestRouter_SigningServiceRouting(t *testing.T) {
	e := router.New(NewMockStore())
	req := httptest.NewRequest("GET", "/2013-04-01/hostedzone", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261201/us-east-1/route53/aws4_request, SignedHeaders=host;x-amz-date, Signature=abcd")
	req.Header.Set("X-Amz-Date", "20261201T000000Z")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ListHostedZonesResponse") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// EC2 and ElastiCache Query calls at the root are dispatched on Version.
func TestRouter_QueryDispatchEC2(t *testing.T) {
	e := router.New(NewMockStore())
	req := httptest.NewRequest("POST", "/", strings.NewReader("Action=DescribeVpcs&Version=2016-11-15"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DescribeVpcsResponse") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRouter_DataPlanesSkipAuth(t *testing.T) {
	store := resource.NewMemStore()
	authn := &auth.Authenticator{Mode: auth.ModeStrict, Source: auth.NewTableSource(nil), Now: time.Now}
	e := router.NewWithConfig(store, router.Config{Auth: authn})
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	// Unsigned control-plane calls are refused in strict mode...
	if rec := serve(hostReq("GET", "apigateway.us-east-1.amazonaws.com", "/v2/apis", "")); rec.Code < 400 {
		t.Errorf("unsigned control plane: %d %s", rec.Code, rec.Body.String())
	}
	// ...but the anonymous data planes reach their handlers.
	rec := serve(hostReq("GET", "abc123.execute-api.us-east-1.amazonaws.com", "/prod/x", ""))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"Not Found"`) {
		t.Errorf("execute-api: %d %s", rec.Code, rec.Body.String())
	}
	rec = serve(hostReq("GET", "e2abc.cloudfront.net", "/", ""))
	if rec.Code != http.StatusNotFound || rec.Header().Get("X-Cache") == "" {
		t.Errorf("cloudfront.net: %d %v", rec.Code, rec.Header())
	}
}
