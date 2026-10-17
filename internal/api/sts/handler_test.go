// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sts_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/iam"
	"opensnack/internal/api/sts"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

func newCtx(method, target string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(method, target, strings.NewReader(""))
	return httptest.NewRecorder(), req
}

func TestGetCallerIdentity(t *testing.T) {
	h := sts.NewHandler(resource.NewMemStore(), nil)

	rec, req := newCtx("POST", "/sts?Action=GetCallerIdentity")

	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp sts.GetCallerIdentityResponse
	if xml.Unmarshal(rec.Body.Bytes(), &resp) != nil {
		t.Fatalf("invalid XML: %s", rec.Body.String())
	}

	if resp.GetCallerIdentityResult.Account != "000000000000" {
		t.Fatalf("wrong account: %s", resp.GetCallerIdentityResult.Account)
	}

	if !strings.Contains(rec.Body.String(), "<Arn>arn:aws:iam::000000000000:user/default</Arn>") {
		t.Fatalf("bad Arn: %s", rec.Body.String())
	}
}

// Each tenant sees their own account and identity.
func TestGetCallerIdentity_PerTenant(t *testing.T) {
	h := sts.NewHandler(resource.NewMemStore(), nil)
	rec, req := newCtx("POST", "/?Action=GetCallerIdentity")
	req = req.WithContext(util.WithIdentity(req.Context(), &util.Identity{
		AccessKeyID: "AKIATENANT", Namespace: "tenant-7", AccountID: "123456789012", Region: "eu-west-2",
	}))

	h.Dispatch(rec, req)

	body := rec.Body.String()
	for _, want := range []string{
		"<Account>123456789012</Account>",
		"<Arn>arn:aws:iam::123456789012:user/tenant-7</Arn>",
		"<UserId>tenant-7</UserId>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

// A service assuming a role on a tenant's behalf gets credentials in that
// tenant's namespace only, and they can be revoked.
func TestRoleSession(t *testing.T) {
	store := resource.NewMemStore()
	iamh := iam.NewHandler(store)
	form := url.Values{"Action": {"CreateRole"}, "RoleName": {"fn"}, "AssumeRolePolicyDocument": {`{"Version":"2012-10-17","Statement":[]}`}}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(util.WithIdentity(req.Context(), &util.Identity{Namespace: "tenant-a", AccountID: "111111111111"}))
	rec := httptest.NewRecorder()
	iamh.Dispatch(rec, req)
	if rec.Code != 200 {
		t.Fatalf("create role: %d %s", rec.Code, rec.Body.String())
	}

	h := sts.NewHandler(store, nil)
	sess, err := h.RoleSession("tenant-a", "111111111111", "arn:aws:iam::111111111111:role/fn", "myfn", 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := h.Sessions.Lookup(context.Background(), sess.Credential.AccessKeyID)
	if err != nil || cred.Namespace != "tenant-a" || cred.AccountID != "111111111111" || sess.Arn != "arn:aws:sts::111111111111:assumed-role/fn/myfn" {
		t.Fatalf("session: %+v %+v %v", sess, cred, err)
	}
	if time.Until(sess.Expires) < 119*time.Minute {
		t.Fatalf("expiry %s", sess.Expires)
	}
	// Another tenant's namespace, or another account, has no such role.
	for _, c := range [][2]string{{"tenant-b", "111111111111"}, {"tenant-a", "222222222222"}} {
		if _, err := h.RoleSession(c[0], c[1], "arn:aws:iam::111111111111:role/fn", "x", time.Hour); err == nil {
			t.Fatalf("%v: role assumed across tenants", c)
		}
	}
	h.Sessions.Revoke(sess.Credential.AccessKeyID)
	if _, err := h.Sessions.Lookup(context.Background(), sess.Credential.AccessKeyID); err == nil {
		t.Fatal("revoked session still verifies")
	}
}
