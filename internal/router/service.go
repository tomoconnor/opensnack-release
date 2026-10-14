// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"net/http"
	"strings"

	"opensnack/internal/util"
)

// serviceFor works out which AWS service a request is for, in order of
// reliability:
//
//  1. the Host header, when it is a real AWS endpoint ({service}.{region}.amazonaws.com,
//     {bucket}.s3..., {account}.s3-control...);
//  2. X-Amz-Target, which names the service for every JSON-protocol call;
//  3. the service in the SigV4 credential scope, which every real client
//     sends, so path-style requests to localhost route correctly too.
//
// It returns "" when none of those say, and the caller falls back to the
// legacy path routes (/sqs, /lambda, ...). For virtual-hosted S3 it also
// returns the bucket from the host name.
func serviceFor(r *http.Request) (service, bucket string) {
	if h, ok := util.ParseAWSHost(r.Host); ok {
		return canonicalService(h.Service, r), h.Bucket
	}
	if t := r.Header.Get("X-Amz-Target"); t != "" {
		if s := serviceForTarget(t); s != "" {
			return s, ""
		}
	}
	if s := util.IdentityFrom(r).SigningService; s != "" {
		return canonicalService(s, r), ""
	}
	return "", ""
}

// canonicalService maps endpoint prefixes and signing names onto the names
// the dispatcher switches on.
func canonicalService(s string, r *http.Request) string {
	switch s {
	case "s3":
		// S3 Control signs as "s3" too; its paths give it away.
		p := r.URL.Path
		if strings.HasPrefix(p, "/v20180820/") || strings.HasPrefix(p, "/s3-control/") {
			return "s3-control"
		}
	case "email":
		return "ses"
	case "monitoring":
		return "cloudwatch"
	}
	return s
}

func serviceForTarget(target string) string {
	prefixes := []struct{ prefix, service string }{
		{"TrentService.", "kms"},
		{"DynamoDB_", "dynamodb"},
		{"Logs_", "logs"},
		{"secretsmanager.", "secretsmanager"},
		{"AmazonSSM.", "ssm"},
		{"AmazonSQS.", "sqs"},
		{"AWSLambda", "lambda"},
		{"AWSEvents.", "events"},
		{"GraniteServiceVersion20100801.", "cloudwatch"},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(target, p.prefix) {
			return p.service
		}
	}
	return ""
}

// withVirtualHostBucket rewrites a virtual-hosted-style S3 request
// (bucket in the host name) to the path style the S3 handlers parse.
func withVirtualHostBucket(r *http.Request, bucket string) *http.Request {
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + bucket + r.URL.Path
	if r.URL.RawPath != "" {
		r2.URL.RawPath = "/" + bucket + r.URL.RawPath
	}
	return r2
}

// stripPrefix drops a legacy path prefix such as "/s3-control" so handlers
// see the real AWS path.
func stripPrefix(r *http.Request, prefix string) *http.Request {
	if !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return r
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.RawPath != "" {
		r2.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, prefix)
	}
	return r2
}

func methods(h http.HandlerFunc, allowed ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, m := range allowed {
			if r.Method == m {
				h(w, r)
				return
			}
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
