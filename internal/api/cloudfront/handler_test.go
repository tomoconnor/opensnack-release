// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudfront

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"opensnack/internal/resource"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]string
	reads   int
}

func (f *fakeS3) GetObject(ns, bucket, key string) (*Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	body, ok := f.objects[ns+"/"+bucket+"/"+key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &Object{Body: []byte(body)}, nil
}

func (f *fakeS3) put(key, body string) {
	f.mu.Lock()
	f.objects["default/site/"+key] = body
	f.mu.Unlock()
}

func send(h *Handler, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func mustSend(t *testing.T, h *Handler, want int, method, path, body string, hdr ...string) *node {
	t.Helper()
	rec := send(h, method, path, body, hdr...)
	if rec.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	n, err := parseXML(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return n
}

const distConfig = `<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>ref-1</CallerReference>
  <Comment>site</Comment>
  <DefaultRootObject>index.html</DefaultRootObject>
  <Enabled>true</Enabled>
  <Origins><Quantity>1</Quantity><Items>
    <Origin><Id>s3</Id><DomainName>site.s3.us-east-1.amazonaws.com</DomainName><OriginAccessControlId>OAC</OriginAccessControlId>
      <S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig></Origin>
  </Items></Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>s3</TargetOriginId><ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <AllowedMethods><Quantity>2</Quantity><Items><Method>GET</Method><Method>HEAD</Method></Items></AllowedMethods>
    <ForwardedValues><QueryString>false</QueryString><Cookies><Forward>none</Forward></Cookies></ForwardedValues>
    <MinTTL>0</MinTTL><DefaultTTL>3600</DefaultTTL><MaxTTL>86400</MaxTTL>
  </DefaultCacheBehavior>
  <CacheBehaviors><Quantity>1</Quantity><Items>
    <CacheBehavior><PathPattern>/live/*</PathPattern><TargetOriginId>s3</TargetOriginId>
      <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
      <CachePolicyId>4135ea2d-6df8-44a3-9df3-4b5a84be39ad</CachePolicyId></CacheBehavior>
  </Items></CacheBehaviors>
</DistributionConfig>`

const oacConfig = `<OriginAccessControlConfig><Name>oac</Name><Description></Description><SigningProtocol>sigv4</SigningProtocol>
<SigningBehavior>always</SigningBehavior><OriginAccessControlOriginType>s3</OriginAccessControlOriginType></OriginAccessControlConfig>`

func setup(t *testing.T) (*Handler, *fakeS3, string, string) {
	t.Helper()
	h := NewHandler(resource.NewMemStore())
	s3 := &fakeS3{objects: map[string]string{}}
	h.Objects = s3
	oac := mustSend(t, h, 201, "POST", "/2020-05-31/origin-access-control", oacConfig)
	oacID := oac.text("Id")
	cfg := strings.Replace(distConfig, "<OriginAccessControlId>OAC<", "<OriginAccessControlId>"+oacID+"<", 1)
	rec := send(h, "POST", "/2020-05-31/distribution", cfg)
	if rec.Code != 201 {
		t.Fatalf("create distribution: %d %s", rec.Code, rec.Body.String())
	}
	d, _ := parseXML(rec.Body.Bytes())
	return h, s3, d.text("Id"), rec.Header().Get("ETag")
}

func view(h *Handler, id, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "http://"+strings.ToLower(id)+".cloudfront.net"+path, nil)
	rec := httptest.NewRecorder()
	h.ServeDataPlane(rec, req)
	return rec
}

func TestDistributionRoundTrip(t *testing.T) {
	h, _, id, etag := setup(t)

	d := mustSend(t, h, 200, "GET", "/2020-05-31/distribution/"+id, "")
	if d.text("DomainName") != strings.ToLower(id)+".cloudfront.net" || d.text("Status") != "InProgress" {
		t.Errorf("distribution %s", d.String())
	}
	cfg := d.child("DistributionConfig")
	if cfg.text("Comment") != "site" || cfg.text("OriginGroups/Quantity") != "0" ||
		cfg.text("ViewerCertificate/CloudFrontDefaultCertificate") != "true" ||
		cfg.text("CacheBehaviors/Items/CacheBehavior/PathPattern") != "/live/*" {
		t.Errorf("config %s", cfg.String())
	}

	// Optimistic locking.
	mustSend(t, h, 412, "PUT", "/2020-05-31/distribution/"+id+"/config", cfg.String(), "If-Match", "stale")
	mustSend(t, h, 400, "PUT", "/2020-05-31/distribution/"+id+"/config", cfg.String())
	cfg.set("Comment", "changed")
	rec := send(h, "PUT", "/2020-05-31/distribution/"+id+"/config", cfg.String(), "If-Match", etag)
	if rec.Code != 200 || rec.Header().Get("ETag") == etag {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	etag = rec.Header().Get("ETag")
	got := mustSend(t, h, 200, "GET", "/2020-05-31/distribution/"+id+"/config", "")
	if got.text("Comment") != "changed" {
		t.Errorf("config after update: %s", got.String())
	}

	// Disable before delete, and wait for the deploy.
	mustSend(t, h, 409, "DELETE", "/2020-05-31/distribution/"+id, "", "If-Match", etag)
	got.set("Enabled", "false")
	rec = send(h, "PUT", "/2020-05-31/distribution/"+id+"/config", got.String(), "If-Match", etag)
	etag = rec.Header().Get("ETag")
	mustSend(t, h, 409, "DELETE", "/2020-05-31/distribution/"+id, "", "If-Match", etag)
	h.now = func() time.Time { return time.Now().Add(DeployDelay + time.Second) }
	if s := mustSend(t, h, 200, "GET", "/2020-05-31/distribution/"+id, "").text("Status"); s != "Deployed" {
		t.Fatalf("status %s", s)
	}
	mustSend(t, h, 204, "DELETE", "/2020-05-31/distribution/"+id, "", "If-Match", etag)
	mustSend(t, h, 404, "GET", "/2020-05-31/distribution/"+id, "")
}

func TestManagedPolicies(t *testing.T) {
	h := NewHandler(resource.NewMemStore())
	list := mustSend(t, h, 200, "GET", "/2020-05-31/cache-policy?Type=managed", "")
	found := false
	for _, s := range list.child("Items").Children {
		if s.text("CachePolicy/CachePolicyConfig/Name") == "Managed-CachingOptimized" {
			found = s.text("Type") == "managed" && s.text("CachePolicy/Id") == "658327ea-f89d-4fab-a63d-7e88639e58f6"
		}
	}
	if !found {
		t.Errorf("Managed-CachingOptimized not listed: %s", list.String())
	}
	p := mustSend(t, h, 200, "GET", "/2020-05-31/origin-request-policy/88a5eaf4-2fd4-4709-b370-b4c650ea3fcf", "")
	if p.text("OriginRequestPolicyConfig/Name") != "Managed-CORS-S3Origin" {
		t.Errorf("managed origin request policy %s", p.String())
	}
	mustSend(t, h, 400, "DELETE", "/2020-05-31/cache-policy/658327ea-f89d-4fab-a63d-7e88639e58f6", "", "If-Match", "E23ZP02F085DFQ")
}

func TestServeS3CacheAndInvalidate(t *testing.T) {
	h, s3, id, _ := setup(t)
	s3.put("index.html", "<h1>v1</h1>")
	s3.put("live/score.json", `{"n":1}`)

	rec := view(h, id, "/")
	if rec.Code != 200 || rec.Body.String() != "<h1>v1</h1>" || rec.Header().Get("X-Cache") != "Miss from cloudfront" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("first view: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	s3.put("index.html", "<h1>v2</h1>")
	rec = view(h, id, "/")
	if rec.Body.String() != "<h1>v1</h1>" || rec.Header().Get("X-Cache") != "Hit from cloudfront" || s3.reads != 1 {
		t.Fatalf("second view should be a cache hit: %q %v reads=%d", rec.Body.String(), rec.Header(), s3.reads)
	}

	// The /live/* behaviour uses Managed-CachingDisabled.
	view(h, id, "/live/score.json")
	view(h, id, "/live/score.json")
	if s3.reads != 3 {
		t.Errorf("CachingDisabled behaviour was cached: reads=%d", s3.reads)
	}

	// Missing keys look like S3 behind OAC: 403.
	if rec := view(h, id, "/nope.html"); rec.Code != 403 {
		t.Errorf("missing object: %d", rec.Code)
	}

	inv := mustSend(t, h, 201, "POST", "/2020-05-31/distribution/"+id+"/invalidation",
		`<InvalidationBatch><Paths><Quantity>1</Quantity><Items><Path>/index*</Path></Items></Paths><CallerReference>r1</CallerReference></InvalidationBatch>`)
	if inv.text("Status") != "Completed" {
		t.Errorf("invalidation %s", inv.String())
	}
	if rec := view(h, id, "/"); rec.Body.String() != "<h1>v2</h1>" || rec.Header().Get("X-Cache") != "Miss from cloudfront" {
		t.Fatalf("after invalidation: %q %v", rec.Body.String(), rec.Header())
	}
	got := mustSend(t, h, 200, "GET", "/2020-05-31/distribution/"+id+"/invalidation/"+inv.text("Id"), "")
	if got.text("InvalidationBatch/Paths/Items/Path") != "/index*" {
		t.Errorf("get invalidation %s", got.String())
	}
	if l := mustSend(t, h, 200, "GET", "/2020-05-31/distribution/"+id+"/invalidation", ""); l.text("Quantity") != "1" {
		t.Errorf("list invalidations %s", l.String())
	}

	// TTL expiry.
	h.cache.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	s3.put("index.html", "<h1>v3</h1>")
	if rec := view(h, id, "/"); rec.Body.String() != "<h1>v3</h1>" {
		t.Errorf("after TTL: %q", rec.Body.String())
	}
}

func TestBehaviourOrderAndGlob(t *testing.T) {
	for _, tt := range []struct {
		pattern, path string
		want          bool
	}{
		{"/images/*.jpg", "/images/a/b.jpg", true},
		{"images/*", "/images/x", true},
		{"/a?c", "/abc", true},
		{"/a?c", "/abbc", false},
		{"*.JPG", "/x.jpg", false},
		{"*", "/anything", true},
	} {
		if got := globMatch(tt.pattern, tt.path); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v", tt.pattern, tt.path, got)
		}
	}
}

func TestParseHost(t *testing.T) {
	t.Setenv("OPENSNACK_HOST_SUFFIXES", "localtest.me")
	for host, want := range map[string]string{
		"e2abc.cloudfront.net":             "E2ABC",
		"e2abc.cloudfront.localtest.me:80": "E2ABC",
		"cloudfront.localtest.me":          "",
		"cloudfront.amazonaws.com":         "",
		"a.b.cloudfront.net":               "",
	} {
		if got, ok := ParseHost(host); got != want || ok != (want != "") {
			t.Errorf("ParseHost(%q) = %q, %v", host, got, ok)
		}
	}
}

// An execute-api origin is served in process. The handler reads the body,
// which a GET's in-process request must still have.
func TestLoopbackOriginGet(t *testing.T) {
	h := NewHandler(resource.NewMemStore())
	h.Loopback = func(host string) http.Handler {
		if host != "abc123.execute-api.us-east-1.amazonaws.com" {
			return nil
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "%s %s body=%q", r.Method, r.URL.Path, body)
		})
	}
	cfg := strings.NewReplacer(
		`<DomainName>site.s3.us-east-1.amazonaws.com</DomainName><OriginAccessControlId>OAC</OriginAccessControlId>
      <S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig>`,
		`<DomainName>abc123.execute-api.us-east-1.amazonaws.com</DomainName>
      <CustomOriginConfig><HTTPPort>80</HTTPPort><HTTPSPort>443</HTTPSPort><OriginProtocolPolicy>https-only</OriginProtocolPolicy></CustomOriginConfig>`,
		"<DefaultRootObject>index.html</DefaultRootObject>", "",
	).Replace(distConfig)
	rec := send(h, "POST", "/2020-05-31/distribution", cfg)
	if rec.Code != 201 {
		t.Fatalf("create distribution: %d %s", rec.Code, rec.Body.String())
	}
	d, _ := parseXML(rec.Body.Bytes())
	got := view(h, d.text("Id"), "/where")
	if got.Code != 200 || got.Body.String() != `GET /where body=""` {
		t.Fatalf("loopback GET: %d %s", got.Code, got.Body.String())
	}
}
