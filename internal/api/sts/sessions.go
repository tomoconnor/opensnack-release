// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"opensnack/internal/auth"
)

// Session is one set of temporary credentials.
type Session struct {
	Credential   auth.Credential
	SessionToken string
	Expires      time.Time
	// Arn and UserID are what GetCallerIdentity reports for the session.
	Arn, UserID string
}

// Sessions holds the temporary credentials STS has issued. It is an
// auth.Source, so the keys verify like any other. It lives in memory: a
// restart forgets every issued key.
type Sessions struct {
	mu  sync.Mutex
	m   map[string]*Session
	Now func() time.Time
}

func NewSessions() *Sessions {
	return &Sessions{m: map[string]*Session{}, Now: time.Now}
}

func (s *Sessions) add(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	for k, v := range s.m {
		if now.After(v.Expires) {
			delete(s.m, k)
		}
	}
	s.m[sess.Credential.AccessKeyID] = sess
}

// Revoke forgets a session before it expires.
func (s *Sessions) Revoke(accessKeyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, accessKeyID)
}

// Get returns the live session for an access key, if there is one.
func (s *Sessions) Get(accessKeyID string) *Session {
	if s == nil || accessKeyID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[accessKeyID]
	if !ok || s.Now().After(sess.Expires) {
		return nil
	}
	return sess
}

func (s *Sessions) Lookup(_ context.Context, accessKeyID string) (*auth.Credential, error) {
	sess := s.Get(accessKeyID)
	if sess == nil {
		return nil, auth.ErrUnknownKey
	}
	c := sess.Credential
	return &c, nil
}

const keyAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

func newAccessKeyID() string {
	b := make([]byte, 16)
	rand.Read(b)
	for i := range b {
		b[i] = keyAlphabet[int(b[i])%len(keyAlphabet)]
	}
	return "ASIA" + string(b)
}

func randomB64(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}
