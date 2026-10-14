// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package util

import (
	"context"
	"net/http"
	"os"
	"strings"
)

// Identity is who a request is acting as, resolved once by the auth
// middleware from the SigV4 credential and read by every handler.
type Identity struct {
	AccessKeyID string
	// Namespace is the tenant: every resource the request touches lives in it.
	Namespace string
	AccountID string
	// Region comes from the SigV4 credential scope, falling back to the host
	// name and then to the configured default.
	Region string
	// SigningService is the service name from the credential scope ("s3",
	// "sqs", ...). The router uses it when the host name doesn't say.
	SigningService string
	// Authenticated is true when the signature was checked against a known
	// secret. Dev mode leaves it false.
	Authenticated bool
}

type identityKey struct{}

func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity the middleware attached, or one derived
// from the legacy User-Agent convention when the request never went through
// the middleware (handler unit tests call handlers directly).
func IdentityFrom(r *http.Request) *Identity {
	if id, ok := r.Context().Value(identityKey{}).(*Identity); ok && id != nil {
		return id
	}
	ns := NamespaceFromUserAgent(r)
	if ns == "" {
		ns = "default"
	}
	return &Identity{
		Namespace: ns,
		AccountID: DefaultAccountID(),
		Region:    DefaultRegion(),
	}
}

// NamespaceFromHeader returns the store scope for a regional service: the
// tenant namespace plus the request's region, so the same name can exist in
// two regions. Global services (IAM, Route 53, S3, STS) use GlobalNamespace.
func NamespaceFromHeader(r *http.Request) string {
	id := IdentityFrom(r)
	return RegionalScope(id.Namespace, id.Region)
}

// GlobalNamespace is the store scope for services whose names are global
// within an account: IAM, Route 53, S3 buckets, STS, CloudFront.
func GlobalNamespace(r *http.Request) string {
	return IdentityFrom(r).Namespace
}

// RegionalScope joins a namespace and region into the scope string the store
// splits back apart (see resource.SplitScope).
func RegionalScope(namespace, region string) string {
	return namespace + "@" + region
}

// AccountID is the AWS account the request's credentials belong to.
func AccountID(r *http.Request) string {
	return IdentityFrom(r).AccountID
}

// Region is the AWS region the request was signed for.
func Region(r *http.Request) string {
	return IdentityFrom(r).Region
}

// NamespaceFromUserAgent reads the legacy namespace convention: the last
// User-Agent token prefixed "custom-", which Terraform appends through
// TF_APPEND_USER_AGENT. Only dev mode honours it; it returns "" when absent.
func NamespaceFromUserAgent(r *http.Request) string {
	// User-Agent format: "APN/1.0 HashiCorp/1.0 Terraform/1.5.0 (+https://www.terraform.io) custom-namespace"
	parts := strings.Fields(r.Header.Get("User-Agent"))
	if len(parts) > 0 {
		if ns, ok := strings.CutPrefix(parts[len(parts)-1], "custom-"); ok {
			return ns
		}
	}
	return ""
}

// DefaultAccountID is the account used when the credential source doesn't
// assign one.
func DefaultAccountID() string {
	if v := os.Getenv("OPENSNACK_ACCOUNT_ID"); v != "" {
		return v
	}
	return "000000000000"
}

// DefaultRegion is used when neither the signature nor the host name names a
// region.
func DefaultRegion() string {
	if v := os.Getenv("OPENSNACK_DEFAULT_REGION"); v != "" {
		return v
	}
	return "us-east-1"
}
