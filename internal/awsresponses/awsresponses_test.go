// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package awsresponses_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/awsresponses"

	"github.com/labstack/echo/v4"
)

func newCtx(method, path string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	return ctx, rec
}

func TestWriteEmpty200(t *testing.T) {
	c, rec := newCtx("GET", "/")

	err := awsresponses.WriteEmpty200(c, map[string]string{"Location": "/test"})
	if err != nil {
		t.Fatal(err)
	}

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", rec.Body.String())
	}
	if rec.Header().Get("Location") != "/test" {
		t.Fatalf("missing Location header")
	}
}

func TestWriteEmpty204(t *testing.T) {
	c, rec := newCtx("DELETE", "/xyz")

	err := awsresponses.WriteEmpty204(c)
	if err != nil {
		t.Fatal(err)
	}

	if rec.Code != 204 {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body should be empty")
	}
}

func TestRequestIDSequence(t *testing.T) {
	id1 := awsresponses.NextRequestID()
	id2 := awsresponses.NextRequestID()

	if id1 == id2 {
		t.Fatalf("request IDs must be unique")
	}
}

func TestWriteErrorXML(t *testing.T) {
	c, rec := newCtx("GET", "/bad")

	err := awsresponses.WriteErrorXML(c, 404, "NoSuchBucket", "Bucket does not exist", "foo")
	if err != nil {
		t.Fatal(err)
	}

	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<Code>NoSuchBucket</Code>") {
		t.Fatalf("expected NoSuchBucket in error: %s", body)
	}
	if !strings.Contains(body, "<Resource>foo</Resource>") {
		t.Fatalf("expected Resource in error")
	}
}
