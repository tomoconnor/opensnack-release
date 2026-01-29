// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package awsresponses

import (
	"encoding/xml"
	"net/http"
)

// ErrorResponse is the AWS Query API error response format
type ErrorResponse struct {
	XMLName   xml.Name `xml:"ErrorResponse"`
	Error     Error    `xml:"Error"`
	RequestId string   `xml:"RequestId"`
}

// Error represents the error details in AWS Query API format
type Error struct {
	Type    string `xml:"Type"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// XMLErrorResponse is kept for backward compatibility but deprecated
// Use ErrorResponse for Query API services
type XMLErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource"`
	RequestId string   `xml:"RequestId"`
}

// S3ErrorResponse represents the S3 error response format
type S3ErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestId string   `xml:"RequestId"`
}

// WriteErrorXML writes an AWS Query API error response
// For Query API services (EC2, RDS, etc.), errors must be wrapped in ErrorResponse
func WriteErrorXML(w http.ResponseWriter, status int, code, message, resource string) error {
	// Get request ID before writing headers
	requestId := NextRequestID()
	WriteAWSHeaders(w)

	resp := ErrorResponse{
		Error: Error{
			Type:    "Sender",
			Code:    code,
			Message: message,
		},
		RequestId: requestId,
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)

	out, err := xml.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}

	_, err = w.Write(out)

	return err
}

func WriteS3ErrorXML(w http.ResponseWriter, status int, code, message, resource string) error {
	requestId := NextRequestID()

	// IMPORTANT: headers only, no writes
	WriteAWSHeaders(w)

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)

	resp := S3ErrorResponse{
		Code:      code,
		Message:   message,
		Resource:  resource,
		RequestId: requestId,
	}

	out, err := xml.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}

	_, err = w.Write(out)
	return err
}
