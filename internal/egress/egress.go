// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package egress decides which outbound HTTP targets the data planes (API
// Gateway HTTP_PROXY integrations, CloudFront custom origins) may reach.
// Tenants choose those URLs, so the default is to reach nothing: the
// operator lists in-cluster targets explicitly.
package egress

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrNotAllowed means the target is not on the allowlist.
var ErrNotAllowed = errors.New("egress target not allowed")

// Allowlist is a set of host patterns:
//
//	example.internal        that host, any port
//	example.internal:8080   that host and port
//	*.svc.cluster.local     any subdomain
//	*                       anything (only for local development)
//
// Entries may also be written as URLs ("http://api.internal:8080/"); only
// the host and port count.
type Allowlist struct {
	entries []string
}

// FromEnv reads a comma-separated allowlist from the named variable.
func FromEnv(name string) *Allowlist {
	return New(strings.Split(os.Getenv(name), ","))
}

func New(entries []string) *Allowlist {
	a := &Allowlist{}
	for _, e := range entries {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if u, err := url.Parse(e); err == nil && u.Host != "" {
			e = u.Host
		}
		a.entries = append(a.entries, strings.TrimSuffix(e, "/"))
	}
	return a
}

// Empty reports whether nothing at all is allowed.
func (a *Allowlist) Empty() bool { return a == nil || len(a.entries) == 0 }

// Allows reports whether rawURL's host may be contacted.
func (a *Allowlist) Allows(rawURL string) bool {
	if a == nil {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	for _, e := range a.entries {
		if e == "*" {
			return true
		}
		eh, ep := e, ""
		if h, p, err := net.SplitHostPort(e); err == nil {
			eh, ep = h, p
		}
		if ep != "" && ep != port {
			continue
		}
		if suffix, ok := strings.CutPrefix(eh, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == eh {
			return true
		}
	}
	return false
}

// Client returns an HTTP client that refuses redirects to targets off the
// allowlist, so an allowed target can't bounce us to the internet.
func (a *Allowlist) Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !a.Allows(req.URL.String()) {
				return ErrNotAllowed
			}
			return nil
		},
	}
}

// HopHeaders are the hop-by-hop headers a proxy must not forward.
var HopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}
