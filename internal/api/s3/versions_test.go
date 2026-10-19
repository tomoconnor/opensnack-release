// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"opensnack/internal/api/s3"
	"opensnack/internal/api/s3control"
	"opensnack/internal/resource"
)

type s3env struct {
	t     *testing.T
	store *resource.MemStore
	h     *s3.Handler
}

func newEnv(t *testing.T) *s3env {
	tempObjectRoot(t)
	store := resource.NewMemStore()
	return &s3env{t: t, store: store, h: s3.NewHandler(store)}
}

func (e *s3env) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.Dispatch(rec, req)
	return rec
}

func (e *s3env) must(code int, rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	e.t.Helper()
	if rec.Code != code {
		e.t.Fatalf("expected %d, got %d: %s", code, rec.Code, rec.Body.String())
	}
	return rec
}

func (e *s3env) versioned(bucket, status string) {
	e.must(200, e.do("PUT", "/"+bucket, ""))
	e.must(200, e.do("PUT", "/"+bucket+"?versioning",
		`<VersioningConfiguration><Status>`+status+`</Status></VersioningConfiguration>`))
}

type listVersions struct {
	Versions []struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
		IsLatest  bool   `xml:"IsLatest"`
	} `xml:"Version"`
	DeleteMarkers []struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
		IsLatest  bool   `xml:"IsLatest"`
	} `xml:"DeleteMarker"`
	IsTruncated bool `xml:"IsTruncated"`
}

func (e *s3env) versions(bucket string) listVersions {
	e.t.Helper()
	var out listVersions
	rec := e.must(200, e.do("GET", "/"+bucket+"?versions", ""))
	if err := xml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("bad ListVersionsResult: %v", err)
	}
	return out
}

func TestVersioningKeepsOldVersions(t *testing.T) {
	e := newEnv(t)
	e.versioned("vb", "Enabled")

	v1 := e.must(200, e.do("PUT", "/vb/k", "one")).Header().Get("X-Amz-Version-Id")
	v2 := e.must(200, e.do("PUT", "/vb/k", "two")).Header().Get("X-Amz-Version-Id")
	if v1 == "" || v2 == "" || v1 == v2 {
		t.Fatalf("version IDs %q %q", v1, v2)
	}

	got := e.must(200, e.do("GET", "/vb/k", ""))
	if got.Body.String() != "two" || got.Header().Get("X-Amz-Version-Id") != v2 {
		t.Fatalf("current: %q %q", got.Body.String(), got.Header().Get("X-Amz-Version-Id"))
	}
	if b := e.must(200, e.do("GET", "/vb/k?versionId="+v1, "")).Body.String(); b != "one" {
		t.Fatalf("old version body %q", b)
	}
	if h := e.must(200, e.do("HEAD", "/vb/k?versionId="+v1, "")).Header().Get("X-Amz-Version-Id"); h != v1 {
		t.Fatalf("HEAD version %q", h)
	}
	e.must(404, e.do("GET", "/vb/k?versionId=nope", ""))

	lv := e.versions("vb")
	if len(lv.Versions) != 2 || lv.Versions[0].VersionID != v2 || !lv.Versions[0].IsLatest || lv.Versions[1].IsLatest {
		t.Fatalf("versions: %+v", lv)
	}

	// Deleting the current version by ID makes the previous one current.
	e.must(204, e.do("DELETE", "/vb/k?versionId="+v2, ""))
	if b := e.must(200, e.do("GET", "/vb/k", "")).Body.String(); b != "one" {
		t.Fatalf("after deleting v2, current is %q", b)
	}
}

func TestDeleteMarkers(t *testing.T) {
	e := newEnv(t)
	e.versioned("vb", "Enabled")
	v1 := e.must(200, e.do("PUT", "/vb/k", "one")).Header().Get("X-Amz-Version-Id")

	del := e.must(204, e.do("DELETE", "/vb/k", ""))
	marker := del.Header().Get("X-Amz-Version-Id")
	if del.Header().Get("X-Amz-Delete-Marker") != "true" || marker == "" {
		t.Fatalf("delete did not create a marker: %v", del.Header())
	}

	get := e.must(404, e.do("GET", "/vb/k", ""))
	if get.Header().Get("X-Amz-Delete-Marker") != "true" {
		t.Fatal("GET of deleted key lacks x-amz-delete-marker")
	}
	e.must(405, e.do("GET", "/vb/k?versionId="+marker, ""))
	e.must(200, e.do("GET", "/vb/k?versionId="+v1, ""))

	// The key is gone from listings but its versions remain, and the bucket
	// can't be deleted.
	if strings.Contains(e.must(200, e.do("GET", "/vb?list-type=2", "")).Body.String(), "<Key>k</Key>") {
		t.Fatal("deleted key still listed")
	}
	lv := e.versions("vb")
	if len(lv.DeleteMarkers) != 1 || !lv.DeleteMarkers[0].IsLatest || len(lv.Versions) != 1 || lv.Versions[0].IsLatest {
		t.Fatalf("versions: %+v", lv)
	}
	e.must(409, e.do("DELETE", "/vb", ""))

	// Removing the marker brings the object back.
	rm := e.must(204, e.do("DELETE", "/vb/k?versionId="+marker, ""))
	if rm.Header().Get("X-Amz-Delete-Marker") != "true" {
		t.Fatal("deleting a marker should say so")
	}
	if b := e.must(200, e.do("GET", "/vb/k", "")).Body.String(); b != "one" {
		t.Fatalf("restored body %q", b)
	}
}

func TestSuspendedVersioningNullVersion(t *testing.T) {
	e := newEnv(t)
	e.must(200, e.do("PUT", "/sb", ""))
	e.must(200, e.do("PUT", "/sb/k", "pre")) // unversioned: the null version

	e.must(200, e.do("PUT", "/sb?versioning", `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`))
	v1 := e.must(200, e.do("PUT", "/sb/k", "v1")).Header().Get("X-Amz-Version-Id")

	e.must(200, e.do("PUT", "/sb?versioning", `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`))
	if v := e.must(200, e.do("PUT", "/sb/k", "s1")).Header().Get("X-Amz-Version-Id"); v != "null" {
		t.Fatalf("suspended PUT version %q", v)
	}
	e.must(200, e.do("PUT", "/sb/k", "s2"))

	// The pre-versioning object and the first suspended write were both the
	// null version, so each write replaced it; v1 survives.
	lv := e.versions("sb")
	ids := []string{}
	for _, v := range lv.Versions {
		ids = append(ids, v.VersionID)
	}
	if strings.Join(ids, ",") != "null,"+v1 {
		t.Fatalf("versions %v", ids)
	}
	if b := e.must(200, e.do("GET", "/sb/k?versionId=null", "")).Body.String(); b != "s2" {
		t.Fatalf("null version body %q", b)
	}

	// A suspended delete puts a null delete marker in place of the null version.
	del := e.must(204, e.do("DELETE", "/sb/k", ""))
	if del.Header().Get("X-Amz-Version-Id") != "null" || del.Header().Get("X-Amz-Delete-Marker") != "true" {
		t.Fatalf("suspended delete headers %v", del.Header())
	}
	lv = e.versions("sb")
	if len(lv.Versions) != 1 || lv.Versions[0].VersionID != v1 || len(lv.DeleteMarkers) != 1 {
		t.Fatalf("after suspended delete: %+v", lv)
	}
}

func TestCopyObject(t *testing.T) {
	e := newEnv(t)
	e.must(200, e.do("PUT", "/cb", ""))
	e.must(200, e.do("PUT", "/cb/src", "payload",
		"Content-Type", "text/plain", "X-Amz-Meta-Colour", "blue", "X-Amz-Tagging", "a=1"))

	rec := e.must(200, e.do("PUT", "/cb/dst", "", "X-Amz-Copy-Source", "/cb/src"))
	if !strings.Contains(rec.Body.String(), "<CopyObjectResult") {
		t.Fatalf("copy body %s", rec.Body.String())
	}
	got := e.must(200, e.do("GET", "/cb/dst", ""))
	if got.Body.String() != "payload" || got.Header().Get("Content-Type") != "text/plain" ||
		got.Header().Get("X-Amz-Meta-Colour") != "blue" || got.Header().Get("X-Amz-Tagging-Count") != "1" {
		t.Fatalf("copied object: %q %v", got.Body.String(), got.Header())
	}

	// REPLACE takes the request's metadata and tags instead.
	e.must(200, e.do("PUT", "/cb/dst2", "", "X-Amz-Copy-Source", "cb%2Fsrc",
		"X-Amz-Metadata-Directive", "REPLACE", "Content-Type", "application/json",
		"X-Amz-Tagging-Directive", "REPLACE", "X-Amz-Tagging", "b=2&c=3"))
	got = e.must(200, e.do("HEAD", "/cb/dst2", ""))
	if got.Header().Get("Content-Type") != "application/json" || got.Header().Get("X-Amz-Meta-Colour") != "" ||
		got.Header().Get("X-Amz-Tagging-Count") != "2" {
		t.Fatalf("replaced metadata: %v", got.Header())
	}

	// Copying onto itself without changing anything is refused.
	e.must(400, e.do("PUT", "/cb/src", "", "X-Amz-Copy-Source", "/cb/src"))
	e.must(404, e.do("PUT", "/cb/dst3", "", "X-Amz-Copy-Source", "/cb/missing"))
}

func TestCopyRestoresOlderVersion(t *testing.T) {
	e := newEnv(t)
	e.versioned("rv", "Enabled")
	first := e.must(200, e.do("PUT", "/rv/k", "one")).Header().Get("X-Amz-Version-Id")
	current := e.must(200, e.do("PUT", "/rv/k", "two")).Header().Get("X-Amz-Version-Id")

	// Copying the current version onto itself is still refused...
	e.must(400, e.do("PUT", "/rv/k", "", "X-Amz-Copy-Source", "/rv/k?versionId="+current))
	// ...but copying an older one restores it as a new current version.
	rec := e.must(200, e.do("PUT", "/rv/k", "", "X-Amz-Copy-Source", "/rv/k?versionId="+first))
	if rec.Header().Get("X-Amz-Copy-Source-Version-Id") != first {
		t.Fatalf("copy source version %v", rec.Header())
	}
	if got := e.must(200, e.do("GET", "/rv/k", "")).Body.String(); got != "one" {
		t.Fatalf("restored body %q", got)
	}
	if lv := e.versions("rv"); len(lv.Versions) != 3 {
		t.Fatalf("want 3 versions, got %d", len(lv.Versions))
	}
}

// The AWS provider's aws_s3_object_copy query-escapes the whole source,
// version and all: bucket%2Fkey%3FversionId%3D...
func TestCopyRestoresEncodedVersion(t *testing.T) {
	e := newEnv(t)
	e.versioned("ev", "Enabled")
	first := e.must(200, e.do("PUT", "/ev/k", "one")).Header().Get("X-Amz-Version-Id")
	e.must(200, e.do("PUT", "/ev/k", "two"))

	rec := e.must(200, e.do("PUT", "/ev/k", "", "X-Amz-Copy-Source", url.QueryEscape("ev/k?versionId="+first)))
	if rec.Header().Get("X-Amz-Copy-Source-Version-Id") != first {
		t.Fatalf("copy source version %v", rec.Header())
	}
	if got := e.must(200, e.do("GET", "/ev/k", "")).Body.String(); got != "one" {
		t.Fatalf("restored body %q", got)
	}
}

func TestDeleteObjectsEmptiesVersionedBucket(t *testing.T) {
	e := newEnv(t)
	e.versioned("db", "Enabled")
	for _, k := range []string{"a", "b", "c/d"} {
		e.must(200, e.do("PUT", "/db/"+k, "1"))
		e.must(200, e.do("PUT", "/db/"+k, "2"))
	}
	e.must(204, e.do("DELETE", "/db/a", ""))

	// What force_destroy does: list every version and delete them in a batch.
	lv := e.versions("db")
	var body strings.Builder
	body.WriteString("<Delete><Quiet>true</Quiet>")
	for _, v := range lv.Versions {
		body.WriteString("<Object><Key>" + v.Key + "</Key><VersionId>" + v.VersionID + "</VersionId></Object>")
	}
	for _, m := range lv.DeleteMarkers {
		body.WriteString("<Object><Key>" + m.Key + "</Key><VersionId>" + m.VersionID + "</VersionId></Object>")
	}
	body.WriteString("</Delete>")
	if len(lv.Versions) != 6 || len(lv.DeleteMarkers) != 1 {
		t.Fatalf("versions before delete: %+v", lv)
	}

	rec := e.must(200, e.do("POST", "/db?delete", body.String()))
	if strings.Contains(rec.Body.String(), "<Error>") {
		t.Fatalf("DeleteObjects errors: %s", rec.Body.String())
	}
	if lv := e.versions("db"); len(lv.Versions)+len(lv.DeleteMarkers) != 0 {
		t.Fatalf("left behind: %+v", lv)
	}
	e.must(204, e.do("DELETE", "/db", ""))

	// Without version IDs, a versioned bucket gets delete markers back.
	e.versioned("db2", "Enabled")
	e.must(200, e.do("PUT", "/db2/x", "1"))
	rec = e.must(200, e.do("POST", "/db2?delete", "<Delete><Object><Key>x</Key></Object></Delete>"))
	if !strings.Contains(rec.Body.String(), "<DeleteMarker>true</DeleteMarker>") {
		t.Fatalf("expected a delete marker: %s", rec.Body.String())
	}
}

func TestBucketTagging(t *testing.T) {
	e := newEnv(t)
	e.must(200, e.do("PUT", "/tb", ""))
	if rec := e.must(404, e.do("GET", "/tb?tagging", "")); !strings.Contains(rec.Body.String(), "NoSuchTagSet") {
		t.Fatalf("untagged bucket: %s", rec.Body.String())
	}
	e.must(204, e.do("PUT", "/tb?tagging",
		`<Tagging><TagSet><Tag><Key>z</Key><Value>1</Value></Tag><Tag><Key>a</Key><Value>2</Value></Tag></TagSet></Tagging>`))
	body := e.must(200, e.do("GET", "/tb?tagging", "")).Body.String()
	if strings.Index(body, "<Key>a</Key>") > strings.Index(body, "<Key>z</Key>") {
		t.Fatalf("tags not sorted: %s", body)
	}
	e.must(400, e.do("PUT", "/tb?tagging",
		`<Tagging><TagSet><Tag><Key>a</Key><Value>1</Value></Tag><Tag><Key>a</Key><Value>2</Value></Tag></TagSet></Tagging>`))

	// S3 Control sees and edits the same tags.
	ctl := s3control.NewHandler(e.store)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("User-Agent", "opensnack-test custom-ns1")
		rec := httptest.NewRecorder()
		ctl.Tags(rec, req)
		return rec
	}
	arn := "/v20180820/tags/arn%3Aaws%3As3%3A%3A%3Atb"
	if rec := call("GET", arn, ""); !strings.Contains(rec.Body.String(), "<Key>z</Key>") {
		t.Fatalf("ListTagsForResource: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", arn, `<TagResourceRequest><Tags><Tag><Key>m</Key><Value>3</Value></Tag></Tags></TagResourceRequest>`); rec.Code != http.StatusNoContent {
		t.Fatalf("TagResource: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("DELETE", arn+"?tagKeys=z&tagKeys=a", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("UntagResource: %d", rec.Code)
	}
	body = e.must(200, e.do("GET", "/tb?tagging", "")).Body.String()
	if !strings.Contains(body, "<Key>m</Key>") || strings.Contains(body, "<Key>z</Key>") {
		t.Fatalf("after control edits: %s", body)
	}
	if rec := call("GET", "/v20180820/tags/arn%3Aaws%3As3%3A%3A%3Amissing", ""); rec.Code != 404 {
		t.Fatalf("missing bucket: %d", rec.Code)
	}

	e.must(204, e.do("DELETE", "/tb?tagging", ""))
	e.must(404, e.do("GET", "/tb?tagging", ""))
}

func TestObjectTagging(t *testing.T) {
	e := newEnv(t)
	e.versioned("ob", "Enabled")
	v1 := e.must(200, e.do("PUT", "/ob/k", "1", "X-Amz-Tagging", "a=1&b=two%20words")).Header().Get("X-Amz-Version-Id")
	body := e.must(200, e.do("GET", "/ob/k?tagging", "")).Body.String()
	if !strings.Contains(body, "<Value>two words</Value>") {
		t.Fatalf("header tags: %s", body)
	}

	e.must(200, e.do("PUT", "/ob/k", "2"))
	e.must(200, e.do("PUT", "/ob/k?tagging",
		`<Tagging><TagSet><Tag><Key>new</Key><Value>x</Value></Tag></TagSet></Tagging>`))
	if body := e.must(200, e.do("GET", "/ob/k?tagging", "")).Body.String(); !strings.Contains(body, "<Key>new</Key>") {
		t.Fatalf("current version tags: %s", body)
	}
	// The old version keeps its own tags.
	if body := e.must(200, e.do("GET", "/ob/k?tagging&versionId="+v1, "")).Body.String(); !strings.Contains(body, "<Key>a</Key>") {
		t.Fatalf("old version tags: %s", body)
	}

	e.must(204, e.do("DELETE", "/ob/k?tagging", ""))
	if body := e.must(200, e.do("GET", "/ob/k?tagging", "")).Body.String(); strings.Contains(body, "<Tag>") {
		t.Fatalf("tags after delete: %s", body)
	}
	e.must(400, e.do("PUT", "/ob/k2", "x", "X-Amz-Tagging", "a=1&a=2"))
}

func TestBucketConfigNotFoundErrors(t *testing.T) {
	e := newEnv(t)
	e.must(200, e.do("PUT", "/nb", ""))
	for sub, code := range map[string]string{
		"tagging":           "NoSuchTagSet",
		"cors":              "NoSuchCORSConfiguration",
		"website":           "NoSuchWebsiteConfiguration",
		"publicAccessBlock": "NoSuchPublicAccessBlockConfiguration",
		"ownershipControls": "OwnershipControlsNotFoundError",
		"policy":            "NoSuchBucketPolicy",
		"replication":       "ReplicationConfigurationNotFoundError",
		"object-lock":       "ObjectLockConfigurationNotFoundError",
		"lifecycle":         "NoSuchLifecycleConfiguration",
	} {
		rec := e.must(404, e.do("GET", "/nb?"+sub, ""))
		if !strings.Contains(rec.Body.String(), "<Code>"+code+"</Code>") {
			t.Errorf("?%s: %s", sub, rec.Body.String())
		}
	}
	// Every bucket is encrypted with SSE-S3 unless configured otherwise.
	if body := e.must(200, e.do("GET", "/nb?encryption", "")).Body.String(); !strings.Contains(body, "AES256") {
		t.Errorf("default encryption: %s", body)
	}
	if body := e.must(200, e.do("GET", "/nb?versioning", "")).Body.String(); strings.Contains(body, "<Status>") {
		t.Errorf("unversioned bucket reports a status: %s", body)
	}
}

func TestBucketEncryptionAppliesToObjects(t *testing.T) {
	e := newEnv(t)
	e.must(200, e.do("PUT", "/eb", ""))
	if h := e.must(200, e.do("PUT", "/eb/a", "x")).Header().Get("X-Amz-Server-Side-Encryption"); h != "AES256" {
		t.Fatalf("default SSE header %q", h)
	}
	e.must(200, e.do("PUT", "/eb?encryption", `<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>aws:kms</SSEAlgorithm><KMSMasterKeyID>1234abcd</KMSMasterKeyID></ApplyServerSideEncryptionByDefault><BucketKeyEnabled>true</BucketKeyEnabled></Rule></ServerSideEncryptionConfiguration>`))
	head := e.must(200, e.do("PUT", "/eb/b", "x")).Header()
	if head.Get("X-Amz-Server-Side-Encryption") != "aws:kms" ||
		!strings.HasSuffix(head.Get("X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id"), ":key/1234abcd") ||
		head.Get("X-Amz-Server-Side-Encryption-Bucket-Key-Enabled") != "true" {
		t.Fatalf("kms headers %v", head)
	}
	if body := e.must(200, e.do("GET", "/eb?encryption", "")).Body.String(); !strings.Contains(body, "<KMSMasterKeyID>1234abcd</KMSMasterKeyID>") {
		t.Fatalf("stored config %s", body)
	}
	e.must(400, e.do("PUT", "/eb?encryption", `<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm><KMSMasterKeyID>k</KMSMasterKeyID></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`))
}

// A bucket policy's explicit Deny stops deletes: the bucket and object
// versions here, as a "nobody deletes this" policy does on AWS. A delete
// marker is still allowed, and the policy itself can always be removed.
func TestBucketPolicyDeniesDeletes(t *testing.T) {
	e := newEnv(t)
	e.versioned("pd", "Enabled")
	e.must(200, e.do("PUT", "/pd/list.csv", "1"))
	e.must(200, e.do("PUT", "/pd/list.csv", "2"))
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*",` +
		`"Action":["s3:DeleteBucket","s3:DeleteObjectVersion"],"Resource":["arn:aws:s3:::pd","arn:aws:s3:::pd/*"],` +
		`"Condition":{"ArnNotEquals":{"aws:PrincipalArn":"arn:aws:iam::000000000000:role/keeper"}}}]}`
	e.must(200, e.do("PUT", "/pd?policy", policy))

	lv := e.versions("pd")
	old := lv.Versions[len(lv.Versions)-1].VersionID
	if rec := e.do("DELETE", "/pd/list.csv?versionId="+old, ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), "AccessDenied") {
		t.Fatalf("version delete: %d %s", rec.Code, rec.Body.String())
	}
	rec := e.must(200, e.do("POST", "/pd?delete", "<Delete><Object><Key>list.csv</Key><VersionId>"+old+"</VersionId></Object></Delete>"))
	if !strings.Contains(rec.Body.String(), "<Code>AccessDenied</Code>") {
		t.Fatalf("batch version delete: %s", rec.Body.String())
	}
	if rec := e.do("DELETE", "/pd", ""); rec.Code != 403 {
		t.Fatalf("bucket delete: %d %s", rec.Code, rec.Body.String())
	}
	if lv := e.versions("pd"); len(lv.Versions) != 2 {
		t.Fatalf("versions after denied deletes: %+v", lv)
	}
	e.must(204, e.do("DELETE", "/pd/list.csv", "")) // a delete marker

	// The keeper, by assumed role, may.
	e.h.Principal = func(*http.Request) string { return "arn:aws:iam::000000000000:role/keeper" }
	e.must(204, e.do("DELETE", "/pd/list.csv?versionId="+old, ""))
	e.h.Principal = nil

	e.must(204, e.do("DELETE", "/pd?policy", ""))
	lv = e.versions("pd")
	for _, v := range lv.Versions {
		e.must(204, e.do("DELETE", "/pd/list.csv?versionId="+v.VersionID, ""))
	}
	for _, m := range lv.DeleteMarkers {
		e.must(204, e.do("DELETE", "/pd/list.csv?versionId="+m.VersionID, ""))
	}
	e.must(204, e.do("DELETE", "/pd", ""))
}
