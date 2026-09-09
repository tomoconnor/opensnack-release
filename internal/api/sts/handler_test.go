// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sts_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/api/sts"
)

func newCtx(method, target string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(method, target, strings.NewReader(""))
	return httptest.NewRecorder(), req
}

func TestGetCallerIdentity(t *testing.T) {
	h := sts.NewHandler()

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

	if !strings.Contains(rec.Body.String(), "<Arn>arn:aws:iam::000000000000:user/opensnack</Arn>") {
		t.Fatalf("bad Arn: %s", rec.Body.String())
	}
}
