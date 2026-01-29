// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package awsresponses

import (
	"encoding/json"
	"net/http"
	"time"
)

func WriteJSON(w http.ResponseWriter, status int, v interface{}) error {
	// WriteAWSHeaders(w)
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.Header().Set("Server", "AmazonEC2")
	w.Header().Set("X-Amz-Request-Id", NextRequestID())
	w.Header().Set("X-Amz-Id-2", "opensnackfakeid")
	w.Header().Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	w.Header().Set("Connection", "close")

	w.WriteHeader(status)

	// IMPORTANT: no Content-Length, no double writers
	return json.NewEncoder(w).Encode(v)

}

// func writeAWSJSON(w http.ResponseWriter, status int, v any) error {
// 	b, err := json.Marshal(v)
// 	if err != nil {
// 		return err
// 	}
// 	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
// 	w.Header().Set("Connection", "close")
// 	w.WriteHeader(status)

// 	_, err = w.Write(b)
// 	return err
// }
