// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package awsresponses

import (
	"encoding/xml"
	"net/http"
)

func WriteXML(w http.ResponseWriter, v interface{}) error {
	WriteAWSHeaders(w)

	w.Header().Set("Content-Type", "application/xml")

	// Write XML header
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n"))

	out, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return err
	}

	w.WriteHeader(http.StatusOK)
	_, err = w.Write(out)
	return err
}
