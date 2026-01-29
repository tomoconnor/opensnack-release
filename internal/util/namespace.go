// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package util

import (
	"net/http"
	"strings"
)

func NamespaceFromHeader(r *http.Request) string {
	// Extract from User-Agent (for TF_APPEND_USER_AGENT)
	userAgent := r.Header.Get("User-Agent")
	// log.Println("User-Agent:", userAgent)
	if userAgent != "" {
		// User-Agent format: "APN/1.0 HashiCorp/1.0 Terraform/1.5.0 (+https://www.terraform.io) custom-namespace"
		// We want the last token after the last space
		parts := strings.Fields(userAgent)
		if len(parts) > 0 {
			lastPart := parts[len(parts)-1]
			if strings.HasPrefix(lastPart, "custom-") {
				return strings.TrimPrefix(lastPart, "custom-")
			}
		}
	}

	return "default"
}
