// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package awsresponses

import "net/http"

func WriteEmpty200(w http.ResponseWriter, extraHeaders map[string]string) error {
	WriteAWSHeaders(w)

	for k, v := range extraHeaders {
		w.Header().Set(k, v)
	}

	w.WriteHeader(200)
	return nil
}

func WriteEmpty204(w http.ResponseWriter) error {
	WriteAWSHeaders(w)
	w.WriteHeader(204)
	return nil
}
