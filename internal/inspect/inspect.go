// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package inspect is opensnack's internal API, for external checkers (anything
// that verifies what a tenant built) and test harnesses. It reads and deletes resources across
// namespaces, so it sits outside AWS authentication behind its own token.
//
//	GET    /_opensnack/v1/namespaces                       namespaces and resource counts
//	GET    /_opensnack/v1/namespaces/{ns}                  one namespace's count, by service
//	DELETE /_opensnack/v1/namespaces/{ns}                  delete everything in it
//	GET    /_opensnack/v1/namespaces/{ns}/resources        ?service=&type=&region=&id=
//	GET    /_opensnack/v1/namespaces/{ns}/resources/{service}/{type}/{id...}   ?region=
//	GET    /_opensnack/v1/namespaces/{ns}/s3/{bucket}/{key...}               raw object bytes
//
// Services can add their own read-only views under the same prefix and
// token through Routes (EC2's reachability evaluator, for example).
package inspect

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"opensnack/internal/resource"
)

const Prefix = "/_opensnack/v1/"

// Item is one resource as the inspection API reports it.
type Item struct {
	Namespace string `json:"namespace"`
	// Region is "" for global services.
	Region    string          `json:"region"`
	Service   string          `json:"service"`
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Attrs     json.RawMessage `json:"attributes"`
}

// ObjectReader opens an S3 object's bytes.
type ObjectReader func(namespace, bucket, key string) ([]byte, error)

// NamespaceCleaner removes whatever a namespace keeps outside the database
// (S3 object files, Lambda code).
type NamespaceCleaner func(namespace string) error

type API struct {
	Store resource.Inspector
	// Token is required as "Authorization: Bearer <token>". Empty disables
	// the check, which is only allowed in dev mode (see FromEnv).
	Token    string
	Objects  ObjectReader
	Cleaners []NamespaceCleaner
	// Routes adds handlers by pattern relative to Prefix, e.g.
	// "GET namespaces/{ns}/ec2/reachability".
	Routes map[string]http.HandlerFunc
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"namespaces", a.namespaces)
	mux.HandleFunc("GET "+Prefix+"namespaces/{ns}", a.count)
	mux.HandleFunc("DELETE "+Prefix+"namespaces/{ns}", a.deleteNamespace)
	mux.HandleFunc("GET "+Prefix+"namespaces/{ns}/resources", a.list)
	mux.HandleFunc("GET "+Prefix+"namespaces/{ns}/resources/{service}/{type}/{id...}", a.get)
	mux.HandleFunc("GET "+Prefix+"namespaces/{ns}/s3/{bucket}/{key...}", a.object)
	for pattern, h := range a.Routes {
		method, path, _ := strings.Cut(pattern, " ")
		mux.HandleFunc(method+" "+Prefix+path, h)
	}
	return a.authorize(mux)
}

func (a *API) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Token != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + a.Token
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				writeErr(w, http.StatusUnauthorized, "missing or wrong bearer token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) namespaces(w http.ResponseWriter, r *http.Request) {
	counts, err := a.Store.Namespaces()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": counts})
}

func (a *API) count(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	by, err := a.Store.Count(ns)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	total := 0
	for _, n := range by {
		total += n
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespace": ns, "total": total, "by_service": by})
}

func (a *API) deleteNamespace(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	n, err := a.Store.DeleteNamespace(ns)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, clean := range a.Cleaners {
		if err := clean(ns); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespace": ns, "deleted": n})
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := resource.Filter{
		Namespace: r.PathValue("ns"),
		Service:   q.Get("service"),
		Type:      q.Get("type"),
		ID:        q.Get("id"),
	}
	if q.Has("region") {
		f.Region, f.RegionSet = q.Get("region"), true
	}
	rows, err := a.Store.Find(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, toItem(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": items})
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	f := resource.Filter{
		Namespace: r.PathValue("ns"),
		Service:   r.PathValue("service"),
		Type:      r.PathValue("type"),
		ID:        r.PathValue("id"),
	}
	if q := r.URL.Query(); q.Has("region") {
		f.Region, f.RegionSet = q.Get("region"), true
	}
	rows, err := a.Store.Find(f)
	switch {
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	case len(rows) == 0:
		writeErr(w, http.StatusNotFound, "no such resource")
	case len(rows) > 1:
		writeErr(w, http.StatusConflict, "that id exists in several regions; pass ?region=")
	default:
		writeJSON(w, http.StatusOK, toItem(rows[0]))
	}
}

func (a *API) object(w http.ResponseWriter, r *http.Request) {
	if a.Objects == nil {
		writeErr(w, http.StatusNotImplemented, "object reads not configured")
		return
	}
	b, err := a.Objects(r.PathValue("ns"), r.PathValue("bucket"), r.PathValue("key"))
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, "no such object")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(b)
}

func toItem(r resource.Resource) Item {
	attrs := json.RawMessage(r.Attributes)
	if !json.Valid(attrs) {
		attrs = json.RawMessage("null")
	}
	return Item{
		Namespace: r.Namespace,
		Region:    r.Region,
		Service:   r.Service,
		Type:      r.Type,
		ID:        r.ID,
		CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(),
		Attrs:     attrs,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
