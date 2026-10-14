// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cloudfront is the CloudFront control plane (REST-XML under
// /2020-05-31/) and the data plane that serves {id}.cloudfront.net.
//
// Configs (distribution, origin access control, cache and origin request
// policy) are stored as the XML the client sent, with the defaults AWS adds
// filled in, and returned verbatim, so the provider's large schemas
// round-trip without every field being modelled here. CloudFront is a
// global service: everything lives in the tenant's global namespace.
package cloudfront

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/egress"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	service    = "cloudfront"
	apiVersion = "/2020-05-31/"
)

// DeployDelay is how long a distribution stays InProgress after a change.
// The provider polls every 15-30s, so anything shorter finishes its waiters
// on the first poll.
var DeployDelay = 2 * time.Second

type Handler struct {
	Store resource.Store
	// Objects reads S3 origins. Nil makes S3 origins answer 502.
	Objects ObjectReader
	// Origins is where custom (HTTP) origins may be fetched from
	// (OPENSNACK_CLOUDFRONT_ALLOWED_ORIGINS, empty by default).
	Origins *egress.Allowlist
	// Loopback returns a handler for origin hosts opensnack serves itself
	// (execute-api), or nil.
	Loopback func(host string) http.Handler

	cache  *cache
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
}

func NewHandler(store resource.Store) *Handler {
	origins := egress.FromEnv("OPENSNACK_CLOUDFRONT_ALLOWED_ORIGINS")
	return &Handler{
		Store:   store,
		Origins: origins,
		cache:   newCache(),
		client:  origins.Client(30 * time.Second),
		now:     time.Now,
	}
}

//
// ERRORS
//

type cfErr struct {
	status int
	code   string
	msg    string
}

func (e *cfErr) Error() string { return e.code + ": " + e.msg }

func errf(status int, code, format string, a ...any) *cfErr {
	return &cfErr{status, code, fmt.Sprintf(format, a...)}
}

func malformed(err error) *cfErr {
	return errf(http.StatusBadRequest, "MalformedXML", "The XML document you provided was not well-formed or did not validate: %v", err)
}

func invalidArg(format string, a ...any) *cfErr {
	return errf(http.StatusBadRequest, "InvalidArgument", format, a...)
}

func writeErr(w http.ResponseWriter, err error) {
	var e *cfErr
	if !errors.As(err, &e) {
		e = &cfErr{http.StatusInternalServerError, "InternalError", err.Error()}
	}
	id := awsresponses.NextRequestID()
	doc := elem("ErrorResponse",
		elem("Error", leaf("Type", "Sender"), leaf("Code", e.code), leaf("Message", e.msg)),
		leaf("RequestId", id))
	w.Header().Set("Content-Type", "text/xml")
	w.Header().Set("X-Amzn-Requestid", id)
	w.WriteHeader(e.status)
	w.Write(doc.render())
}

func writeDoc(w http.ResponseWriter, status int, doc *node, etag string) {
	w.Header().Set("Content-Type", "text/xml")
	w.Header().Set("X-Amzn-Requestid", awsresponses.NextRequestID())
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.WriteHeader(status)
	w.Write(doc.render())
}

//
// STORE
//

// record is what the store keeps for every CloudFront resource.
type record struct {
	ID       string `json:"id"`
	ETag     string `json:"etag"`
	Config   string `json:"config"`
	Modified string `json:"last_modified"`
	// Distributions
	ARN          string            `json:"arn,omitempty"`
	DomainName   string            `json:"domain_name,omitempty"`
	DeployedAt   string            `json:"deployed_at,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	AccountID    string            `json:"account_id,omitempty"`
	Distribution string            `json:"distribution,omitempty"`
}

func (rec *record) config() *node {
	n, err := parseXML([]byte(rec.Config))
	if err != nil {
		return elem("Config")
	}
	return n
}

var errNotStored = errors.New("not stored")

func (h *Handler) load(ns, typ, id string) (*record, error) {
	res, err := h.Store.Get(id, service, typ, ns)
	if err != nil || res == nil || res.ID == "" {
		return nil, errNotStored
	}
	var rec record
	if err := json.Unmarshal(res.Attributes, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (h *Handler) save(ns, typ string, rec *record, create bool) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	res := &resource.Resource{ID: rec.ID, Namespace: ns, Service: service, Type: typ, Attributes: b}
	if create {
		return h.Store.Create(res)
	}
	return h.Store.Update(res)
}

func (h *Handler) all(ns, typ string) []*record {
	rows, _ := h.Store.List(service, typ, ns)
	var out []*record
	for _, row := range rows {
		var rec record
		if json.Unmarshal(row.Attributes, &rec) == nil {
			out = append(out, &rec)
		}
	}
	return out
}

func randomID(prefix string, n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return prefix + string(b)
}

func uuid() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func newETag() string { return randomID("E", 13) }

func (h *Handler) timestamp() string {
	return h.now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func readDoc(r *http.Request) (*node, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, malformed(err)
	}
	n, err := parseXML(b)
	if err != nil {
		return nil, malformed(err)
	}
	return n, nil
}

// checkIfMatch enforces the optimistic locking every CloudFront update and
// delete uses.
func checkIfMatch(r *http.Request, etag string) error {
	m := r.Header.Get("If-Match")
	if m == "" {
		return errf(http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match version is missing or not valid for the resource.")
	}
	if m != etag {
		return errf(http.StatusPreconditionFailed, "PreconditionFailed", "The precondition given in one or more of the request-header fields evaluated to false.")
	}
	return nil
}

//
// ROUTING
//

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := strings.CutPrefix(r.URL.EscapedPath(), apiVersion)
	if !ok {
		writeErr(w, errf(http.StatusNotFound, "NotFound", "Unknown path %s", r.URL.Path))
		return
	}
	var segs []string
	for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}
		segs = append(segs, s)
	}
	ns := util.GlobalNamespace(r)
	if r.Method != http.MethodGet {
		h.mu.Lock()
		defer h.mu.Unlock()
	}

	var err error
	switch segs[0] {
	case "distribution":
		err = h.distributions(w, r, ns, segs[1:])
	case "origin-access-control":
		err = h.policies(w, r, ns, oacKind, segs[1:])
	case "cache-policy":
		err = h.policies(w, r, ns, cachePolicyKind, segs[1:])
	case "origin-request-policy":
		err = h.policies(w, r, ns, originRequestPolicyKind, segs[1:])
	case "tagging":
		err = h.tagging(w, r, ns)
	default:
		err = errf(http.StatusBadRequest, "InvalidArgument", "opensnack does not implement CloudFront %s", segs[0])
	}
	if err != nil {
		writeErr(w, err)
	}
}

//
// TAGS
//

func (h *Handler) tagging(w http.ResponseWriter, r *http.Request, ns string) error {
	arn := r.URL.Query().Get("Resource")
	_, rest, _ := strings.Cut(arn, ":distribution/")
	rec, err := h.load(ns, "distribution", rest)
	if rest == "" || err != nil || rec.ARN != arn {
		return errf(http.StatusNotFound, "NoSuchResource", "The specified resource %s does not exist.", arn)
	}
	if rec.Tags == nil {
		rec.Tags = map[string]string{}
	}
	switch {
	case r.Method == http.MethodGet:
		writeDoc(w, http.StatusOK, tagsDoc(rec.Tags), "")
		return nil
	case r.Method == http.MethodPost && r.URL.Query().Get("Operation") == "Tag":
		doc, err := readDoc(r)
		if err != nil {
			return err
		}
		for k, v := range parseTags(doc) {
			rec.Tags[k] = v
		}
	case r.Method == http.MethodPost && r.URL.Query().Get("Operation") == "Untag":
		doc, err := readDoc(r)
		if err != nil {
			return err
		}
		if items := doc.child("Items"); items != nil {
			for _, k := range items.Children {
				delete(rec.Tags, strings.TrimSpace(k.Text))
			}
		}
	default:
		return invalidArg("Unsupported tagging operation")
	}
	if err := h.save(ns, "distribution", rec, false); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// parseTags reads <Tags><Items><Tag><Key/><Value/></Tag>...</Items></Tags>.
func parseTags(doc *node) map[string]string {
	out := map[string]string{}
	items := doc.child("Items")
	if items == nil {
		return out
	}
	for _, t := range items.Children {
		out[t.text("Key")] = t.text("Value")
	}
	return out
}

func tagsDoc(tags map[string]string) *node {
	items := elem("Items")
	for _, k := range sortedKeys(tags) {
		items.Children = append(items.Children, elem("Tag", leaf("Key", k), leaf("Value", tags[k])))
	}
	return elem("Tags", items)
}
