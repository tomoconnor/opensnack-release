// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/egress"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"go.uber.org/zap"
)

// Object is an S3 object as an origin sees it.
type Object struct {
	Body         []byte
	ContentType  string
	ETag         string
	LastModified time.Time
	CacheControl string
}

// ObjectReader reads S3 objects for S3 origins. It returns os.ErrNotExist
// for a missing bucket or key. The router wires the S3 service in.
type ObjectReader interface {
	GetObject(namespace, bucket, key string) (*Object, error)
}

type ObjectReaderFunc func(namespace, bucket, key string) (*Object, error)

func (f ObjectReaderFunc) GetObject(namespace, bucket, key string) (*Object, error) {
	return f(namespace, bucket, key)
}

// maxOriginBody caps what we read from an origin.
const maxOriginBody = 64 << 20

// ParseHost recognises {id}.cloudfront.net and, for local work,
// {id}.cloudfront.{suffix} for each OPENSNACK_HOST_SUFFIXES entry. It returns
// the distribution ID.
func ParseHost(hostport string) (string, bool) {
	host := strings.ToLower(hostport)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	label, rest, ok := strings.Cut(strings.TrimSuffix(host, "."), ".cloudfront.")
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	if rest == "net" {
		return strings.ToUpper(label), true
	}
	for _, s := range util.HostSuffixes() {
		if rest == s && s != "amazonaws.com" {
			return strings.ToUpper(label), true
		}
	}
	return "", false
}

// lookupDistribution finds a distribution by ID across tenants: viewers
// don't sign requests, so the host name is all there is.
func (h *Handler) lookupDistribution(id string) (string, *record, error) {
	insp, ok := h.Store.(resource.Inspector)
	if !ok {
		return "", nil, errNotStored
	}
	rows, err := insp.Find(resource.Filter{Service: service, Type: "distribution", ID: id})
	if err != nil || len(rows) == 0 {
		return "", nil, errNotStored
	}
	var rec record
	if err := json.Unmarshal(rows[0].Attributes, &rec); err != nil {
		return "", nil, err
	}
	ns, _ := resource.SplitScope(rows[0].Namespace)
	return ns, &rec, nil
}

// edgeResponse is a response on its way to the viewer (and maybe the cache).
type edgeResponse struct {
	status int
	header http.Header
	body   []byte
}

func plainError(status int, msg string) *edgeResponse {
	hdr := http.Header{}
	hdr.Set("Content-Type", "text/plain; charset=utf-8")
	return &edgeResponse{status: status, header: hdr, body: []byte(msg + "\n")}
}

// edge is one viewer request against one distribution.
type edge struct {
	h    *Handler
	r    *http.Request
	ns   string
	rec  *record
	cfg  *node
	host string
}

func (h *Handler) ServeDataPlane(w http.ResponseWriter, r *http.Request) {
	id, ok := ParseHost(r.Host)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ns, rec, err := h.lookupDistribution(id)
	if err != nil {
		writeEdge(w, r, plainError(http.StatusNotFound, "The distribution does not exist."), "Error", id)
		return
	}
	e := &edge{h: h, r: r, ns: ns, rec: rec, cfg: rec.config(), host: r.Host}
	if !e.cfg.bool("Enabled") {
		writeEdge(w, r, plainError(http.StatusForbidden, "The distribution is disabled."), "Error", id)
		return
	}

	p := r.URL.Path
	if p == "/" || p == "" {
		if root := e.cfg.text("DefaultRootObject"); root != "" {
			p = "/" + root
		}
	}
	b := e.behaviour(p)

	if !methodIn(b.items("AllowedMethods"), r.Method) {
		writeEdge(w, r, plainError(http.StatusForbidden,
			"This distribution is not configured to allow the HTTP request method that was used for this request."), "Error", id)
		return
	}
	// TLS ends in front of opensnack, so only an explicit "http" from the
	// proxy says the viewer used plain HTTP.
	if r.Header.Get("X-Forwarded-Proto") == "http" {
		switch b.text("ViewerProtocolPolicy") {
		case "redirect-to-https":
			resp := plainError(http.StatusMovedPermanently, "Moved Permanently")
			resp.header.Set("Location", "https://"+r.Host+r.URL.RequestURI())
			writeEdge(w, r, resp, "Redirect", id)
			return
		case "https-only":
			writeEdge(w, r, plainError(http.StatusForbidden, "This distribution only accepts HTTPS."), "Error", id)
			return
		}
	}

	query := ""
	if e.forwardsQuery(b) {
		query = r.URL.RawQuery
	}
	cacheable := r.Method == http.MethodGet || r.Method == http.MethodHead
	key := p
	if query != "" {
		key += "?" + query
	}
	if cacheable {
		if hit := h.cache.get(rec.ID, key); hit != nil {
			resp := &edgeResponse{status: hit.status, header: hit.header.Clone(), body: hit.body}
			resp.header.Set("Age", strconv.Itoa(int(h.cache.now().Sub(hit.stored).Seconds())))
			writeEdge(w, r, resp, "Hit", id)
			return
		}
	}

	resp := e.fetch(b, p, query)
	if resp.status >= 400 {
		resp = e.customError(resp)
	}
	if cacheable && resp.status == http.StatusOK {
		if ttl := e.ttl(b, resp.header); ttl > 0 {
			now := h.cache.now()
			h.cache.put(rec.ID, key, &cached{status: resp.status, header: resp.header.Clone(), body: resp.body,
				stored: now, expires: now.Add(ttl)})
		}
	}
	writeEdge(w, r, resp, "Miss", id)
}

func writeEdge(w http.ResponseWriter, r *http.Request, resp *edgeResponse, result, id string) {
	for k, vs := range resp.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Cache", result+" from cloudfront")
	w.Header().Set("Via", "1.1 "+strings.ToLower(id)+".cloudfront.net (CloudFront)")
	w.Header().Set("X-Amz-Cf-Pop", "OSN1-C1")
	w.Header().Set("X-Amz-Cf-Id", awsresponses.NextRequestID())
	w.Header().Set("Content-Length", strconv.Itoa(len(resp.body)))
	w.WriteHeader(resp.status)
	if r.Method != http.MethodHead {
		w.Write(resp.body)
	}
}

func methodIn(items []*node, method string) bool {
	for _, m := range items {
		if strings.TrimSpace(m.Text) == method {
			return true
		}
	}
	return false
}

// behaviour returns the first ordered cache behaviour whose pattern matches,
// else the default one.
func (e *edge) behaviour(p string) *node {
	for _, b := range e.cfg.items("CacheBehaviors") {
		if globMatch(b.text("PathPattern"), p) {
			return b
		}
	}
	return e.cfg.child("DefaultCacheBehavior")
}

// globMatch is CloudFront's path pattern: * is any run of characters
// (slashes included), ? is one character, and case matters.
func globMatch(pattern, p string) bool {
	if !strings.HasPrefix(pattern, "/") {
		pattern = "/" + pattern
	}
	re := "^" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(pattern)) + "$"
	ok, _ := regexp.MatchString(re, p)
	return ok
}

func (e *edge) cachePolicy(b *node) *node {
	if id := b.text("CachePolicyId"); id != "" {
		if rec := e.h.findPolicy(e.ns, cachePolicyKind, id); rec != nil {
			return rec.config()
		}
	}
	return nil
}

func (e *edge) forwardsQuery(b *node) bool {
	if cp := e.cachePolicy(b); cp != nil {
		qb := cp.text("ParametersInCacheKeyAndForwardedToOrigin/QueryStringsConfig/QueryStringBehavior")
		return qb != "" && qb != "none"
	}
	return b.bool("ForwardedValues/QueryString")
}

var maxAge = regexp.MustCompile(`(?:^|[,\s])(s-maxage|max-age)=(\d+)`)

// ttl is how long to cache a response: the origin's Cache-Control clamped
// to the policy's (or legacy behaviour's) min and max, else the default.
func (e *edge) ttl(b *node, hdr http.Header) time.Duration {
	src := b
	if cp := e.cachePolicy(b); cp != nil {
		src = cp
	}
	min, _ := src.int64("MinTTL")
	def, ok := src.int64("DefaultTTL")
	if !ok {
		def = 86400
	}
	max, ok := src.int64("MaxTTL")
	if !ok {
		max = 31536000
	}
	ttl := def
	cc := strings.ToLower(hdr.Get("Cache-Control"))
	switch {
	case strings.Contains(cc, "no-store"), strings.Contains(cc, "no-cache"), strings.Contains(cc, "private"):
		ttl = min
	default:
		var sMax, plain int64 = -1, -1
		for _, m := range maxAge.FindAllStringSubmatch(cc, -1) {
			v, _ := strconv.ParseInt(m[2], 10, 64)
			if m[1] == "s-maxage" {
				sMax = v
			} else {
				plain = v
			}
		}
		if sMax < 0 {
			sMax = plain
		}
		if sMax >= 0 {
			ttl = sMax
			if ttl < min {
				ttl = min
			}
			if ttl > max {
				ttl = max
			}
		}
	}
	return time.Duration(ttl) * time.Second
}

func (e *edge) origin(id string) *node {
	for _, o := range e.cfg.items("Origins") {
		if o.text("Id") == id {
			return o
		}
	}
	// An origin group serves from its first member.
	for _, g := range e.cfg.items("OriginGroups") {
		if g.text("Id") == id {
			if members := g.items("Members"); len(members) > 0 {
				return e.origin(members[0].text("OriginId"))
			}
		}
	}
	return nil
}

func (e *edge) fetch(b *node, p, query string) *edgeResponse {
	o := e.origin(b.text("TargetOriginId"))
	if o == nil {
		return plainError(http.StatusBadGateway, "The origin does not exist.")
	}
	domain := strings.ToLower(o.text("DomainName"))
	full := strings.TrimSuffix(o.text("OriginPath"), "/") + p

	if bucket, website, ok := s3Origin(domain); ok {
		return e.fetchS3(bucket, website, full)
	}
	if e.h.Loopback != nil {
		if lh := e.h.Loopback(domain); lh != nil {
			return e.fetchLoopback(lh, o, domain, full, query)
		}
	}
	return e.fetchHTTP(o, domain, full, query)
}

// s3Origin recognises S3 REST and website endpoints.
func s3Origin(domain string) (bucket string, website, ok bool) {
	if h, ok := util.ParseAWSHost(domain); ok && h.Service == "s3" && h.Bucket != "" {
		return h.Bucket, false, true
	}
	for _, marker := range []string{".s3-website-", ".s3-website."} {
		if i := strings.Index(domain, marker); i > 0 {
			return domain[:i], true, true
		}
	}
	return "", false, false
}

func (e *edge) fetchS3(bucket string, website bool, p string) *edgeResponse {
	if e.h.Objects == nil {
		return plainError(http.StatusBadGateway, "S3 origins are not available.")
	}
	key := strings.TrimPrefix(p, "/")
	if website && (key == "" || strings.HasSuffix(key, "/")) {
		key += "index.html"
	}
	obj, err := e.h.Objects.GetObject(e.ns, bucket, key)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			zap.L().Warn("cloudfront: S3 origin read failed", zap.String("bucket", bucket), zap.Error(err))
			return plainError(http.StatusBadGateway, "The origin could not be read.")
		}
		if website {
			resp := plainError(http.StatusNotFound, "404 Not Found")
			resp.header.Set("Content-Type", "text/html")
			return resp
		}
		// Without s3:ListBucket (the usual OAC grant) S3 answers 403.
		hdr := http.Header{}
		hdr.Set("Content-Type", "application/xml")
		body := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
			`<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`
		return &edgeResponse{status: http.StatusForbidden, header: hdr, body: []byte(body)}
	}
	hdr := http.Header{}
	ct := obj.ContentType
	if ct == "" {
		ct = mime.TypeByExtension(path.Ext(key))
	}
	if ct == "" {
		ct = "binary/octet-stream"
	}
	hdr.Set("Content-Type", ct)
	if obj.ETag != "" {
		hdr.Set("ETag", obj.ETag)
	}
	if !obj.LastModified.IsZero() {
		hdr.Set("Last-Modified", obj.LastModified.UTC().Format(http.TimeFormat))
	}
	if obj.CacheControl != "" {
		hdr.Set("Cache-Control", obj.CacheControl)
	}
	return &edgeResponse{status: http.StatusOK, header: hdr, body: obj.Body}
}

func (e *edge) originRequest(ctx context.Context, o *node, method, target string) (*http.Request, error) {
	var body io.Reader
	if method != http.MethodGet && method != http.MethodHead {
		body = io.LimitReader(e.r.Body, maxOriginBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"Accept", "Accept-Language", "Content-Type", "Origin", "Authorization"} {
		if v := e.r.Header.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("User-Agent", "Amazon CloudFront")
	req.Header.Set("Via", "1.1 "+strings.ToLower(e.rec.ID)+".cloudfront.net (CloudFront)")
	for _, h := range o.items("CustomHeaders") {
		req.Header.Set(h.text("HeaderName"), h.text("HeaderValue"))
	}
	return req, nil
}

func (e *edge) fetchHTTP(o *node, domain, p, query string) *edgeResponse {
	c := o.child("CustomOriginConfig")
	scheme := "https"
	switch c.text("OriginProtocolPolicy") {
	case "http-only":
		scheme = "http"
	case "match-viewer":
		if e.r.Header.Get("X-Forwarded-Proto") == "http" || e.r.TLS == nil && e.r.Header.Get("X-Forwarded-Proto") == "" {
			scheme = "http"
		}
	}
	port := c.text("HTTPSPort")
	if scheme == "http" {
		port = c.text("HTTPPort")
	}
	host := domain
	if port != "" && !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
		host = net.JoinHostPort(domain, port)
	}
	target := scheme + "://" + host + p
	if query != "" {
		target += "?" + query
	}
	if !e.h.Origins.Allows(target) {
		zap.L().Info("cloudfront: origin not on OPENSNACK_CLOUDFRONT_ALLOWED_ORIGINS",
			zap.String("distribution", e.rec.ID), zap.String("target", target))
		return plainError(http.StatusBadGateway, "CloudFront wasn't able to connect to the origin.")
	}
	timeout := 30 * time.Second
	if s, ok := c.int64("OriginReadTimeout"); ok && s > 0 {
		timeout = time.Duration(s) * time.Second
	}
	ctx, cancel := context.WithTimeout(e.r.Context(), timeout)
	defer cancel()
	req, err := e.originRequest(ctx, o, e.r.Method, target)
	if err != nil {
		return plainError(http.StatusBadGateway, "CloudFront wasn't able to connect to the origin.")
	}
	resp, err := e.h.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return plainError(http.StatusGatewayTimeout, "The origin did not respond in time.")
		}
		return plainError(http.StatusBadGateway, "CloudFront wasn't able to connect to the origin.")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxOriginBody))
	hdr := resp.Header.Clone()
	for _, k := range egress.HopHeaders {
		hdr.Del(k)
	}
	hdr.Del("Content-Length")
	return &edgeResponse{status: resp.StatusCode, header: hdr, body: body}
}

// fetchLoopback serves an origin opensnack itself hosts (an execute-api
// host) without going through the network.
func (e *edge) fetchLoopback(lh http.Handler, o *node, domain, p, query string) *edgeResponse {
	target := "https://" + domain + p
	if query != "" {
		target += "?" + query
	}
	req, err := e.originRequest(e.r.Context(), o, e.r.Method, target)
	if err != nil {
		return plainError(http.StatusBadGateway, "CloudFront wasn't able to connect to the origin.")
	}
	req.Host = domain
	rec := &recorder{header: http.Header{}}
	lh.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	rec.header.Del("Content-Length")
	return &edgeResponse{status: rec.status, header: rec.header, body: rec.body.Bytes()}
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(s int) {
	if r.status == 0 {
		r.status = s
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}

// customError applies the distribution's custom error responses: an origin
// error code can be answered with another page and status (the usual
// single-page-app setup maps 403 and 404 to /index.html with 200).
func (e *edge) customError(resp *edgeResponse) *edgeResponse {
	for _, c := range e.cfg.items("CustomErrorResponses") {
		if c.text("ErrorCode") != strconv.Itoa(resp.status) {
			continue
		}
		status := resp.status
		if v, err := strconv.Atoi(c.text("ResponseCode")); err == nil && v > 0 {
			status = v
		}
		page := c.text("ResponsePagePath")
		if page == "" {
			resp.status = status
			return resp
		}
		pageResp := e.fetch(e.behaviour(page), page, "")
		if pageResp.status >= 400 {
			return resp
		}
		pageResp.status = status
		return pageResp
	}
	return resp
}
