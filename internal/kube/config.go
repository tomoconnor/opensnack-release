// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kube

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is how to reach and authenticate to an API server.
type Config struct {
	Host string
	TLS  *tls.Config
	// Token is a bearer token; TokenFile one to read (and re-read) from disk.
	Token     string
	TokenFile string
	// Namespace is the kubeconfig context's namespace, if any.
	Namespace string
}

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InCluster is the configuration of a pod's own service account.
func InCluster() (*Config, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a cluster (KUBERNETES_SERVICE_HOST is unset)")
	}
	tokenFile := filepath.Join(serviceAccountDir, "token")
	if _, err := os.Stat(tokenFile); err != nil {
		return nil, fmt.Errorf("no service account token: %w", err)
	}
	ca, err := os.ReadFile(filepath.Join(serviceAccountDir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("no service account CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("service account CA is not PEM")
	}
	return &Config{
		Host:      "https://" + net.JoinHostPort(host, port),
		TLS:       &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TokenFile: tokenFile,
	}, nil
}

// kubeconfig is the part of a kubeconfig file this client understands:
// client certificates, bearer tokens and CA bundles. Exec and
// auth-provider plugins (cloud logins) are not supported.
type kubeconfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthority     string `yaml:"certificate-authority"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
			TLSServerName            string `yaml:"tls-server-name"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			ClientCertificate     string `yaml:"client-certificate"`
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKey             string `yaml:"client-key"`
			ClientKeyData         string `yaml:"client-key-data"`
			Token                 string `yaml:"token"`
			TokenFile             string `yaml:"tokenFile"`
			Exec                  any    `yaml:"exec"`
			AuthProvider          any    `yaml:"auth-provider"`
		} `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

// FromKubeconfig loads a context from a kubeconfig file; an empty context
// means its current-context.
func FromKubeconfig(path, context string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kc kubeconfig
	if err := yaml.Unmarshal(raw, &kc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if context == "" {
		context = kc.CurrentContext
	}
	dir := filepath.Dir(path)
	rel := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	// data returns inline base64 data, or the named file's contents.
	data := func(inline, file string) ([]byte, error) {
		if inline != "" {
			return base64.StdEncoding.DecodeString(inline)
		}
		if file != "" {
			return os.ReadFile(rel(file))
		}
		return nil, nil
	}

	var clusterName, userName, namespace string
	found := false
	for _, c := range kc.Contexts {
		if c.Name == context {
			clusterName, userName, namespace, found = c.Context.Cluster, c.Context.User, c.Context.Namespace, true
		}
	}
	if !found {
		return nil, fmt.Errorf("%s: no context %q", path, context)
	}
	cfg := &Config{Namespace: namespace}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	found = false
	for _, c := range kc.Clusters {
		if c.Name != clusterName {
			continue
		}
		found = true
		cfg.Host = c.Cluster.Server
		tlsCfg.ServerName = c.Cluster.TLSServerName
		tlsCfg.InsecureSkipVerify = c.Cluster.InsecureSkipTLSVerify
		ca, err := data(c.Cluster.CertificateAuthorityData, c.Cluster.CertificateAuthority)
		if err != nil {
			return nil, fmt.Errorf("cluster %s CA: %w", clusterName, err)
		}
		if len(ca) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ca) {
				return nil, fmt.Errorf("cluster %s CA is not PEM", clusterName)
			}
			tlsCfg.RootCAs = pool
		}
	}
	if !found {
		return nil, fmt.Errorf("%s: no cluster %q", path, clusterName)
	}
	for _, u := range kc.Users {
		if u.Name != userName {
			continue
		}
		if u.User.Exec != nil || u.User.AuthProvider != nil {
			return nil, fmt.Errorf("user %s: exec and auth-provider credentials are not supported; use a token or client certificate", userName)
		}
		cert, err := data(u.User.ClientCertificateData, u.User.ClientCertificate)
		if err != nil {
			return nil, fmt.Errorf("user %s certificate: %w", userName, err)
		}
		key, err := data(u.User.ClientKeyData, u.User.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("user %s key: %w", userName, err)
		}
		if len(cert) > 0 {
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				return nil, fmt.Errorf("user %s: %w", userName, err)
			}
			tlsCfg.Certificates = []tls.Certificate{pair}
		}
		cfg.Token = u.User.Token
		cfg.TokenFile = rel(u.User.TokenFile)
	}
	cfg.TLS = tlsCfg
	return cfg, nil
}

// Load is the in-cluster configuration when running in a pod, otherwise
// the kubeconfig at path (or $KUBECONFIG). It never falls back to
// ~/.kube/config: pointing a server at whatever cluster a developer's
// shell happens to have selected is too easy a mistake.
func Load(path, context string) (*Config, error) {
	if path == "" {
		if cfg, err := InCluster(); err == nil {
			return cfg, nil
		}
		path = os.Getenv("KUBECONFIG")
	}
	if path == "" {
		return nil, errors.New("not running in a cluster, and no kubeconfig given (set OPENSNACK_LAMBDA_K8S_KUBECONFIG or KUBECONFIG)")
	}
	return FromKubeconfig(path, context)
}
