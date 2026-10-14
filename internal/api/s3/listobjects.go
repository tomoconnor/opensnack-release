// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/util"
)

// maxKeysLimit is the ceiling AWS applies to max-keys, and the default when the
// client does not ask for one.
const maxKeysLimit = 1000

// listPage is one page of a bucket listing, shared by both protocol versions.
type listPage struct {
	contents  []ObjectEntry
	prefixes  []CommonPrefix
	truncated bool
	// next is the last element emitted on this page: the resume point the next
	// request echoes back as a continuation token or marker.
	next string
}

//
// LIST OBJECTS
// GET /:bucket            → ListObjects
// GET /:bucket?list-type=2 → ListObjectsV2
//

// ListObjects serves both listing operations. AWS CLI v2 and the current SDKs
// only ever send the V2 form; V1 is kept for the older clients that still page
// with a marker.
func (h *Handler) ListObjects(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	bucket, _ := extractBucketKey(r.URL.Path)

	if _, err := h.Store.Get(bucket, "s3", "bucket", ns); err != nil {
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusNotFound,
			"NoSuchBucket",
			"The specified bucket does not exist",
			bucket,
		)
		return
	}

	q := r.URL.Query()
	v2 := q.Get("list-type") == "2"
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	encodingType := q.Get("encoding-type")
	maxKeys := parseMaxKeys(q.Get("max-keys"))

	// Where the page resumes from, exclusive. V2 prefers the opaque
	// continuation token it handed out and falls back to the caller's
	// start-after; V1 has only the marker.
	marker := q.Get("marker")
	continuationToken := q.Get("continuation-token")
	startAfter := q.Get("start-after")
	after := marker
	if v2 {
		after = startAfter
		if continuationToken != "" {
			after = decodeContinuationToken(continuationToken)
		}
	}

	entries, err := h.bucketObjects(ns, bucket, prefix)
	if err != nil {
		awsresponses.WriteS3ErrorXML(
			w,
			http.StatusInternalServerError,
			"InternalError",
			"Failed to list objects: "+err.Error(),
			bucket,
		)
		return
	}

	page := paginate(entries, prefix, delimiter, after, maxKeys)

	// Under encoding-type=url every key-derived value in the response goes out
	// percent-encoded, which is how the CLI asks for keys it could not
	// otherwise represent in XML.
	encode := func(s string) string { return s }
	if encodingType == "url" {
		encode = urlEncodeKey
	}
	for i := range page.contents {
		page.contents[i].Key = encode(page.contents[i].Key)
	}
	for i := range page.prefixes {
		page.prefixes[i].Prefix = encode(page.prefixes[i].Prefix)
	}

	if v2 {
		resp := ListBucketResult{
			Xmlns:             s3XMLNS,
			Name:              bucket,
			Prefix:            encode(prefix),
			Delimiter:         encode(delimiter),
			MaxKeys:           maxKeys,
			KeyCount:          len(page.contents) + len(page.prefixes),
			IsTruncated:       page.truncated,
			EncodingType:      encodingType,
			StartAfter:        encode(startAfter),
			ContinuationToken: continuationToken,
			Contents:          page.contents,
			CommonPrefixes:    page.prefixes,
		}
		if page.truncated {
			resp.NextContinuationToken = encodeContinuationToken(page.next)
		}
		awsresponses.WriteXML(w, resp)
		return
	}

	resp := ListBucketResultV1{
		Xmlns:          s3XMLNS,
		Name:           bucket,
		Prefix:         encode(prefix),
		Delimiter:      encode(delimiter),
		MaxKeys:        maxKeys,
		IsTruncated:    page.truncated,
		EncodingType:   encodingType,
		Marker:         encode(marker),
		Contents:       page.contents,
		CommonPrefixes: page.prefixes,
	}
	if page.truncated {
		resp.NextMarker = encode(page.next)
	}
	awsresponses.WriteXML(w, resp)
}

// bucketObjects returns every stored object in the bucket whose key starts with
// prefix, sorted by key the way S3 orders a listing.
func (h *Handler) bucketObjects(ns, bucket, prefix string) ([]ObjectEntry, error) {
	items, err := h.Store.List("s3", "object", ns)
	if err != nil {
		return nil, err
	}

	// Objects are stored under a "<bucket>/<key>" ID across the whole
	// namespace, so the bucket has to be filtered back out here.
	idPrefix := bucket + "/"

	var entries []ObjectEntry
	for _, item := range items {
		if !strings.HasPrefix(item.ID, idPrefix) {
			continue
		}
		key := strings.TrimPrefix(item.ID, idPrefix)
		if key == "" || !strings.HasPrefix(key, prefix) {
			continue
		}

		var meta map[string]any
		if err := jsonUnmarshal(item.Attributes, &meta); err != nil {
			continue
		}

		etag, _ := meta["etag"].(string)
		size, _ := meta["size"].(float64)

		entries = append(entries, ObjectEntry{
			Key:          key,
			LastModified: listTimestamp(meta),
			ETag:         etag,
			Size:         int64(size),
			StorageClass: "STANDARD",
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries, nil
}

// paginate applies the delimiter roll-up and the page window to a sorted key
// list. Keys sharing a delimited group are contiguous once sorted, so a group
// is only ever rolled up once and never revisited on a later page.
func paginate(entries []ObjectEntry, prefix, delimiter, after string, maxKeys int) listPage {
	var page listPage
	seen := map[string]bool{}

	for _, e := range entries {
		if skipUntilAfter(e.Key, after, delimiter) {
			continue
		}

		// The group is the key truncated at the first delimiter that follows
		// the prefix; empty when the key has no delimiter left to roll up on.
		group := ""
		if delimiter != "" {
			if i := strings.Index(e.Key[len(prefix):], delimiter); i >= 0 {
				group = e.Key[:len(prefix)+i+len(delimiter)]
			}
		}

		// A group already emitted costs no slot in the page.
		if group != "" && seen[group] {
			continue
		}

		if len(page.contents)+len(page.prefixes) >= maxKeys {
			page.truncated = true
			break
		}

		if group != "" {
			seen[group] = true
			page.prefixes = append(page.prefixes, CommonPrefix{Prefix: group})
			page.next = group
			continue
		}

		page.contents = append(page.contents, e)
		page.next = e.Key
	}

	return page
}

// skipUntilAfter reports whether a key falls at or before the resume point. A
// page that stopped on a rolled-up group resumes past the whole group, not just
// past the prefix string itself.
func skipUntilAfter(key, after, delimiter string) bool {
	if after == "" {
		return false
	}
	if key <= after {
		return true
	}
	return delimiter != "" &&
		strings.HasSuffix(after, delimiter) &&
		strings.HasPrefix(key, after)
}

// parseMaxKeys reads the max-keys parameter, clamped to what AWS will return.
func parseMaxKeys(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > maxKeysLimit {
		return maxKeysLimit
	}
	return n
}

// The continuation token is opaque to clients, so base64 of the resume key is
// as good a shape as any — it just has to survive a round trip through a query
// string.
func encodeContinuationToken(key string) string {
	return base64.StdEncoding.EncodeToString([]byte(key))
}

func decodeContinuationToken(token string) string {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		// A token we did not mint: treat it as a plain key rather than
		// silently restarting the listing from the top.
		return token
	}
	return string(raw)
}

// urlEncodeKey percent-encodes a key the way S3 does under encoding-type=url:
// spaces become %20 rather than "+", and "/" is left intact so that prefixes
// stay readable.
func urlEncodeKey(s string) string {
	escaped := url.QueryEscape(s)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	return strings.ReplaceAll(escaped, "%2F", "/")
}

// listTimestamp renders the stored modification time in the millisecond-precision UTC
// form S3 uses in listings.
func listTimestamp(meta map[string]any) string {
	for _, k := range []string{"last_modified", "created_at"} {
		s, _ := meta[k].(string)
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
