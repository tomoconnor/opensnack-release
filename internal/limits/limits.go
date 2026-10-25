// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package limits keeps one tenant from exhausting opensnack for the others:
// request and object size caps, a per-namespace resource cap and S3 byte
// quota, and a per-namespace request rate. Errors use the codes AWS SDKs
// understand (the throttling ones are retried automatically).
package limits

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensnack/internal/auth"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// Config holds the caps. Zero disables a cap.
type Config struct {
	// MaxRequestBytes caps every request body except S3 object uploads.
	MaxRequestBytes int64
	// MaxObjectBytes caps one S3 object upload.
	MaxObjectBytes int64
	// MaxResources caps the resources one namespace may hold. When it is
	// reached, creates are refused but updates and deletes still work, so a
	// tenant can always clean up.
	MaxResources int
	// MaxS3Bytes caps the total size of a namespace's S3 objects.
	MaxS3Bytes int64
	// RatePerSecond and Burst throttle each namespace.
	RatePerSecond float64
	Burst         int
}

// FromEnv reads OPENSNACK_MAX_REQUEST_BYTES (default 80 MiB, room for a
// base64 50 MB Lambda zip), OPENSNACK_MAX_OBJECT_BYTES (default 100 MiB),
// OPENSNACK_MAX_RESOURCES, OPENSNACK_MAX_S3_BYTES, OPENSNACK_RATE_LIMIT
// (requests per second) and OPENSNACK_RATE_BURST (default 2x the rate).
// The last four are off unless set.
func FromEnv() Config {
	c := Config{
		MaxRequestBytes: envInt("OPENSNACK_MAX_REQUEST_BYTES", 80<<20),
		MaxObjectBytes:  envInt("OPENSNACK_MAX_OBJECT_BYTES", 100<<20),
		MaxResources:    int(envInt("OPENSNACK_MAX_RESOURCES", 0)),
		MaxS3Bytes:      envInt("OPENSNACK_MAX_S3_BYTES", 0),
	}
	if v, err := strconv.ParseFloat(os.Getenv("OPENSNACK_RATE_LIMIT"), 64); err == nil && v > 0 {
		c.RatePerSecond = v
		c.Burst = int(envInt("OPENSNACK_RATE_BURST", int64(2*v)))
	}
	return c
}

func envInt(name string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(name), 10, 64); err == nil {
		return v
	}
	return def
}

// usageTTL is how long a namespace's counted usage is trusted before it is
// recounted. In between, successful writes adjust it (see recordWrite), and
// uploads in flight hold their bytes against the S3 cap (see reserve).
// Concurrent creates can still overshoot the resource cap slightly.
const usageTTL = 5 * time.Second

type Limiter struct {
	cfg  Config
	insp resource.Inspector // nil disables the resource and byte caps
	now  func() time.Time

	mu       sync.Mutex
	usage    map[string]usage
	inflight map[string]int64 // S3 bytes reserved by uploads still running
	buckets  map[string]*bucket
}

type usage struct {
	resources int
	s3Bytes   int64
	at        time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func New(cfg Config, insp resource.Inspector) *Limiter {
	return &Limiter{
		cfg:      cfg,
		insp:     insp,
		now:      time.Now,
		usage:    map[string]usage{},
		inflight: map[string]int64{},
		buckets:  map[string]*bucket{},
	}
}

// Middleware must run after authentication, which decides the namespace.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_opensnack/") {
			next.ServeHTTP(w, r)
			return
		}
		ns := util.IdentityFrom(r).Namespace

		if !l.allow(ns) {
			throttled(w, r)
			return
		}

		upload := isObjectUpload(r)
		max := l.cfg.MaxRequestBytes
		if upload {
			max = l.cfg.MaxObjectBytes
		}
		// The body's length without aws-chunked framing; -1 if unknown.
		size := r.ContentLength
		if v, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64); err == nil {
			size = v
		}
		if max > 0 {
			if size > max {
				tooLarge(w, r, max)
				return
			}
			// Bodies of unknown length are cut off at the cap (with some slack
			// for aws-chunked framing); the handler then fails the read.
			r.Body = http.MaxBytesReader(w, r.Body, max+max/8+4096)
		}

		capped := (l.cfg.MaxResources > 0 || l.cfg.MaxS3Bytes > 0) && l.insp != nil
		create := capped && creates(r)
		var reserved int64
		if create {
			u, err := l.usageOf(ns)
			if err == nil {
				if l.cfg.MaxResources > 0 && u.resources >= l.cfg.MaxResources {
					limitExceeded(w, r, "You have reached the limit of "+strconv.Itoa(l.cfg.MaxResources)+
						" resources. Delete some before creating more.")
					return
				}
				if upload && l.cfg.MaxS3Bytes > 0 {
					// A body of unknown length may be as large as the object cap.
					n := size
					if n < 0 {
						n = max64(max, 0)
					}
					if !l.reserve(ns, u, n) {
						limitExceeded(w, r, "Your S3 objects would exceed the storage quota of "+
							strconv.FormatInt(l.cfg.MaxS3Bytes, 10)+" bytes.")
						return
					}
					reserved = n
				}
			}
		}

		if !capped || r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		rw := &statusWriter{ResponseWriter: w}
		defer func() { l.recordWrite(ns, rw.status < 300, create, upload, reserved, size) }()
		next.ServeHTTP(rw, r)
	})
}

// reserve holds n bytes against the namespace's S3 cap while an upload runs,
// so a burst of uploads can't all pass the same check (AOTF-22). u is the
// usage just counted, in case a concurrent write has dropped the cached one.
func (l *Limiter) reserve(ns string, u usage, n int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur, ok := l.usage[ns]; ok {
		u = cur
	}
	if u.s3Bytes+l.inflight[ns]+n > l.cfg.MaxS3Bytes {
		return false
	}
	l.inflight[ns] += n
	return true
}

// recordWrite releases an upload's reservation and keeps the cached usage
// honest between recounts: a create counts as one more resource (and its
// bytes, for an upload of known size), and any other write (a delete, say)
// or an upload of unknown size drops the cached figure so the next check
// recounts.
func (l *Limiter) recordWrite(ns string, ok, create, upload bool, reserved, size int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if reserved > 0 {
		if l.inflight[ns] -= reserved; l.inflight[ns] <= 0 {
			delete(l.inflight, ns)
		}
	}
	if !ok {
		return
	}
	u, cached := l.usage[ns]
	if !cached {
		return
	}
	if !create || (upload && size < 0) {
		delete(l.usage, ns)
		return
	}
	u.resources++
	if upload {
		u.s3Bytes += size
	}
	l.usage[ns] = u
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the connection (to extend a
// write deadline, say).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// allow takes a token from the namespace's bucket.
func (l *Limiter) allow(ns string) bool {
	if l.cfg.RatePerSecond <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ns]
	if !ok {
		b = &bucket{tokens: float64(l.cfg.Burst), last: now}
		l.buckets[ns] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.cfg.RatePerSecond
	if burst := float64(max(l.cfg.Burst, 1)); b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *Limiter) usageOf(ns string) (usage, error) {
	now := l.now()
	l.mu.Lock()
	u, ok := l.usage[ns]
	l.mu.Unlock()
	if ok && now.Sub(u.at) < usageTTL {
		return u, nil
	}

	by, err := l.insp.Count(ns)
	if err != nil {
		return usage{}, err
	}
	u = usage{at: now}
	for _, n := range by {
		u.resources += n
	}
	if l.cfg.MaxS3Bytes > 0 {
		if u.s3Bytes, err = l.insp.S3Bytes(ns); err != nil {
			return usage{}, err
		}
	}
	l.mu.Lock()
	l.usage[ns] = u
	l.mu.Unlock()
	return u, nil
}

// isObjectUpload is an S3 PutObject or UploadPart: a PUT to a key.
func isObjectUpload(r *http.Request) bool {
	if r.Method != http.MethodPut || auth.Protocol(r) != "s3" {
		return false
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if h, ok := util.ParseAWSHost(r.Host); ok && h.Bucket != "" {
		return path != ""
	}
	_, key, _ := strings.Cut(path, "/")
	return key != ""
}

// createPrefixes are the operation-name prefixes of calls that can add a
// resource. Deletes, updates, reads and tagging stay allowed at the cap.
var createPrefixes = []string{
	"Create", "Put", "Run", "Allocate", "Import", "Register", "Associate",
	"Attach", "Add", "Copy", "Subscribe", "Authorize", "Request", "Publish",
	"Send", "Upload", "Batch", "Transact",
}

// creates reports whether the request might add a resource.
func creates(r *http.Request) bool {
	if t := r.Header.Get("X-Amz-Target"); t != "" {
		_, op, _ := strings.Cut(t, ".")
		return hasCreatePrefix(op)
	}
	switch auth.Protocol(r) {
	case "s3":
		// PUT of a bucket or object; POST ?delete is DeleteObjects.
		_, del := r.URL.Query()["delete"]
		return r.Method == http.MethodPut || (r.Method == http.MethodPost && !del)
	case "rest-json":
		return r.Method == http.MethodPost || r.Method == http.MethodPut
	}
	if op := queryAction(r); op != "" {
		return hasCreatePrefix(op)
	}
	// Route 53 and other REST-XML APIs: record changes may be deletes, so
	// only treat zone and health check creation as creates.
	return r.Method == http.MethodPost && !strings.Contains(r.URL.Path, "/rrset")
}

func hasCreatePrefix(op string) bool {
	for _, p := range createPrefixes {
		if strings.HasPrefix(op, p) {
			return true
		}
	}
	return false
}

// queryAction reads Action from the query string or a form body, putting
// the body back for the handler.
func queryAction(r *http.Request) string {
	if a := r.URL.Query().Get("Action"); a != "" {
		return a
	}
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return ""
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	v, _ := url.ParseQuery(string(body))
	return v.Get("Action")
}

func throttled(w http.ResponseWriter, r *http.Request) {
	switch auth.Protocol(r) {
	case "s3":
		auth.WriteError(w, r, http.StatusServiceUnavailable, "SlowDown", "Please reduce your request rate.")
	case "json":
		auth.WriteError(w, r, http.StatusBadRequest, "ThrottlingException", "Rate exceeded")
	case "rest-json":
		auth.WriteError(w, r, http.StatusTooManyRequests, "TooManyRequestsException", "Rate exceeded")
	default:
		auth.WriteError(w, r, http.StatusBadRequest, "Throttling", "Rate exceeded")
	}
}

func tooLarge(w http.ResponseWriter, r *http.Request, max int64) {
	msg := "Your request exceeds the maximum allowed size of " + strconv.FormatInt(max, 10) + " bytes."
	if auth.Protocol(r) == "s3" {
		auth.WriteError(w, r, http.StatusBadRequest, "EntityTooLarge", msg)
		return
	}
	auth.WriteError(w, r, http.StatusRequestEntityTooLarge, "RequestEntityTooLargeException", msg)
}

func limitExceeded(w http.ResponseWriter, r *http.Request, msg string) {
	switch auth.Protocol(r) {
	case "s3":
		code := "QuotaExceeded"
		if !isObjectUpload(r) && r.Method == http.MethodPut {
			code = "TooManyBuckets"
		}
		auth.WriteError(w, r, http.StatusBadRequest, code, msg)
	case "json", "rest-json":
		auth.WriteError(w, r, http.StatusBadRequest, "LimitExceededException", msg)
	default:
		auth.WriteError(w, r, http.StatusBadRequest, "LimitExceeded", msg)
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
