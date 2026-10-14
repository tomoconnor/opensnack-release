// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"os"
	"strings"

	"opensnack/internal/util"
)

//
// OBJECT TAGGING  (PUT/GET/DELETE /:bucket/:key?tagging)
//

const maxObjectTags = 10

// tagsFromHeader parses the x-amz-tagging header of a PUT or copy, which
// carries the tag set URL-query encoded.
func tagsFromHeader(w http.ResponseWriter, r *http.Request) ([]Tag, bool) {
	raw := r.Header.Get("X-Amz-Tagging")
	if raw == "" {
		return nil, true
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		invalidArgument(w, "The header 'x-amz-tagging' shall be encoded as UTF-8 then URLEncoded URL query parameters without tag name duplicates.")
		return nil, false
	}
	var tags []Tag
	for k, vs := range vals {
		if len(vs) != 1 {
			writeS3Error(w, http.StatusBadRequest, "InvalidTag", "Cannot provide multiple Tags with the same key", "")
			return nil, false
		}
		tags = append(tags, Tag{Key: k, Value: vs[0]})
	}
	if code, msg, ok := validateTags(tags, maxObjectTags); !ok {
		writeS3Error(w, http.StatusBadRequest, code, msg, "")
		return nil, false
	}
	return sortedTags(tags), true
}

func (h *Handler) GetObjectTagging(w http.ResponseWriter, r *http.Request) {
	_, v := h.lookupObject(w, r)
	if v == nil {
		return
	}
	if v.VersionID != "" {
		w.Header().Set("X-Amz-Version-Id", v.VersionID)
	}
	writeXMLOK(w, Tagging{Xmlns: s3XMLNS, TagSet: v.Tags})
}

func (h *Handler) PutObjectTagging(w http.ResponseWriter, r *http.Request) {
	var in Tagging
	if !readXML(w, r, &in) {
		return
	}
	if code, msg, ok := validateTags(in.TagSet, maxObjectTags); !ok {
		writeS3Error(w, http.StatusBadRequest, code, msg, "")
		return
	}
	h.editObjectTags(w, r, sortedTags(in.TagSet), http.StatusOK)
}

func (h *Handler) DeleteObjectTagging(w http.ResponseWriter, r *http.Request) {
	h.editObjectTags(w, r, nil, http.StatusNoContent)
}

func (h *Handler) editObjectTags(w http.ResponseWriter, r *http.Request, tags []Tag, status int) {
	bucket, key := extractBucketKey(r.URL.Path)
	defer lockKey(util.GlobalNamespace(r), bucket, key)()
	ns, v := h.lookupObject(w, r)
	if v == nil {
		return
	}
	v.Tags = tags
	if err := h.saveVersion(ns, v); err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return
	}
	if v.VersionID != "" {
		w.Header().Set("X-Amz-Version-Id", v.VersionID)
	}
	w.WriteHeader(status)
}

//
// COPY OBJECT
// PUT /:bucket/:key with x-amz-copy-source
//

type CopyObjectResult struct {
	XMLName      struct{} `xml:"CopyObjectResult"`
	Xmlns        string   `xml:"xmlns,attr"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

func (h *Handler) CopyObject(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	bucket, key := extractBucketKey(r.URL.Path)
	_, bmeta, err := h.loadBucket(ns, bucket)
	if err != nil {
		writeS3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist", bucket)
		return
	}

	source := r.Header.Get("X-Amz-Copy-Source")
	srcPath, srcVid, _ := strings.Cut(source, "?versionId=")
	if p, err := url.PathUnescape(srcPath); err == nil {
		srcPath = p
	}
	srcBucket, srcKey := extractBucketKey("/" + strings.TrimPrefix(srcPath, "/"))
	if srcBucket == "" || srcKey == "" {
		invalidArgument(w, "Copy Source must mention the source bucket and key: sourcebucket/sourcekey")
		return
	}
	if _, _, err := h.loadBucket(ns, srcBucket); err != nil {
		writeS3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist", srcBucket)
		return
	}
	src, err := h.findVersion(ns, srcBucket, srcKey, srcVid)
	switch {
	case err != nil:
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return
	case src == nil && srcVid != "":
		writeS3Error(w, http.StatusNotFound, "NoSuchVersion", "The specified version does not exist.", source)
		return
	case src == nil || (src.DeleteMarker && srcVid == ""):
		writeS3Error(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.", source)
		return
	case src.DeleteMarker:
		writeS3Error(w, http.StatusBadRequest, "InvalidRequest",
			"The source of a copy request may not specifically refer to a delete marker by version id.", source)
		return
	}

	if m := r.Header.Get("X-Amz-Copy-Source-If-Match"); m != "" && !etagMatches(m, src.ETag) {
		preconditionFailed(w, "x-amz-copy-source-If-Match")
		return
	}
	if m := r.Header.Get("X-Amz-Copy-Source-If-None-Match"); m != "" && etagMatches(m, src.ETag) {
		preconditionFailed(w, "x-amz-copy-source-If-None-Match")
		return
	}

	replaceMeta := strings.EqualFold(r.Header.Get("X-Amz-Metadata-Directive"), "REPLACE")
	if srcBucket == bucket && srcKey == key && !replaceMeta &&
		r.Header.Get("X-Amz-Server-Side-Encryption") == "" &&
		r.Header.Get("X-Amz-Storage-Class") == "" &&
		r.Header.Get("X-Amz-Website-Redirect-Location") == "" {
		writeS3Error(w, http.StatusBadRequest, "InvalidRequest",
			"This copy request is illegal because it is trying to copy an object to itself without changing the object's metadata, storage class, website redirect location or encryption attributes.", "")
		return
	}

	body, err := os.ReadFile(blobPath(ns, src.Blob))
	if err != nil {
		writeS3Error(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.", source)
		return
	}

	meta := metaFromRequest(r, bucket, key, body)
	if !replaceMeta {
		// Content headers and user metadata come from the source; the
		// encryption and storage class are the destination's own.
		meta.ContentType = src.ContentType
		meta.UserMetadata = src.UserMetadata
		for k, v := range src.Headers {
			if !strings.HasPrefix(k, "X-Amz-Server-Side-Encryption") && k != "X-Amz-Storage-Class" {
				meta.Headers[k] = v
			}
		}
	}
	if strings.EqualFold(r.Header.Get("X-Amz-Tagging-Directive"), "REPLACE") {
		tags, ok := tagsFromHeader(w, r)
		if !ok {
			return
		}
		meta.Tags = tags
	} else {
		meta.Tags = src.Tags
	}
	applyDefaultEncryption(meta.Headers, bmeta, r)

	blob, err := writeBlob(ns, body)
	if err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return
	}
	meta.Blob = blob
	if err := h.storeObject(ns, bmeta.Versioning, &meta, false); err != nil {
		removeBlob(ns, blob)
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return
	}

	writeWriteHeaders(w, &meta)
	if src.VersionID != "" {
		w.Header().Set("X-Amz-Copy-Source-Version-Id", src.VersionID)
	}
	writeXMLOK(w, CopyObjectResult{
		Xmlns:        s3XMLNS,
		LastModified: meta.modTime().Format("2006-01-02T15:04:05.000Z"),
		ETag:         meta.ETag,
	})
}

//
// DELETE OBJECTS
// POST /:bucket?delete
//

type deleteRequest struct {
	XMLName xml.Name `xml:"Delete"`
	Quiet   bool     `xml:"Quiet"`
	Objects []struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
	} `xml:"Object"`
}

type DeletedObject struct {
	Key                   string `xml:"Key"`
	VersionID             string `xml:"VersionId,omitempty"`
	DeleteMarker          bool   `xml:"DeleteMarker,omitempty"`
	DeleteMarkerVersionID string `xml:"DeleteMarkerVersionId,omitempty"`
}

type DeleteError struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId,omitempty"`
	Code      string `xml:"Code"`
	Message   string `xml:"Message"`
}

type DeleteResult struct {
	XMLName struct{}        `xml:"DeleteResult"`
	Xmlns   string          `xml:"xmlns,attr"`
	Deleted []DeletedObject `xml:"Deleted"`
	Errors  []DeleteError   `xml:"Error"`
}

func (h *Handler) DeleteObjects(w http.ResponseWriter, r *http.Request) {
	_, bmeta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	var in deleteRequest
	if !readXML(w, r, &in) {
		return
	}
	if len(in.Objects) == 0 || len(in.Objects) > 1000 {
		malformedXML(w)
		return
	}

	out := DeleteResult{Xmlns: s3XMLNS}
	for _, o := range in.Objects {
		var res deleteResult
		var err error
		if o.VersionID != "" {
			res, err = h.deleteVersion(ns, bmeta.Name, o.Key, o.VersionID)
		} else {
			res, err = h.deleteKey(ns, bmeta.Versioning, bmeta.Name, o.Key)
		}
		if err != nil {
			out.Errors = append(out.Errors, DeleteError{
				Key: o.Key, VersionID: o.VersionID, Code: "InternalError", Message: err.Error(),
			})
			continue
		}
		if in.Quiet {
			continue
		}
		d := DeletedObject{Key: o.Key, VersionID: o.VersionID, DeleteMarker: res.DeleteMarker}
		if res.DeleteMarker {
			d.DeleteMarkerVersionID = res.VersionID
		}
		out.Deleted = append(out.Deleted, d)
	}
	writeXMLOK(w, out)
}
