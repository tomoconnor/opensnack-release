// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// Credential is one access key and what it maps to.
type Credential struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	// AccountID defaults to OPENSNACK_ACCOUNT_ID when empty.
	AccountID string `json:"account_id,omitempty"`
	// Namespace defaults to the access key ID when empty. Several keys can
	// share one namespace (a user's long-lived key and the temporary keys
	// AssumeRole hands out, for example).
	Namespace string `json:"namespace,omitempty"`
}

// ErrUnknownKey means the source has no such access key.
var ErrUnknownKey = errors.New("unknown access key")

// Source looks up the secret for an access key.
type Source interface {
	Lookup(ctx context.Context, accessKeyID string) (*Credential, error)
}

// TableSource is a fixed set of keys, loaded from a JSON file:
//
//	[{"access_key_id": "...", "secret_access_key": "...", "account_id": "...", "namespace": "..."}]
type TableSource struct {
	keys map[string]Credential
}

func NewTableSource(creds []Credential) *TableSource {
	t := &TableSource{keys: make(map[string]Credential, len(creds))}
	for _, c := range creds {
		t.keys[c.AccessKeyID] = c
	}
	return t
}

func LoadTableSource(path string) (*TableSource, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds []Credential
	if err := json.Unmarshal(b, &creds); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return NewTableSource(creds), nil
}

func (t *TableSource) Lookup(_ context.Context, accessKeyID string) (*Credential, error) {
	c, ok := t.keys[accessKeyID]
	if !ok {
		return nil, ErrUnknownKey
	}
	return &c, nil
}

// HTTPSource asks an external credential service for each key:
//
//	GET {URL}?access_key_id=AKID   (Authorization: Bearer {Token} when set)
//	200 → a Credential as JSON; 404 → unknown key; anything else → error.
//
// Answers are cached so a busy apply doesn't call out on every request.
type HTTPSource struct {
	URL         string
	Token       string
	Client      *http.Client
	PositiveTTL time.Duration
	NegativeTTL time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	cred    *Credential
	expires time.Time
}

func NewHTTPSource(rawURL, token string) *HTTPSource {
	return &HTTPSource{
		URL:         rawURL,
		Token:       token,
		Client:      &http.Client{Timeout: 5 * time.Second},
		PositiveTTL: 5 * time.Minute,
		NegativeTTL: 30 * time.Second,
		cache:       map[string]cacheEntry{},
	}
}

func (s *HTTPSource) Lookup(ctx context.Context, accessKeyID string) (*Credential, error) {
	now := time.Now()
	s.mu.Lock()
	if e, ok := s.cache[accessKeyID]; ok && now.Before(e.expires) {
		s.mu.Unlock()
		if e.cred == nil {
			return nil, ErrUnknownKey
		}
		return e.cred, nil
	}
	s.mu.Unlock()

	u, err := url.Parse(s.URL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("access_key_id", accessKeyID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("credential lookup: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var c Credential
		if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
			return nil, fmt.Errorf("credential lookup: %w", err)
		}
		if c.AccessKeyID == "" {
			c.AccessKeyID = accessKeyID
		}
		s.store(accessKeyID, &c, s.PositiveTTL)
		return &c, nil
	case http.StatusNotFound:
		s.store(accessKeyID, nil, s.NegativeTTL)
		return nil, ErrUnknownKey
	default:
		return nil, fmt.Errorf("credential lookup: status %d", resp.StatusCode)
	}
}

func (s *HTTPSource) store(key string, c *Credential, ttl time.Duration) {
	s.mu.Lock()
	s.cache[key] = cacheEntry{cred: c, expires: time.Now().Add(ttl)}
	s.mu.Unlock()
}

// ChainSource tries each source in turn, so temporary keys minted by STS can
// sit in front of the table or the HTTP callback.
type ChainSource []Source

func (c ChainSource) Lookup(ctx context.Context, accessKeyID string) (*Credential, error) {
	for _, s := range c {
		cred, err := s.Lookup(ctx, accessKeyID)
		if err == nil {
			return cred, nil
		}
		if !errors.Is(err, ErrUnknownKey) {
			return nil, err
		}
	}
	return nil, ErrUnknownKey
}
