// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package awsresponses_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/awsresponses"
)

func newRec() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}

func TestWriteEmpty200(t *testing.T) {
	rec := newRec()

	err := awsresponses.WriteEmpty200(rec, map[string]string{"Location": "/test"})
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
	rec := newRec()

	err := awsresponses.WriteEmpty204(rec)
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

// WriteErrorXML emits the Query API shape, which wraps the error in
// <ErrorResponse> and carries no <Resource>. WriteS3ErrorXML is the one that
// reports the resource.
func TestWriteErrorXML(t *testing.T) {
	rec := newRec()

	err := awsresponses.WriteErrorXML(rec, 404, "NoSuchBucket", "Bucket does not exist", "foo")
	if err != nil {
		t.Fatal(err)
	}

	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<ErrorResponse>",
		"<Type>Sender</Type>",
		"<Code>NoSuchBucket</Code>",
		"<Message>Bucket does not exist</Message>",
		"<RequestId>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %s in error: %s", want, body)
		}
	}
}

func TestWriteS3ErrorXML(t *testing.T) {
	rec := newRec()

	err := awsresponses.WriteS3ErrorXML(rec, 404, "NoSuchBucket", "Bucket does not exist", "foo")
	if err != nil {
		t.Fatal(err)
	}

	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<Code>NoSuchBucket</Code>",
		"<Message>Bucket does not exist</Message>",
		"<Resource>foo</Resource>",
		"<RequestId>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %s in error: %s", want, body)
		}
	}
}
