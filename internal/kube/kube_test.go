// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientPods(t *testing.T) {
	var gotAuth, gotSelector, gotDelete string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/namespaces/sandboxes/pods":
			var p Pod
			json.NewDecoder(r.Body).Decode(&p)
			if p.Kind != "Pod" || p.APIVersion != "v1" {
				t.Errorf("create: kind %q apiVersion %q", p.Kind, p.APIVersion)
			}
			p.Status.Phase = PodPending
			json.NewEncoder(w).Encode(p)
		case r.Method == "GET" && r.URL.Path == "/api/v1/namespaces/sandboxes/pods":
			gotSelector = r.URL.Query().Get("labelSelector")
			w.Write([]byte(`{"items":[{"metadata":{"name":"a"},"status":{"phase":"Running","podIP":"10.0.0.9"}}]}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/namespaces/sandboxes/pods/missing":
			w.WriteHeader(404)
			w.Write([]byte(`{"kind":"Status","reason":"NotFound","message":"pods \"missing\" not found","code":404}`))
		case r.Method == "DELETE":
			b, _ := io.ReadAll(r.Body)
			gotDelete = r.URL.Path + " " + string(b)
			w.WriteHeader(404)
			w.Write([]byte(`{"kind":"Status","reason":"NotFound","code":404}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()

	tlsCfg := srv.Client().Transport.(*http.Transport).TLSClientConfig
	c := NewClient(&Config{Host: srv.URL, TLS: tlsCfg, Token: "secret"}, "sandboxes")
	ctx := context.Background()

	p, err := c.Create(ctx, &Pod{Metadata: ObjectMeta{Name: "a"}})
	if err != nil || p.Status.Phase != PodPending || gotAuth != "Bearer secret" {
		t.Fatalf("create: %v %+v auth %q", err, p, gotAuth)
	}
	pods, err := c.List(ctx, "app=x,b=c")
	if err != nil || len(pods) != 1 || pods[0].Status.PodIP != "10.0.0.9" || gotSelector != "app=x,b=c" {
		t.Fatalf("list: %v %+v selector %q", err, pods, gotSelector)
	}
	if _, err := c.Get(ctx, "missing"); !IsNotFound(err) {
		t.Fatalf("get missing: %v", err)
	}
	if err := c.Delete(ctx, "gone"); err != nil {
		t.Fatalf("delete of a missing pod should succeed: %v", err)
	}
	if !strings.Contains(gotDelete, "/pods/gone") || !strings.Contains(gotDelete, `"gracePeriodSeconds":1`) {
		t.Fatalf("delete request: %s", gotDelete)
	}
}

func TestKubeconfig(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ca.pem"), ca, 0o600)
	os.WriteFile(filepath.Join(dir, "token"), []byte("from-file\n"), 0o600)
	kc := `apiVersion: v1
kind: Config
current-context: dev
clusters:
- name: dev
  cluster:
    server: ` + srv.URL + `
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString(ca) + `
- name: other
  cluster:
    server: https://other.example
    certificate-authority: ca.pem
contexts:
- name: dev
  context: {cluster: dev, user: dev, namespace: sandboxes}
- name: other
  context: {cluster: other, user: filetoken}
- name: cloud
  context: {cluster: dev, user: cloud}
users:
- name: dev
  user: {token: inline}
- name: filetoken
  user: {tokenFile: token}
- name: cloud
  user:
    exec: {command: aws}
`
	path := filepath.Join(dir, "config")
	os.WriteFile(path, []byte(kc), 0o600)

	cfg, err := FromKubeconfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != srv.URL || cfg.Token != "inline" || cfg.Namespace != "sandboxes" || cfg.TLS.RootCAs == nil {
		t.Fatalf("current context: %+v", cfg)
	}
	// The CA from the kubeconfig verifies the server.
	c := NewClient(cfg, "x")
	if _, err := c.http.Get(srv.URL); err != nil {
		t.Fatalf("TLS with the kubeconfig CA: %v", err)
	}

	cfg, err = FromKubeconfig(path, "other")
	if err != nil || cfg.TokenFile != filepath.Join(dir, "token") || cfg.TLS.RootCAs == nil {
		t.Fatalf("relative paths: %+v %v", cfg, err)
	}
	if got := NewClient(cfg, "x").bearer(); got != "from-file" {
		t.Fatalf("token file: %q", got)
	}
	if _, err := FromKubeconfig(path, "cloud"); err == nil || !strings.Contains(err.Error(), "exec") {
		t.Fatalf("exec credentials should be refused: %v", err)
	}
	if _, err := FromKubeconfig(path, "nope"); err == nil {
		t.Fatal("unknown context should fail")
	}
}

// Load never falls back to ~/.kube/config.
func TestLoadNeedsExplicitKubeconfig(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", t.TempDir())
	if _, err := Load("", ""); err == nil {
		t.Fatal("Load with nothing configured should fail")
	}
}

func TestFakeSelectors(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	f.Create(ctx, &Pod{Metadata: ObjectMeta{Name: "a", Labels: map[string]string{"app": "x", "t": "1"}}})
	f.Create(ctx, &Pod{Metadata: ObjectMeta{Name: "b", Labels: map[string]string{"app": "x", "t": "2"}}})
	if l, _ := f.List(ctx, "app=x,t=2"); len(l) != 1 || l[0].Metadata.Name != "b" {
		t.Fatalf("selector: %+v", l)
	}
	if _, err := f.Create(ctx, &Pod{Metadata: ObjectMeta{Name: "a"}}); err == nil {
		t.Fatal("duplicate create should fail")
	}
	f.Delete(ctx, "a")
	if n := f.Names(); len(n) != 1 || len(f.Deleted) != 1 {
		t.Fatalf("after delete: %v %v", n, f.Deleted)
	}
}
