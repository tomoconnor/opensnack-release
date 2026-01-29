// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sts_test

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/api/sts"

	"github.com/labstack/echo/v4"
)

func newCtx(method, target string) (echo.Context, *httptest.ResponseRecorder, *echo.Echo) {
	e := echo.New()
	req := httptest.NewRequest(method, target, strings.NewReader(""))
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec, e
}

func TestGetCallerIdentity(t *testing.T) {
	h := sts.NewHandler()

	c, rec, _ := newCtx("POST", "/sts?Action=GetCallerIdentity")

	err := h.Dispatch(c)
	if err != nil {
		t.Fatal(err)
	}

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
