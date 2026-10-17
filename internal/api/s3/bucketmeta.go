// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// bucketMeta is the bucket row's attributes. The older handlers (ACL, policy,
// versioning) edit the same JSON as a map, so every key they write has a
// field here and survives a round trip through this struct.
type bucketMeta struct {
	Name         string                  `json:"Name"`
	CreationDate time.Time               `json:"CreationDate"`
	Region       string                  `json:"region,omitempty"`
	Versioning   string                  `json:"versioning,omitempty"`
	ACL          string                  `json:"acl,omitempty"`
	Policy       string                  `json:"policy,omitempty"`
	Lifecycle    *LifecycleConfiguration `json:"lifecycle,omitempty"`

	TagSet            []Tag                              `json:"tag_set,omitempty"`
	Encryption        *ServerSideEncryptionConfiguration `json:"encryption,omitempty"`
	PublicAccessBlock *PublicAccessBlockConfiguration    `json:"public_access_block,omitempty"`
	Ownership         *OwnershipControls                 `json:"ownership_controls,omitempty"`
	CORS              *CORSConfiguration                 `json:"cors,omitempty"`
	Website           *WebsiteConfiguration              `json:"website,omitempty"`
	Replication       *ReplicationConfiguration          `json:"replication,omitempty"`
}

var errNoSuchBucket = errors.New("no such bucket")

func (h *Handler) loadBucket(ns, bucket string) (*resource.Resource, *bucketMeta, error) {
	res, err := h.Store.Get(bucket, "s3", "bucket", ns)
	if err != nil || res == nil {
		return nil, nil, errNoSuchBucket
	}
	meta := &bucketMeta{}
	if len(res.Attributes) > 0 {
		_ = json.Unmarshal(res.Attributes, meta)
	}
	if meta.Name == "" {
		meta.Name = bucket
	}
	return res, meta, nil
}

func (h *Handler) saveBucket(res *resource.Resource, meta *bucketMeta) error {
	buf, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	res.Attributes = buf
	return h.Store.Update(res)
}

// bucketOr404 loads the request's bucket, writing NoSuchBucket when absent.
func (h *Handler) bucketOr404(w http.ResponseWriter, r *http.Request) (*resource.Resource, *bucketMeta, bool) {
	bucket, _ := extractBucketKey(r.URL.Path)
	res, meta, err := h.loadBucket(util.GlobalNamespace(r), bucket)
	if err != nil {
		writeS3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist", bucket)
		return nil, nil, false
	}
	return res, meta, true
}

// editBucket applies fn to the request's bucket and saves it. fn may write an
// error itself and return false to abandon the edit.
func (h *Handler) editBucket(w http.ResponseWriter, r *http.Request, fn func(*bucketMeta) bool) bool {
	bucket, _ := extractBucketKey(r.URL.Path)
	defer lockBucket(util.GlobalNamespace(r), bucket)()
	res, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return false
	}
	if !fn(meta) {
		return false
	}
	if err := h.saveBucket(res, meta); err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return false
	}
	return true
}

// bucketRegion is the region a bucket lives in; buckets from before regions
// were recorded are in the default one.
func (m *bucketMeta) region() string {
	if m.Region != "" {
		return m.Region
	}
	return util.DefaultRegion()
}

//
// Shared tag helpers, also used by S3 Control's tagging operations.
//

// BucketTags returns a bucket's tags sorted by key.
func (h *Handler) BucketTags(ns, bucket string) ([]Tag, error) {
	_, meta, err := h.loadBucket(ns, bucket)
	if err != nil {
		return nil, err
	}
	return sortedTags(meta.TagSet), nil
}

// UpdateBucketTags replaces a bucket's tags with fn's result.
func (h *Handler) UpdateBucketTags(ns, bucket string, fn func([]Tag) []Tag) error {
	defer lockBucket(ns, bucket)()
	res, meta, err := h.loadBucket(ns, bucket)
	if err != nil {
		return err
	}
	meta.TagSet = sortedTags(fn(meta.TagSet))
	return h.saveBucket(res, meta)
}

// IsNoSuchBucket reports whether err means the bucket does not exist.
func IsNoSuchBucket(err error) bool { return errors.Is(err, errNoSuchBucket) }

func sortedTags(tags []Tag) []Tag {
	out := append([]Tag(nil), tags...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// validateTags applies the S3 tag rules shared by buckets and objects.
func validateTags(tags []Tag, max int) (code, msg string, ok bool) {
	if len(tags) > max {
		return "BadRequest", "Object tags cannot be greater than " + strconv.Itoa(max), false
	}
	seen := map[string]bool{}
	for _, t := range tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			return "InvalidTag", "The TagKey you have provided is invalid", false
		}
		if seen[t.Key] {
			return "InvalidTag", "Cannot provide multiple Tags with the same key", false
		}
		seen[t.Key] = true
	}
	return "", "", true
}

//
// XML helpers
//

// writeXMLStatus writes v as an S3 XML body with the given status.
func writeXMLStatus(w http.ResponseWriter, status int, v any) {
	out, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return
	}
	awsresponses.WriteAWSHeaders(w)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

func writeXMLOK(w http.ResponseWriter, v any) { writeXMLStatus(w, http.StatusOK, v) }

func malformedXML(w http.ResponseWriter) {
	writeS3Error(w, http.StatusBadRequest, "MalformedXML",
		"The XML you provided was not well-formed or did not validate against our published schema", "")
}

func invalidArgument(w http.ResponseWriter, msg string) {
	writeS3Error(w, http.StatusBadRequest, "InvalidArgument", msg, "")
}
