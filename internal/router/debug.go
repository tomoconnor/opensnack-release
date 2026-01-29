// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"net/http"
	"opensnack/internal/util"
	"strings"
	"time"

	"go.uber.org/zap"
)

type responseWriter struct {
	http.ResponseWriter
	status int
	size   int
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.status == 0 {
		rw.status = code
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
		rw.ResponseWriter.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.size += n
	return n, err
}

// DebugLoggerMiddleware prints a concise line per request so we can trace Terraform traffic.
func DebugLoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w}
		start := time.Now()

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		query := r.URL.RawQuery
		if query != "" {
			query = "?" + query
		}

		action, version := extractQueryAction(r)
		ns := util.NamespaceFromHeader(r)
		// ns := r.Header.Get("X-Opensnack-Namespace")
		if ns == "" {
			ns = "undefined"
		}

		zap.L().Info("request",
			zap.String("method", r.Method),
			zap.String("host", r.Host),
			zap.String("path", r.URL.Path),
			zap.String("query", query),
			zap.Int("status", rw.status),
			zap.Int("size", rw.size),
			zap.Duration("duration", duration),
			zap.String("action", action),
			zap.String("version", version),
			zap.String("namespace", ns),
		)

	})
}

func extractQueryAction(r *http.Request) (string, string) {
	ct := r.Header.Get("Content-Type")
	// Parse body only for AWS Query API style requests.
	if ct == "" || strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err == nil {
			return r.Form.Get("Action"), r.Form.Get("Version")
		}
	}

	values := r.URL.Query()
	return values.Get("Action"), values.Get("Version")
}
