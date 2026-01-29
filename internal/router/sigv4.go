// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const identityKey contextKey = "identity"

func SigV4Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")

		if strings.Contains(auth, "AWS4-HMAC-SHA256") {
			ctx := context.WithValue(r.Context(), identityKey, "FAKE_ACCESS_KEY")
			r = r.WithContext(ctx)
		}

		// Always allow — this is a stub.
		next.ServeHTTP(w, r)
	})
}
