// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3control

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"

	"opensnack/internal/api/s3"
	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// Handler serves the S3 Control tagging API. Bucket tags live on the bucket
// itself, so these operations and S3's ?tagging see the same tag set.
type Handler struct {
	Store resource.Store
	s3    *s3.Handler
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store, s3: s3.NewHandler(store)}
}

const xmlns = "http://awss3control.amazonaws.com/doc/2018-08-20/"

type tagSet struct {
	Tags []s3.Tag `xml:"Tag"`
}

type listTagsForResourceResult struct {
	XMLName struct{} `xml:"ListTagsForResourceResult"`
	Xmlns   string   `xml:"xmlns,attr"`
	Tags    tagSet   `xml:"Tags"`
}

type tagResourceRequest struct {
	XMLName struct{} `xml:"TagResourceRequest"`
	Tags    tagSet   `xml:"Tags"`
}

// Tags serves /v20180820/tags/{ResourceArn}:
//
//	GET    ListTagsForResource
//	POST   TagResource
//	DELETE UntagResource (?tagKeys=...)
func (h *Handler) Tags(w http.ResponseWriter, r *http.Request) {
	arn := resourceARN(r)
	bucket, ok := bucketFromARN(arn)
	if !ok {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "Unsupported resource ARN: "+arn)
		return
	}
	ns := util.GlobalNamespace(r)

	var err error
	switch r.Method {
	case http.MethodGet:
		var tags []s3.Tag
		if tags, err = h.s3.BucketTags(ns, bucket); err == nil {
			writeXML(w, http.StatusOK, listTagsForResourceResult{Xmlns: xmlns, Tags: tagSet{tags}})
			return
		}
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var in tagResourceRequest
		if xml.Unmarshal(body, &in) != nil {
			writeError(w, http.StatusBadRequest, "MalformedXML", "The XML you provided was not well-formed")
			return
		}
		err = h.s3.UpdateBucketTags(ns, bucket, func(old []s3.Tag) []s3.Tag {
			byKey := map[string]string{}
			for _, t := range old {
				byKey[t.Key] = t.Value
			}
			for _, t := range in.Tags.Tags {
				byKey[t.Key] = t.Value
			}
			out := make([]s3.Tag, 0, len(byKey))
			for k, v := range byKey {
				out = append(out, s3.Tag{Key: k, Value: v})
			}
			return out
		})
	case http.MethodDelete:
		drop := map[string]bool{}
		for _, k := range r.URL.Query()["tagKeys"] {
			drop[k] = true
		}
		err = h.s3.UpdateBucketTags(ns, bucket, func(old []s3.Tag) []s3.Tag {
			var out []s3.Tag
			for _, t := range old {
				if !drop[t.Key] {
					out = append(out, t)
				}
			}
			return out
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed against this resource.")
		return
	}

	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case s3.IsNoSuchBucket(err):
		writeError(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
	default:
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
	}
}

// resourceARN reads the ARN from the path, where it is percent-encoded
// (and may contain encoded slashes).
func resourceARN(r *http.Request) string {
	raw := r.URL.EscapedPath()
	_, after, found := strings.Cut(raw, "/tags/")
	if !found {
		return ""
	}
	if decoded, err := url.PathUnescape(after); err == nil {
		return decoded
	}
	return after
}

// bucketFromARN accepts arn:<partition>:s3:::<bucket>.
func bucketFromARN(arn string) (string, bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "s3" || parts[5] == "" || strings.Contains(parts[5], "/") {
		return "", false
	}
	return parts[5], true
}

func writeXML(w http.ResponseWriter, status int, v any) {
	out, _ := xml.Marshal(v)
	awsresponses.WriteAWSHeaders(w)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

// writeError uses the S3 Control error shape, which wraps Error in
// ErrorResponse.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeXML(w, status, struct {
		XMLName   struct{} `xml:"ErrorResponse"`
		Code      string   `xml:"Error>Code"`
		Message   string   `xml:"Error>Message"`
		RequestID string   `xml:"RequestId"`
	}{Code: code, Message: msg, RequestID: awsresponses.NextRequestID()})
}
