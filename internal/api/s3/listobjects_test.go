// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3_test

import (
	"encoding/xml"
	"net/http/httptest"
	"testing"

	"opensnack/internal/api/s3"
	"opensnack/internal/resource"
)

// listResult mirrors both ListBucketResult shapes closely enough to assert on:
// V1 and V2 differ only in how they name the paging cursor.
type listResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	Name                  string
	Prefix                string
	Delimiter             string
	MaxKeys               int
	KeyCount              int
	IsTruncated           bool
	StartAfter            string
	NextMarker            string
	NextContinuationToken string
	Contents              []struct {
		Key          string
		LastModified string
		ETag         string
		Size         int64
		StorageClass string
	}
	CommonPrefixes []struct {
		Prefix string
	}
}

func seedBucket(t *testing.T, keys ...string) *MockStore {
	t.Helper()

	store := NewMockStore()
	for _, bucket := range []string{"b", "other"} {
		store.Create(&resource.Resource{
			ID:         bucket,
			Namespace:  "ns1",
			Service:    "s3",
			Type:       "bucket",
			Attributes: []byte(`{"Name":"` + bucket + `"}`),
		})
	}

	meta := []byte(`{"etag":"\"abc\"","size":3,"content_type":"text/plain","created_at":"2026-01-02T03:04:05Z"}`)
	for _, key := range append(keys, "") {
		id := "b/" + key
		if key == "" {
			// An object in a second bucket, to prove the listing filters by
			// bucket rather than returning the whole namespace.
			id = "other/leak.txt"
		}
		store.Create(&resource.Resource{
			ID:         id,
			Namespace:  "ns1",
			Service:    "s3",
			Type:       "object",
			Attributes: meta,
		})
	}
	return store
}

func listObjects(t *testing.T, store *MockStore, query string) (int, listResult) {
	t.Helper()

	req := httptest.NewRequest("GET", "/b"+query, nil)
	// Namespace isolation travels in the User-Agent (see k6/README.md).
	req.Header.Set("User-Agent", "aws-cli/2.15.0 custom-ns1")
	rec := httptest.NewRecorder()

	s3.NewHandler(store).ListObjects(rec, req)

	var out listResult
	if rec.Code == 200 {
		if err := xml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal %s: %v\n%s", query, err, rec.Body.String())
		}
	}
	return rec.Code, out
}

func keysOf(res listResult) []string {
	var out []string
	for _, c := range res.Contents {
		out = append(out, c.Key)
	}
	return out
}

func prefixesOf(res listResult) []string {
	var out []string
	for _, p := range res.CommonPrefixes {
		out = append(out, p.Prefix)
	}
	return out
}

func assertEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
	}
}

// `aws s3 ls s3://bucket/` sends exactly this: list-type=2 with a "/" delimiter
// and encoding-type=url. Before ListObjectsV2 existed it answered 405.
func TestListObjectsV2_AwsCliShape(t *testing.T) {
	store := seedBucket(t, "a.txt", "photos/1.jpg", "photos/2.jpg", "photos/sub/3.jpg")

	code, res := listObjects(t, store, "?list-type=2&prefix=&delimiter=%2F&encoding-type=url")

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	if res.Name != "b" {
		t.Fatalf("expected bucket b, got %q", res.Name)
	}
	if res.MaxKeys != 1000 || res.IsTruncated {
		t.Fatalf("expected an untruncated 1000-key page, got MaxKeys=%d IsTruncated=%v", res.MaxKeys, res.IsTruncated)
	}
	// KeyCount counts rolled-up prefixes alongside keys.
	if res.KeyCount != 2 {
		t.Fatalf("expected KeyCount 2, got %d", res.KeyCount)
	}

	assertEqual(t, "contents", keysOf(res), []string{"a.txt"})
	assertEqual(t, "common prefixes", prefixesOf(res), []string{"photos/"})

	if got := res.Contents[0]; got.ETag != `"abc"` || got.Size != 3 || got.StorageClass != "STANDARD" {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got := res.Contents[0].LastModified; got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("expected a millisecond UTC timestamp, got %q", got)
	}
}

// Without a delimiter the listing is flat, which is what `--recursive` asks for.
func TestListObjectsV2_NoDelimiterIsRecursive(t *testing.T) {
	store := seedBucket(t, "photos/sub/3.jpg", "a.txt", "photos/1.jpg")

	_, res := listObjects(t, store, "?list-type=2")

	// Sorted by key, and the object in bucket "other" stays out of it.
	assertEqual(t, "contents", keysOf(res), []string{"a.txt", "photos/1.jpg", "photos/sub/3.jpg"})
}

func TestListObjectsV2_PrefixAndDelimiter(t *testing.T) {
	store := seedBucket(t, "a.txt", "photos/1.jpg", "photos/sub/3.jpg")

	_, res := listObjects(t, store, "?list-type=2&prefix=photos%2F&delimiter=%2F")

	assertEqual(t, "contents", keysOf(res), []string{"photos/1.jpg"})
	assertEqual(t, "common prefixes", prefixesOf(res), []string{"photos/sub/"})
}

// A truncated page hands back a continuation token, and replaying that token
// must resume past the whole rolled-up group rather than re-listing it.
func TestListObjectsV2_ContinuationToken(t *testing.T) {
	store := seedBucket(t, "a.txt", "photos/1.jpg", "photos/2.jpg", "z.txt")

	_, first := listObjects(t, store, "?list-type=2&delimiter=%2F&max-keys=2")

	if !first.IsTruncated || first.NextContinuationToken == "" {
		t.Fatalf("expected a truncated first page with a token, got %+v", first)
	}
	assertEqual(t, "page 1 contents", keysOf(first), []string{"a.txt"})
	assertEqual(t, "page 1 prefixes", prefixesOf(first), []string{"photos/"})

	_, second := listObjects(t, store,
		"?list-type=2&delimiter=%2F&max-keys=2&continuation-token="+first.NextContinuationToken)

	if second.IsTruncated {
		t.Fatalf("expected the second page to finish the listing, got %+v", second)
	}
	assertEqual(t, "page 2 contents", keysOf(second), []string{"z.txt"})
	assertEqual(t, "page 2 prefixes", prefixesOf(second), nil)
}

func TestListObjectsV2_StartAfter(t *testing.T) {
	store := seedBucket(t, "a.txt", "b.txt", "c.txt")

	_, res := listObjects(t, store, "?list-type=2&start-after=a.txt")

	if res.StartAfter != "a.txt" {
		t.Fatalf("expected StartAfter echoed back, got %q", res.StartAfter)
	}
	assertEqual(t, "contents", keysOf(res), []string{"b.txt", "c.txt"})
}

// encoding-type=url is how the CLI asks for keys it cannot otherwise represent.
func TestListObjectsV2_UrlEncoding(t *testing.T) {
	store := seedBucket(t, "with space.txt", "dir/plus+sign.txt")

	_, encoded := listObjects(t, store, "?list-type=2&encoding-type=url")
	// "/" stays readable; a space is %20 rather than "+".
	assertEqual(t, "encoded", keysOf(encoded), []string{"dir/plus%2Bsign.txt", "with%20space.txt"})

	_, raw := listObjects(t, store, "?list-type=2")
	assertEqual(t, "raw", keysOf(raw), []string{"dir/plus+sign.txt", "with space.txt"})
}

// The V1 listing is the same body paged by marker, for the older clients.
func TestListObjects_V1Marker(t *testing.T) {
	store := seedBucket(t, "a.txt", "b.txt", "c.txt")

	_, first := listObjects(t, store, "?max-keys=2")

	if !first.IsTruncated || first.NextMarker != "b.txt" {
		t.Fatalf("expected truncation at b.txt, got %+v", first)
	}
	assertEqual(t, "page 1", keysOf(first), []string{"a.txt", "b.txt"})

	_, second := listObjects(t, store, "?marker="+first.NextMarker)
	assertEqual(t, "page 2", keysOf(second), []string{"c.txt"})
}

func TestListObjects_MissingBucket(t *testing.T) {
	req := httptest.NewRequest("GET", "/ghost?list-type=2", nil)
	req.Header.Set("User-Agent", "aws-cli/2.15.0 custom-ns1")
	rec := httptest.NewRecorder()

	s3.NewHandler(NewMockStore()).ListObjects(rec, req)

	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// max-keys above the AWS ceiling is clamped, and a junk value falls back to it.
func TestListObjects_MaxKeysClamped(t *testing.T) {
	store := seedBucket(t, "a.txt")

	for _, q := range []string{"?list-type=2&max-keys=99999", "?list-type=2&max-keys=nonsense", "?list-type=2"} {
		if _, res := listObjects(t, store, q); res.MaxKeys != 1000 {
			t.Fatalf("%s: expected MaxKeys 1000, got %d", q, res.MaxKeys)
		}
	}
}
