// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"opensnack/internal/auth"
	"opensnack/internal/resource"
	"opensnack/internal/router"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// sigv4Call signs a Query API request with the SDK signer and serves it.
func sigv4Call(t *testing.T, h http.Handler, creds aws.Credentials, service, host string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	body := form.Encode()
	out, _ := http.NewRequest("POST", "https://"+host+"/", strings.NewReader(body))
	out.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sum := sha256.Sum256([]byte(body))
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, out, hex.EncodeToString(sum[:]),
		service, "us-east-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	in := httptest.NewRequest("POST", "/", strings.NewReader(body))
	in.Host = host
	for k, vs := range out.Header {
		in.Header[k] = vs
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, in)
	return rec
}

// AssumeRole issues keys that verify in strict mode, act in the caller's
// namespace and account, and identify as the assumed role.
func TestRouter_AssumeRoleRoundTrip(t *testing.T) {
	store := resource.NewMemStore()
	authn := &auth.Authenticator{
		Mode: auth.ModeStrict,
		Now:  time.Now,
		Source: auth.NewTableSource([]auth.Credential{{
			AccessKeyID: "AKIATENANT7", SecretAccessKey: "tenant-secret",
			AccountID: "123456789012", Namespace: "tenant-7",
		}}),
	}
	h := router.NewWithConfig(store, router.Config{Auth: authn})
	tenant := aws.Credentials{AccessKeyID: "AKIATENANT7", SecretAccessKey: "tenant-secret"}

	rec := sigv4Call(t, h, tenant, "iam", "iam.amazonaws.com", url.Values{
		"Action": {"CreateRole"}, "Version": {"2010-05-08"}, "RoleName": {"deployer"},
		"AssumeRolePolicyDocument": {`{"Version":"2012-10-17","Statement":[]}`},
	})
	if rec.Code != 200 {
		t.Fatalf("CreateRole: %d %s", rec.Code, rec.Body.String())
	}

	assume := url.Values{
		"Action": {"AssumeRole"}, "Version": {"2011-06-15"},
		"RoleArn": {"arn:aws:iam::123456789012:role/deployer"}, "RoleSessionName": {"worker"},
	}
	rec = sigv4Call(t, h, tenant, "sts", "sts.amazonaws.com", assume)
	if rec.Code != 200 {
		t.Fatalf("AssumeRole: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result struct {
			Credentials struct {
				AccessKeyId, SecretAccessKey, SessionToken, Expiration string
			}
			AssumedRoleUser struct{ Arn string }
		} `xml:"AssumeRoleResult"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v %s", err, rec.Body.String())
	}
	c := resp.Result.Credentials
	if !strings.HasPrefix(c.AccessKeyId, "ASIA") || c.SecretAccessKey == "" || c.SessionToken == "" {
		t.Fatalf("bad credentials: %+v", c)
	}
	if exp, err := time.Parse(time.RFC3339, c.Expiration); err != nil || time.Until(exp) < 59*time.Minute {
		t.Fatalf("bad expiration %q", c.Expiration)
	}
	wantArn := "arn:aws:sts::123456789012:assumed-role/deployer/worker"
	if resp.Result.AssumedRoleUser.Arn != wantArn {
		t.Fatalf("AssumedRoleUser.Arn = %s", resp.Result.AssumedRoleUser.Arn)
	}

	temp := aws.Credentials{AccessKeyID: c.AccessKeyId, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken}
	rec = sigv4Call(t, h, temp, "sts", "sts.amazonaws.com", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}})
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "<Arn>"+wantArn+"</Arn>") ||
		!strings.Contains(body, "<Account>123456789012</Account>") || !strings.Contains(body, ":worker</UserId>") {
		t.Fatalf("GetCallerIdentity with temporary keys: %d %s", rec.Code, body)
	}

	// The temporary keys see the tenant's resources.
	rec = sigv4Call(t, h, temp, "iam", "iam.amazonaws.com", url.Values{"Action": {"GetRole"}, "Version": {"2010-05-08"}, "RoleName": {"deployer"}})
	if rec.Code != 200 {
		t.Fatalf("GetRole with temporary keys: %d %s", rec.Code, rec.Body.String())
	}

	// A wrong secret for the temporary key is still rejected.
	bad := temp
	bad.SecretAccessKey = "nope"
	if rec := sigv4Call(t, h, bad, "sts", "sts.amazonaws.com", url.Values{"Action": {"GetCallerIdentity"}}); rec.Code != 403 ||
		!strings.Contains(rec.Body.String(), "SignatureDoesNotMatch") {
		t.Fatalf("bad secret: %d %s", rec.Code, rec.Body.String())
	}

	// Roles that don't exist can't be assumed.
	assume.Set("RoleArn", "arn:aws:iam::123456789012:role/ghost")
	if rec := sigv4Call(t, h, tenant, "sts", "sts.amazonaws.com", assume); rec.Code != 403 ||
		!strings.Contains(rec.Body.String(), "AccessDenied") {
		t.Fatalf("missing role: %d %s", rec.Code, rec.Body.String())
	}

	// GetSessionToken keys act as the tenant.
	rec = sigv4Call(t, h, tenant, "sts", "sts.amazonaws.com", url.Values{"Action": {"GetSessionToken"}, "Version": {"2011-06-15"}})
	var st struct {
		Credentials struct{ AccessKeyId, SecretAccessKey, SessionToken string } `xml:"GetSessionTokenResult>Credentials"`
	}
	if rec.Code != 200 || xml.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("GetSessionToken: %d %s", rec.Code, rec.Body.String())
	}
	sc := st.Credentials
	rec = sigv4Call(t, h, aws.Credentials{AccessKeyID: sc.AccessKeyId, SecretAccessKey: sc.SecretAccessKey, SessionToken: sc.SessionToken},
		"sts", "sts.amazonaws.com", url.Values{"Action": {"GetCallerIdentity"}})
	if !strings.Contains(rec.Body.String(), "<Arn>arn:aws:iam::123456789012:user/tenant-7</Arn>") {
		t.Fatalf("GetCallerIdentity with session token: %d %s", rec.Code, rec.Body.String())
	}
}
