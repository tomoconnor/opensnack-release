// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Pods is the pod API of one namespace. Client implements it against a
// cluster; Fake in memory.
type Pods interface {
	Create(ctx context.Context, pod *Pod) (*Pod, error)
	Get(ctx context.Context, name string) (*Pod, error)
	// List returns the pods matching a label selector ("a=b,c=d").
	List(ctx context.Context, selector string) ([]Pod, error)
	// Delete removes a pod with a short grace period. A pod that is already
	// gone is not an error.
	Delete(ctx context.Context, name string) error
}

// APIError is a failed request, from the API server's Status object.
type APIError struct {
	Code    int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kubernetes: %d %s: %s", e.Code, e.Reason, e.Message)
}

// IsNotFound reports whether err is a 404 from the API server.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Code == http.StatusNotFound
}

// Client talks to the API server for one namespace.
type Client struct {
	cfg       *Config
	namespace string
	http      *http.Client

	mu        sync.Mutex
	token     string
	tokenRead time.Time
}

// NewClient returns a Pods client for namespace.
func NewClient(cfg *Config, namespace string) *Client {
	return &Client{
		cfg:       cfg,
		namespace: namespace,
		http:      &http.Client{Transport: &http.Transport{TLSClientConfig: cfg.TLS, MaxIdleConnsPerHost: 8}, Timeout: 30 * time.Second},
	}
}

// bearer returns the token, re-reading a token file every minute: bound
// service account tokens are rotated under a running pod.
func (c *Client) bearer() string {
	if c.cfg.TokenFile == "" {
		return c.cfg.Token
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" || time.Since(c.tokenRead) > time.Minute {
		if b, err := os.ReadFile(c.cfg.TokenFile); err == nil {
			c.token = strings.TrimSpace(string(b))
			c.tokenRead = time.Now()
		}
	}
	return c.token
}

func (c *Client) podsURL(name string, query url.Values) string {
	u := strings.TrimSuffix(c.cfg.Host, "/") + "/api/v1/namespaces/" + url.PathEscape(c.namespace) + "/pods"
	if name != "" {
		u += "/" + url.PathEscape(name)
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

func (c *Client) do(ctx context.Context, method, target string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t := c.bearer(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	if resp.StatusCode >= 300 {
		var st struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &st)
		if st.Message == "" {
			st.Message = strings.TrimSpace(string(raw))
		}
		return &APIError{Code: resp.StatusCode, Reason: st.Reason, Message: st.Message}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) Create(ctx context.Context, pod *Pod) (*Pod, error) {
	pod.APIVersion, pod.Kind = "v1", "Pod"
	var out Pod
	if err := c.do(ctx, http.MethodPost, c.podsURL("", nil), pod, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Get(ctx context.Context, name string) (*Pod, error) {
	var out Pod
	if err := c.do(ctx, http.MethodGet, c.podsURL(name, nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) List(ctx context.Context, selector string) ([]Pod, error) {
	var out podList
	if err := c.do(ctx, http.MethodGet, c.podsURL("", url.Values{"labelSelector": {selector}}), nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) Delete(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, c.podsURL(name, nil),
		map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "gracePeriodSeconds": 1, "propagationPolicy": "Background"}, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}
