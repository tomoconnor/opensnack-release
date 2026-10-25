// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	algorithm       = "AWS4-HMAC-SHA256"
	timeFormat      = "20060102T150405Z"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	// maxSkew is how far X-Amz-Date may be from the server clock, as in AWS.
	maxSkew = 15 * time.Minute
)

var (
	ErrMissingAuth   = errors.New("missing authentication")
	ErrMalformedAuth = errors.New("malformed authorization")
	ErrBadSignature  = errors.New("signature does not match")
	ErrExpired       = errors.New("request time too skewed or expired")
	// ErrContentSHA256Mismatch is a body that isn't the one whose hash was
	// signed. Large bodies are checked as they're read, so a handler sees it
	// from the read that reaches the end of the body.
	ErrContentSHA256Mismatch = errors.New("x-amz-content-sha256 does not match the body")
)

// maxPrecheckedBody is the largest body hashed before the handler runs.
// Configuration bodies are all far smaller, and some handlers ignore read
// errors; object uploads are checked as they stream (see hashedBody).
const maxPrecheckedBody = 1 << 20

// SignedRequest is the parsed SigV4 material from a request, from either the
// Authorization header or a presigned URL's query string.
type SignedRequest struct {
	AccessKeyID   string
	Date          string // yyyymmdd from the credential scope
	Region        string
	Service       string
	SignedHeaders []string
	Signature     string
	AmzDate       time.Time
	Presigned     bool
	Expires       time.Duration // presigned only
}

func (s *SignedRequest) scope() string {
	return s.Date + "/" + s.Region + "/" + s.Service + "/aws4_request"
}

// Parse extracts the SigV4 fields. It returns ErrMissingAuth when the request
// isn't signed at all.
func Parse(r *http.Request) (*SignedRequest, error) {
	if auth := r.Header.Get("Authorization"); auth != "" {
		return parseHeader(r, auth)
	}
	if r.URL.Query().Get("X-Amz-Algorithm") != "" {
		return parsePresigned(r)
	}
	return nil, ErrMissingAuth
}

func parseHeader(r *http.Request, auth string) (*SignedRequest, error) {
	if !strings.HasPrefix(auth, algorithm+" ") {
		return nil, ErrMalformedAuth
	}
	s := &SignedRequest{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, algorithm+" "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "Credential":
			if err := s.setCredential(v); err != nil {
				return nil, err
			}
		case "SignedHeaders":
			s.SignedHeaders = strings.Split(v, ";")
		case "Signature":
			s.Signature = v
		}
	}
	if s.AccessKeyID == "" || s.Signature == "" || len(s.SignedHeaders) == 0 {
		return nil, ErrMalformedAuth
	}

	amz := r.Header.Get("X-Amz-Date")
	if amz == "" {
		// Some clients sign the plain Date header instead.
		t, err := http.ParseTime(r.Header.Get("Date"))
		if err != nil {
			return nil, ErrMalformedAuth
		}
		s.AmzDate = t.UTC()
	} else {
		t, err := time.Parse(timeFormat, amz)
		if err != nil {
			return nil, ErrMalformedAuth
		}
		s.AmzDate = t
	}
	return s, nil
}

func parsePresigned(r *http.Request) (*SignedRequest, error) {
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") != algorithm {
		return nil, ErrMalformedAuth
	}
	s := &SignedRequest{Presigned: true}
	if err := s.setCredential(q.Get("X-Amz-Credential")); err != nil {
		return nil, err
	}
	s.SignedHeaders = strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	s.Signature = q.Get("X-Amz-Signature")
	t, err := time.Parse(timeFormat, q.Get("X-Amz-Date"))
	if err != nil || s.Signature == "" {
		return nil, ErrMalformedAuth
	}
	s.AmzDate = t
	secs, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil || secs < 0 {
		return nil, ErrMalformedAuth
	}
	s.Expires = time.Duration(secs) * time.Second
	return s, nil
}

func (s *SignedRequest) setCredential(v string) error {
	// AKID/20260101/us-east-1/s3/aws4_request
	parts := strings.Split(v, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" || parts[0] == "" {
		return ErrMalformedAuth
	}
	s.AccessKeyID, s.Date, s.Region, s.Service = parts[0], parts[1], parts[2], parts[3]
	return nil
}

// Verify checks the signature against secret. For requests without an
// x-amz-content-sha256 header it reads (and restores) the body to hash it.
func Verify(r *http.Request, s *SignedRequest, secret string, now time.Time) error {
	if s.Presigned {
		if now.Before(s.AmzDate.Add(-maxSkew)) || now.After(s.AmzDate.Add(s.Expires)) {
			return ErrExpired
		}
	} else if d := now.Sub(s.AmzDate); d > maxSkew || d < -maxSkew {
		return ErrExpired
	}

	payloadHash, err := payloadHash(r, s)
	if err != nil {
		return err
	}

	key := signingKey(secret, s.Date, s.Region, s.Service)
	headers := canonicalHeaders(r, s.SignedHeaders)
	query := canonicalQuery(r.URL.RawQuery, s.Presigned)
	want, err := hex.DecodeString(s.Signature)
	if err != nil {
		return ErrBadSignature
	}

	// Clients disagree slightly on how to encode the path (S3 signs it
	// encoded once, every other service twice, and SDKs differ on which
	// characters they escape), so accept any of the plausible forms.
	for _, uri := range canonicalURIs(r, s.Service) {
		creq := strings.Join([]string{
			r.Method,
			uri,
			query,
			headers,
			strings.Join(s.SignedHeaders, ";"),
			payloadHash,
		}, "\n")
		sts := strings.Join([]string{
			algorithm,
			s.AmzDate.Format(timeFormat),
			s.scope(),
			hexSHA256([]byte(creq)),
		}, "\n")
		if hmac.Equal(hmacSHA256(key, []byte(sts)), want) {
			return checkPayload(r, payloadHash)
		}
	}
	return ErrBadSignature
}

func payloadHash(r *http.Request, s *SignedRequest) (string, error) {
	if s.Presigned {
		// Presigned URLs carry no body hash; S3 signs them UNSIGNED-PAYLOAD.
		if v := r.URL.Query().Get("X-Amz-Content-Sha256"); v != "" {
			return v, nil
		}
		return unsignedPayload, nil
	}
	// S3 always sends the hash (or a STREAMING-*/UNSIGNED marker); the
	// signature covers the header value, so use it as given.
	if v := r.Header.Get("X-Amz-Content-Sha256"); v != "" {
		return v, nil
	}
	if r.Body == nil || r.Body == http.NoBody {
		return hexSHA256(nil), nil
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return hexSHA256(body), nil
}

// checkPayload holds the body to the signed x-amz-content-sha256, when that
// is a digest rather than UNSIGNED-PAYLOAD or a STREAMING-* marker. Without
// it the signature covers the headers but not the body (AOTF-23).
func checkPayload(r *http.Request, signed string) error {
	want, err := hex.DecodeString(signed)
	if err != nil || len(want) != sha256.Size || r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	if r.ContentLength < 0 || r.ContentLength > maxPrecheckedBody {
		r.Body = &hashedBody{ReadCloser: r.Body, h: sha256.New(), want: want}
		return nil
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if got := sha256.Sum256(body); !hmac.Equal(got[:], want) {
		return ErrContentSHA256Mismatch
	}
	return nil
}

// hashedBody hashes a body as it's read and fails the read that reaches its
// end if the hash isn't the signed one.
type hashedBody struct {
	io.ReadCloser
	h    hash.Hash
	want []byte
	err  error
}

func (b *hashedBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	n, err := b.ReadCloser.Read(p)
	b.h.Write(p[:n])
	if err == io.EOF && !hmac.Equal(b.h.Sum(nil), b.want) {
		b.err = ErrContentSHA256Mismatch
		return n, b.err
	}
	return n, err
}

func signingKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte("aws4_request"))
}

func canonicalHeaders(r *http.Request, signed []string) string {
	var b strings.Builder
	for _, name := range signed {
		var v string
		switch name {
		case "host":
			v = r.Host
		case "content-length":
			v = r.Header.Get("Content-Length")
			if v == "" && r.ContentLength >= 0 {
				v = strconv.FormatInt(r.ContentLength, 10)
			}
		case "transfer-encoding":
			v = strings.Join(r.TransferEncoding, ",")
		default:
			vals := r.Header.Values(name)
			for i := range vals {
				vals[i] = collapseSpaces(vals[i])
			}
			v = strings.Join(vals, ",")
		}
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(collapseSpaces(v))
		b.WriteByte('\n')
	}
	return b.String()
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// canonicalQuery sorts the query parameters and re-encodes them the AWS way.
// A presigned URL's own signature is left out.
func canonicalQuery(raw string, presigned bool) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, p := range strings.Split(raw, "&") {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		dk, err := url.QueryUnescape(k)
		if err != nil {
			dk = k
		}
		dv, err := url.QueryUnescape(v)
		if err != nil {
			dv = v
		}
		if presigned && dk == "X-Amz-Signature" {
			continue
		}
		pairs = append(pairs, kv{awsEscape(dk, true), awsEscape(dv, true)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

// canonicalURIs returns the candidate canonical paths for this request.
func canonicalURIs(r *http.Request, service string) []string {
	raw := rawPath(r)
	if raw == "" {
		raw = "/"
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		decoded = raw
	}
	normalized := awsEscape(decoded, false)

	var out []string
	add := func(s string) {
		for _, o := range out {
			if o == s {
				return
			}
		}
		out = append(out, s)
	}
	if service == "s3" {
		add(raw)
		add(normalized)
	} else {
		add(awsEscape(raw, false))
		add(awsEscape(normalized, false))
		add(normalized)
	}
	return out
}

// rawPath is the path exactly as the client sent it, before Go decodes it.
func rawPath(r *http.Request) string {
	uri := r.RequestURI
	if uri == "" {
		return r.URL.EscapedPath()
	}
	if i := strings.Index(uri, "://"); i >= 0 {
		// Absolute-form request target: skip the scheme and authority.
		rest := uri[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			uri = rest[j:]
		} else {
			uri = "/"
		}
	}
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		uri = uri[:i]
	}
	return uri
}

// awsEscape percent-encodes everything except the unreserved characters
// (A-Z a-z 0-9 - _ . ~), and '/' too unless encodeSlash is set.
func awsEscape(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
