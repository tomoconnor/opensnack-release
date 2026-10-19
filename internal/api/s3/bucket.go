// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

//
// CREATE BUCKET
// PUT /:bucket
//

type createBucketConfiguration struct {
	XMLName            xml.Name `xml:"CreateBucketConfiguration"`
	LocationConstraint string   `xml:"LocationConstraint"`
	Tags               []Tag    `xml:"Tags>Tag"`
}

func (h *Handler) CreateBucket(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	ns := util.GlobalNamespace(r)

	if _, err := h.Store.Get(bucket, "s3", "bucket", ns); err == nil {
		writeS3Error(w, http.StatusConflict, "BucketAlreadyOwnedByYou",
			"Your previous request to create the named bucket succeeded and you already own it.", bucket)
		return
	}

	var cfg createBucketConfiguration
	if body, _ := io.ReadAll(r.Body); len(strings.TrimSpace(string(body))) > 0 {
		if err := xml.Unmarshal(body, &cfg); err != nil {
			malformedXML(w)
			return
		}
	}
	if code, msg, ok := validateTags(cfg.Tags, 50); !ok {
		writeS3Error(w, http.StatusBadRequest, code, msg, "")
		return
	}

	region := cfg.LocationConstraint
	switch region {
	case "":
		region = util.Region(r)
	case "EU":
		region = "eu-west-1"
	}

	meta := bucketMeta{
		Name:         bucket,
		CreationDate: time.Now().UTC(),
		Region:       region,
		TagSet:       sortedTags(cfg.Tags),
	}
	buf, _ := json.Marshal(meta)
	res := &resource.Resource{ID: bucket, Namespace: ns, Service: "s3", Type: "bucket", Attributes: buf}
	if err := h.Store.Create(res); err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), bucket)
		return
	}
	awsresponses.WriteEmpty200(w, map[string]string{"Location": "/" + bucket})
}

//
// DELETE BUCKET
// DELETE /:bucket
//

func (h *Handler) DeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	ns := util.GlobalNamespace(r)

	_, meta, err := h.loadBucket(ns, bucket)
	if err != nil {
		w.WriteHeader(404)
		awsresponses.WriteXML(w, NoSuchBucket(bucket))
		return
	}
	if h.denied(r, meta, "s3:DeleteBucket", "arn:aws:s3:::"+bucket) {
		accessDenied(w)
		return
	}

	// S3 refuses to delete a bucket with objects (or object versions, or
	// delete markers) in it; force_destroy in Terraform empties it first.
	for _, typ := range []string{"object", versionType} {
		objects, err := h.Store.List("s3", typ, ns)
		if err != nil {
			writeS3Error(w, 500, "InternalError", err.Error(), "")
			return
		}
		for _, o := range objects {
			if strings.HasPrefix(o.ID, bucket+"/") {
				writeS3Error(w, http.StatusConflict, "BucketNotEmpty",
					"The bucket you tried to delete is not empty", bucket)
				return
			}
		}
	}

	_ = h.Store.Delete(bucket, "s3", "bucket", ns)
	awsresponses.WriteEmpty204(w)
}

//
// LIST BUCKETS
// GET /
//

func (h *Handler) ListBuckets(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)

	items, err := h.Store.List("s3", "bucket", ns)
	if err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return
	}

	resp := ListAllMyBucketsResult{
		Xmlns: s3XMLNS,
		Owner: &Owner{ID: util.AccountID(r), DisplayName: "opensnack"},
	}
	region := r.URL.Query().Get("bucket-region")
	prefix := r.URL.Query().Get("prefix")
	for _, item := range items {
		var m bucketMeta
		_ = json.Unmarshal(item.Attributes, &m)
		if m.Name == "" {
			m.Name = item.ID
		}
		if (region != "" && m.region() != region) || !strings.HasPrefix(m.Name, prefix) {
			continue
		}
		resp.Buckets = append(resp.Buckets, BucketEntry{
			Name: m.Name, CreationDate: m.CreationDate, BucketRegion: m.region(),
		})
	}
	writeXMLOK(w, resp)
}

//
// HEAD BUCKET
// HEAD /:bucket
//

func (h *Handler) HeadBucket(w http.ResponseWriter, r *http.Request) {
	bucket, _ := extractBucketKey(r.URL.Path)
	_, meta, err := h.loadBucket(util.GlobalNamespace(r), bucket)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// The SDK's GetBucketRegion (which the provider uses on every refresh)
	// reads this header.
	w.Header().Set("X-Amz-Bucket-Region", meta.region())
	w.Header().Set("X-Amz-Access-Point-Alias", "false")
	awsresponses.WriteEmpty200(w, nil)
}

//
// GET BUCKET LOCATION
// GET /:bucket?location
//

func (h *Handler) GetBucketLocation(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	// us-east-1 is reported as an empty constraint.
	region := meta.region()
	if region == "us-east-1" {
		region = ""
	}
	writeXMLOK(w, struct {
		XMLName xml.Name `xml:"LocationConstraint"`
		Xmlns   string   `xml:"xmlns,attr"`
		Region  string   `xml:",chardata"`
	}{Xmlns: s3XMLNS, Region: region})
}

//
// VERSIONING
// PUT/GET /:bucket?versioning
//

type versioningConfiguration struct {
	XMLName   xml.Name `xml:"VersioningConfiguration"`
	Xmlns     string   `xml:"xmlns,attr,omitempty"`
	Status    string   `xml:"Status,omitempty"`
	MFADelete string   `xml:"MfaDelete,omitempty"`
}

func (h *Handler) PutBucketVersioning(w http.ResponseWriter, r *http.Request) {
	var in versioningConfiguration
	if !readXML(w, r, &in) {
		return
	}
	if in.Status != "Enabled" && in.Status != "Suspended" {
		malformedXML(w)
		return
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool {
		if in.Status == "Suspended" && m.Replication != nil {
			writeS3Error(w, http.StatusConflict, "InvalidBucketState",
				"A replication configuration is present on this bucket, so you cannot change the versioning state. To change the versioning state, first delete the replication configuration.", "")
			return false
		}
		m.Versioning = in.Status
		return true
	}) {
		w.WriteHeader(http.StatusOK)
	}
}

// GetBucketVersioning reports no Status at all for a bucket that has never
// had versioning configured.
func (h *Handler) GetBucketVersioning(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	writeXMLOK(w, versioningConfiguration{Xmlns: s3XMLNS, Status: meta.Versioning})
}
