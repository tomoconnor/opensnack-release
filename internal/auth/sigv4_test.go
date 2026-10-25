// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	testKey    = "AKIATENANTONE"
	testSecret = "s3cr3t/with+chars"
)

var testNow = time.Date(2026, 12, 1, 6, 0, 0, 0, time.UTC)

func sha(b string) string {
	s := sha256.Sum256([]byte(b))
	return hex.EncodeToString(s[:])
}

// signed builds a server-side request whose signature the real SDK signer
// produced, as it would arrive at opensnack.
func signed(t *testing.T, method, target, body, service, region string, hdr map[string]string, disableEscape bool) *http.Request {
	t.Helper()
	out, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		out.Header.Set(k, v)
	}
	payload := sha(body)
	if v := out.Header.Get("X-Amz-Content-Sha256"); v != "" {
		payload = v
	}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = disableEscape })
	err = signer.SignHTTP(context.Background(),
		aws.Credentials{AccessKeyID: testKey, SecretAccessKey: testSecret},
		out, payload, service, region, testNow)
	if err != nil {
		t.Fatal(err)
	}

	// Replay it through a server-side request, as net/http would parse it.
	in := httptest.NewRequest(method, out.URL.RequestURI(), strings.NewReader(body))
	in.Host = out.URL.Host
	for k, vs := range out.Header {
		in.Header[k] = vs
	}
	return in
}

func verify(r *http.Request) error {
	s, err := Parse(r)
	if err != nil {
		return err
	}
	return Verify(r, s, testSecret, testNow)
}

func TestVerify_SDKSignatures(t *testing.T) {
	cases := []struct {
		name, method, url, body, service string
		hdr                              map[string]string
		s3                               bool
	}{
		{"json at root", "POST", "https://sqs.eu-west-1.amazonaws.com/", `{"QueueName":"q"}`, "sqs",
			map[string]string{"X-Amz-Target": "AmazonSQS.CreateQueue", "Content-Type": "application/x-amz-json-1.0"}, false},
		{"query form", "POST", "https://iam.amazonaws.com/", "Action=ListRoles&Version=2010-05-08", "iam",
			map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, false},
		{"rest path needing double escape", "GET", "https://lambda.us-east-1.amazonaws.com/2015-03-31/functions/arn%3Aaws%3Alambda%3Aus-east-1%3A1%3Afunction%3Af", "", "lambda", nil, false},
		{"s3 virtual host with query", "GET", "https://my.bucket.s3.us-east-1.amazonaws.com/?list-type=2&prefix=a%20b&delimiter=%2F", "", "s3",
			map[string]string{"X-Amz-Content-Sha256": sha("")}, true},
		{"s3 key with spaces", "PUT", "https://s3.us-east-1.amazonaws.com/bkt/dir/a%20file%2Bplus.txt", "hello", "s3",
			map[string]string{"X-Amz-Content-Sha256": sha("hello")}, true},
		{"s3 unsigned payload", "PUT", "https://s3.amazonaws.com/bkt/state.tfstate", "{}", "s3",
			map[string]string{"X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := signed(t, c.method, c.url, c.body, c.service, "eu-west-1", c.hdr, c.s3)
			if err := verify(r); err != nil {
				t.Fatalf("verify: %v", err)
			}
			// The body must still be readable by the handler afterwards.
			if b, _ := io.ReadAll(r.Body); string(b) != c.body {
				t.Fatalf("body not restored: %q", b)
			}
		})
	}
}

func TestVerify_Rejects(t *testing.T) {
	t.Run("wrong secret", func(t *testing.T) {
		r := signed(t, "POST", "https://sqs.us-east-1.amazonaws.com/", "{}", "sqs", "us-east-1", nil, false)
		s, _ := Parse(r)
		if err := Verify(r, s, "nope", testNow); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("tampered body", func(t *testing.T) {
		r := signed(t, "POST", "https://sqs.us-east-1.amazonaws.com/", `{"QueueName":"a"}`, "sqs", "us-east-1", nil, false)
		r.Body = io.NopCloser(strings.NewReader(`{"QueueName":"b"}`))
		if err := verify(r); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("got %v", err)
		}
	})
	// S3 signs the x-amz-content-sha256 header, not the body; the body must
	// match it (AOTF-23).
	t.Run("tampered s3 body", func(t *testing.T) {
		r := signed(t, "PUT", "https://s3.amazonaws.com/bkt/key", "aaaa", "s3", "us-east-1",
			map[string]string{"X-Amz-Content-Sha256": sha("aaaa")}, true)
		r.Body = io.NopCloser(strings.NewReader("bbbb"))
		if err := verify(r); !errors.Is(err, ErrContentSHA256Mismatch) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("tampered large s3 body", func(t *testing.T) {
		good, bad := strings.Repeat("a", maxPrecheckedBody+1), strings.Repeat("b", maxPrecheckedBody+1)
		r := signed(t, "PUT", "https://s3.amazonaws.com/bkt/key", good, "s3", "us-east-1",
			map[string]string{"X-Amz-Content-Sha256": sha(good)}, true)
		if err := verify(r); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if b, err := io.ReadAll(r.Body); err != nil || string(b) != good {
			t.Fatalf("good large body: %v", err)
		}
		r = signed(t, "PUT", "https://s3.amazonaws.com/bkt/key", good, "s3", "us-east-1",
			map[string]string{"X-Amz-Content-Sha256": sha(good)}, true)
		r.Body = io.NopCloser(strings.NewReader(bad))
		if err := verify(r); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if _, err := io.ReadAll(r.Body); !errors.Is(err, ErrContentSHA256Mismatch) {
			t.Fatalf("read: %v", err)
		}
	})
	t.Run("tampered host", func(t *testing.T) {
		r := signed(t, "GET", "https://s3.amazonaws.com/bkt", "", "s3", "us-east-1",
			map[string]string{"X-Amz-Content-Sha256": sha("")}, true)
		r.Host = "evil.s3.amazonaws.com"
		if err := verify(r); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("clock skew", func(t *testing.T) {
		r := signed(t, "POST", "https://sqs.us-east-1.amazonaws.com/", "{}", "sqs", "us-east-1", nil, false)
		s, _ := Parse(r)
		if err := Verify(r, s, testSecret, testNow.Add(20*time.Minute)); !errors.Is(err, ErrExpired) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unsigned", func(t *testing.T) {
		if _, err := Parse(httptest.NewRequest("GET", "/", nil)); !errors.Is(err, ErrMissingAuth) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestVerify_Presigned(t *testing.T) {
	out, _ := http.NewRequest("GET", "https://bkt.s3.us-west-2.amazonaws.com/a%20b.txt?X-Amz-Expires=300", nil)
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	u, _, err := signer.PresignHTTP(context.Background(),
		aws.Credentials{AccessKeyID: testKey, SecretAccessKey: testSecret},
		out, "UNSIGNED-PAYLOAD", "s3", "us-west-2", testNow)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", strings.TrimPrefix(u, "https://bkt.s3.us-west-2.amazonaws.com"), nil)
	r.Host = "bkt.s3.us-west-2.amazonaws.com"

	s, err := Parse(r)
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessKeyID != testKey || s.Region != "us-west-2" {
		t.Fatalf("parsed %+v", s)
	}
	if err := Verify(r, s, testSecret, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := Verify(r, s, testSecret, testNow.Add(10*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired URL accepted: %v", err)
	}
}

func TestAuthenticator_StrictAndDev(t *testing.T) {
	src := NewTableSource([]Credential{{AccessKeyID: testKey, SecretAccessKey: testSecret, AccountID: "123456789012"}})
	strict := &Authenticator{Mode: ModeStrict, Source: src, Now: func() time.Time { return testNow }}

	r := signed(t, "POST", "https://sqs.eu-west-1.amazonaws.com/", "{}", "sqs", "eu-west-1",
		map[string]string{"X-Amz-Target": "AmazonSQS.ListQueues"}, false)
	id, err := strict.Resolve(r)
	if err != nil {
		t.Fatal(err)
	}
	if id.Namespace != testKey || id.AccountID != "123456789012" || id.Region != "eu-west-1" || !id.Authenticated {
		t.Fatalf("identity %+v", id)
	}

	// A User-Agent namespace must not let one tenant act as another.
	r = signed(t, "POST", "https://sqs.eu-west-1.amazonaws.com/", "{}", "sqs", "eu-west-1",
		map[string]string{"User-Agent": "Terraform custom-victim"}, false)
	if id, _ := strict.Resolve(r); id.Namespace != testKey {
		t.Fatalf("strict mode honoured the User-Agent namespace: %+v", id)
	}

	// Unknown key → InvalidClientTokenId over Query, UnrecognizedClient over JSON.
	other := &Authenticator{Mode: ModeStrict, Source: NewTableSource(nil), Now: strict.Now}
	for _, tc := range []struct {
		target, wantCode string
		wantStatus       int
	}{
		{"", "InvalidClientTokenId", 403},
		{"AmazonSQS.ListQueues", "UnrecognizedClientException", 400},
	} {
		hdr := map[string]string{}
		if tc.target != "" {
			hdr["X-Amz-Target"] = tc.target
		}
		r = signed(t, "POST", "https://sqs.eu-west-1.amazonaws.com/", "{}", "sqs", "eu-west-1", hdr, false)
		rec := httptest.NewRecorder()
		other.Middleware(http.NotFoundHandler()).ServeHTTP(rec, r)
		if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
			t.Fatalf("target %q: got %d %s", tc.target, rec.Code, rec.Body.String())
		}
	}

	// S3 error shape for a bad signature.
	r = signed(t, "GET", "https://s3.amazonaws.com/bkt", "", "s3", "us-east-1",
		map[string]string{"X-Amz-Content-Sha256": sha("")}, true)
	r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "Signature=", "Signature=00", 1))
	rec := httptest.NewRecorder()
	strict.Middleware(http.NotFoundHandler()).ServeHTTP(rec, r)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "<Code>SignatureDoesNotMatch</Code>") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}

	// Dev mode: anything goes, the legacy User-Agent namespace still works.
	dev := &Authenticator{Mode: ModeDev, Now: strict.Now}
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("User-Agent", "opensnack-test custom-ns1")
	if id, err := dev.Resolve(r); err != nil || id.Namespace != "ns1" || id.Region != "us-east-1" {
		t.Fatalf("dev identity %+v %v", id, err)
	}
}

func TestHTTPSource(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("access_key_id") != testKey {
			w.WriteHeader(404)
			return
		}
		io.WriteString(w, `{"secret_access_key":"`+testSecret+`","account_id":"111122223333","namespace":"tenant-1"}`)
	}))
	defer srv.Close()

	s := NewHTTPSource(srv.URL+"/creds", "tok")
	for range 3 {
		c, err := s.Lookup(context.Background(), testKey)
		if err != nil || c.SecretAccessKey != testSecret || c.Namespace != "tenant-1" || c.AccessKeyID != testKey {
			t.Fatalf("%+v %v", c, err)
		}
	}
	if _, err := s.Lookup(context.Background(), "AKIAUNKNOWN"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected the cache to absorb repeat lookups, got %d calls", calls)
	}
}
