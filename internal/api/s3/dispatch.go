// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"net/http"
)

// listingParams are the query parameters of a plain bucket listing; any
// other parameter names a sub-resource.
var listingParams = map[string]bool{
	"list-type": true, "prefix": true, "delimiter": true, "max-keys": true,
	"marker": true, "continuation-token": true, "start-after": true,
	"encoding-type": true, "fetch-owner": true, "x-id": true,
	"key-marker": true, "version-id-marker": true, "versionId": true,
}

type bucketRoutes map[string]map[string]http.HandlerFunc

func (h *Handler) bucketRoutes() bucketRoutes {
	return bucketRoutes{
		http.MethodGet: {
			"versioning":        h.GetBucketVersioning,
			"lifecycle":         h.GetBucketLifecycleConfiguration,
			"acl":               h.GetBucketAcl,
			"policy":            h.GetBucketPolicy,
			"policyStatus":      h.GetBucketPolicyStatus,
			"location":          h.GetBucketLocation,
			"tagging":           h.GetBucketTagging,
			"encryption":        h.GetBucketEncryption,
			"publicAccessBlock": h.GetPublicAccessBlock,
			"ownershipControls": h.GetBucketOwnershipControls,
			"cors":              h.GetBucketCors,
			"website":           h.GetBucketWebsite,
			"accelerate":        h.GetBucketAccelerateConfiguration,
			"requestPayment":    h.GetBucketRequestPayment,
			"logging":           h.GetBucketLogging,
			"replication":       h.GetBucketReplication,
			"object-lock":       h.GetObjectLockConfiguration,
			"versions":          h.ListObjectVersions,
		},
		http.MethodPut: {
			"versioning":        h.PutBucketVersioning,
			"lifecycle":         lockedBucket(h.PutBucketLifecycleConfiguration),
			"acl":               lockedBucket(h.PutBucketAcl),
			"policy":            lockedBucket(h.PutBucketPolicy),
			"tagging":           h.PutBucketTagging,
			"encryption":        h.PutBucketEncryption,
			"publicAccessBlock": h.PutPublicAccessBlock,
			"ownershipControls": h.PutBucketOwnershipControls,
			"cors":              h.PutBucketCors,
			"website":           h.PutBucketWebsite,
		},
		http.MethodDelete: {
			"lifecycle":         h.DeleteBucketLifecycle,
			"policy":            h.DeleteBucketPolicy,
			"tagging":           h.DeleteBucketTagging,
			"encryption":        h.DeleteBucketEncryption,
			"publicAccessBlock": h.DeletePublicAccessBlock,
			"ownershipControls": h.DeleteBucketOwnershipControls,
			"cors":              h.DeleteBucketCors,
			"website":           h.DeleteBucketWebsite,
		},
		http.MethodPost: {
			"delete": h.DeleteObjects,
		},
	}
}

// Dispatch routes an S3 REST request (path style; virtual-host requests
// arrive with the bucket already moved into the path).
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	bucket, key := extractBucketKey(r.URL.Path)
	q := r.URL.Query()

	if bucket == "" {
		if r.Method == http.MethodGet {
			h.ListBuckets(w, r)
			return
		}
		methodNotAllowed(w)
		return
	}

	if key == "" {
		for name := range q {
			if listingParams[name] {
				continue
			}
			if fn, ok := h.bucketRoutes()[r.Method][name]; ok {
				fn(w, r)
				return
			}
			notImplemented(w, name)
			return
		}
		switch r.Method {
		case http.MethodGet:
			h.ListObjects(w, r)
		case http.MethodPut:
			h.CreateBucket(w, r)
		case http.MethodDelete:
			h.DeleteBucket(w, r)
		case http.MethodHead:
			h.HeadBucket(w, r)
		default:
			methodNotAllowed(w)
		}
		return
	}

	_, tagging := q["tagging"]
	switch r.Method {
	case http.MethodPut:
		switch {
		case tagging:
			h.PutObjectTagging(w, r)
		case q.Has("uploadId") || q.Has("acl") || q.Has("retention") || q.Has("legal-hold"):
			notImplemented(w, "object sub-resource")
		case r.Header.Get("X-Amz-Copy-Source") != "":
			h.CopyObject(w, r)
		default:
			h.PutObject(w, r)
		}
	case http.MethodGet:
		switch {
		case tagging:
			h.GetObjectTagging(w, r)
		case q.Has("uploadId") || q.Has("acl") || q.Has("attributes"):
			notImplemented(w, "object sub-resource")
		default:
			h.GetObject(w, r)
		}
	case http.MethodHead:
		h.HeadObject(w, r)
	case http.MethodDelete:
		switch {
		case tagging:
			h.DeleteObjectTagging(w, r)
		case q.Has("uploadId"):
			notImplemented(w, "multipart upload")
		default:
			h.DeleteObject(w, r)
		}
	case http.MethodPost:
		notImplemented(w, "multipart upload")
	default:
		methodNotAllowed(w)
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed",
		"The specified method is not allowed against this resource.", "")
}

func notImplemented(w http.ResponseWriter, what string) {
	writeS3Error(w, http.StatusNotImplemented, "NotImplemented",
		"opensnack does not implement this S3 operation ("+what+")", "")
}
