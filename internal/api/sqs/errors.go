// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"fmt"
	"net/http"
)

// apiError is an SQS error in both protocols' terms: Code is the JSON
// protocol's shape name, QueryCode the legacy Query error code, which the
// JSON protocol also carries in the x-amzn-query-error header so SDKs in
// query-compatible mode (and the AWS provider's error checks) see it.
type apiError struct {
	Code      string
	QueryCode string
	Message   string
	Status    int
	// Sender is false for server faults.
	Sender bool
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func newErr(code, queryCode, format string, args ...any) *apiError {
	if queryCode == "" {
		queryCode = code
	}
	return &apiError{Code: code, QueryCode: queryCode, Message: fmt.Sprintf(format, args...), Status: http.StatusBadRequest, Sender: true}
}

func errNoQueue() *apiError {
	return newErr("QueueDoesNotExist", "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.")
}

func errMissing(param string) *apiError {
	return newErr("MissingParameter", "", "The request must contain the parameter %s.", param)
}

func errInvalidParam(format string, args ...any) *apiError {
	return newErr("InvalidParameterValue", "", format, args...)
}

func errInvalidAttrName(name string) *apiError {
	return newErr("InvalidAttributeName", "", "Unknown Attribute %s.", name)
}

func errInvalidAttrValue(name, reason string) *apiError {
	return newErr("InvalidAttributeValue", "", "Invalid value for the parameter %s. Reason: %s", name, reason)
}

func errReceiptHandle(h string) *apiError {
	return newErr("ReceiptHandleIsInvalid", "", "The input receipt handle \"%s\" is not a valid receipt handle.", h)
}

func errInternal(err error) *apiError {
	return &apiError{Code: "InternalError", QueryCode: "InternalError", Message: err.Error(), Status: http.StatusInternalServerError}
}
