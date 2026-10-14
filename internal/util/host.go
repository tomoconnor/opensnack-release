// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package util

import (
	"net"
	"net/http"
	"os"
	"strings"
)

// AWSHost is what a real AWS endpoint host name says about a request.
type AWSHost struct {
	// Service is the endpoint prefix: "sqs", "dynamodb", "s3", "s3-control"...
	Service string
	// Region is empty for the global endpoints (iam, route53, sts, s3).
	Region string
	// Bucket is set for virtual-hosted-style S3 requests.
	Bucket string
	// AccountID is set for S3 Control ({account}.s3-control.{region}...).
	AccountID string
}

// hostSuffixes are the domains we treat as AWS endpoints. The sandboxes point
// *.amazonaws.com at opensnack; OPENSNACK_HOST_SUFFIXES adds more (comma
// separated, e.g. "opensnack.test") for local work without DNS tricks.
func hostSuffixes() []string {
	out := []string{"amazonaws.com"}
	for _, s := range strings.Split(os.Getenv("OPENSNACK_HOST_SUFFIXES"), ",") {
		if s = strings.Trim(strings.TrimSpace(s), "."); s != "" {
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}

// HostSuffixes is amazonaws.com plus OPENSNACK_HOST_SUFFIXES, for the data
// planes that recognise their own host names ({id}.execute-api..., {id}.cloudfront...).
func HostSuffixes() []string { return hostSuffixes() }

// ParseAWSHost recognises the AWS endpoint forms:
//
//	{service}.{region}.amazonaws.com
//	{service}.amazonaws.com                    (iam, route53, sts, s3)
//	{bucket}.s3.{region}.amazonaws.com         (also s3.dualstack., s3-{region})
//	{bucket}.s3.amazonaws.com
//	{account}.s3-control.{region}.amazonaws.com
//
// It returns false for anything else (localhost, an IP, a bare name).
func ParseAWSHost(hostport string) (AWSHost, bool) {
	host := strings.ToLower(hostport)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")

	var prefix string
	for _, suffix := range hostSuffixes() {
		if p, ok := strings.CutSuffix(host, "."+suffix); ok {
			prefix = p
			break
		}
	}
	if prefix == "" {
		return AWSHost{}, false
	}
	labels := strings.Split(prefix, ".")
	n := len(labels)

	// {account}.s3-control.{region}
	if n >= 3 && labels[n-2] == "s3-control" {
		return AWSHost{
			Service:   "s3-control",
			Region:    labels[n-1],
			AccountID: strings.Join(labels[:n-2], "."),
		}, true
	}

	// S3: find the "s3" (or legacy "s3-{region}") label from the right, since
	// bucket names can themselves contain dots.
	for i := n - 1; i >= 0 && i >= n-3; i-- {
		l := labels[i]
		var region string
		switch {
		case l == "s3":
		case strings.HasPrefix(l, "s3-") && l != "s3-control" && !strings.HasPrefix(l, "s3-website"):
			region = strings.TrimPrefix(l, "s3-")
			if region == "external-1" {
				region = "us-east-1"
			}
		default:
			continue
		}
		for _, rest := range labels[i+1:] {
			if rest != "dualstack" && rest != "fips" {
				region = rest
			}
		}
		return AWSHost{
			Service: "s3",
			Region:  region,
			Bucket:  strings.Join(labels[:i], "."),
		}, true
	}

	h := AWSHost{Service: labels[0]}
	if n >= 2 {
		h.Region = labels[n-1]
	}
	return h, true
}

// BaseURL is scheme://host as the client addressed us, for building URLs we
// hand back (SQS queue URLs). Requests to an AWS host name are always https
// from the client's side, even when a proxy terminated TLS in front of us.
func BaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if _, ok := ParseAWSHost(r.Host); ok {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}
