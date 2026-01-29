// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package awsresponses

import (
	"net/http"
	"time"
)

func WriteAWSHeaders(w http.ResponseWriter) {
	h := w.Header()

	h.Set("Server", "AmazonEC2") // AWS uses various servers; this is fine for now
	// AWS expects GMT, not UTC. RFC1123 uses UTC, so we format manually with GMT
	dateStr := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	h.Set("Date", dateStr)
	h.Set("x-amz-request-id", NextRequestID())
	h.Set("x-amz-id-2", "opensnackfakeid") // AWS uses this for tracking; static is fine
}
