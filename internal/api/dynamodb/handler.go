// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"

	"go.uber.org/zap"
)

const (
	APIVersion = "2012-08-10"

	service   = "dynamodb"
	typeTable = "table"
	// Items are stored one resource each, with the ID "<table>/<key>". Table
	// names can't contain "/", so the prefix is unambiguous.
	typeItem = "item"
)

type Handler struct {
	Store resource.Store
	// mu serialises every DynamoDB call, which makes conditional writes and
	// transactions atomic without help from the store.
	mu sync.Mutex
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// apiError is a DynamoDB JSON-protocol error.
type apiError struct {
	Code    string
	Message string
	Status  int
	Extra   map[string]any
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func newErr(code, msg string) *apiError {
	return &apiError{Code: code, Message: msg, Status: http.StatusBadRequest}
}

func validationErr(msg string) *apiError { return newErr("ValidationException", msg) }

func tableNotFound(name string) *apiError {
	return newErr("ResourceNotFoundException", "Requested resource not found: Table: "+name+" not found")
}

func conditionFailed() *apiError {
	return newErr("ConditionalCheckFailedException", "The conditional request failed")
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		zap.S().Errorw("dynamodb internal error", "error", err)
		ae = &apiError{Code: "InternalServerError", Message: err.Error(), Status: http.StatusInternalServerError}
	}
	prefix := "com.amazonaws.dynamodb.v20120810#"
	if ae.Code == "ValidationException" {
		prefix = "com.amazon.coral.validate#"
	}
	body := map[string]any{"__type": prefix + ae.Code, "message": ae.Message}
	for k, v := range ae.Extra {
		body[k] = v
	}
	awsresponses.WriteJSON(w, ae.Status, body)
}

type opFunc func(h *Handler, r *http.Request, body []byte) (any, error)

var operations = map[string]opFunc{
	"CreateTable":               (*Handler).createTable,
	"DescribeTable":             (*Handler).describeTable,
	"DeleteTable":               (*Handler).deleteTable,
	"ListTables":                (*Handler).listTables,
	"UpdateTable":               (*Handler).updateTable,
	"DescribeTimeToLive":        (*Handler).describeTimeToLive,
	"UpdateTimeToLive":          (*Handler).updateTimeToLive,
	"DescribeContinuousBackups": (*Handler).describeContinuousBackups,
	"UpdateContinuousBackups":   (*Handler).updateContinuousBackups,
	"ListTagsOfResource":        (*Handler).listTagsOfResource,
	"TagResource":               (*Handler).tagResource,
	"UntagResource":             (*Handler).untagResource,
	"DescribeLimits":            (*Handler).describeLimits,
	"DescribeEndpoints":         (*Handler).describeEndpoints,
	"PutItem":                   (*Handler).putItem,
	"GetItem":                   (*Handler).getItem,
	"DeleteItem":                (*Handler).deleteItem,
	"UpdateItem":                (*Handler).updateItem,
	"Query":                     (*Handler).query,
	"Scan":                      (*Handler).scan,
	"BatchWriteItem":            (*Handler).batchWriteItem,
	"BatchGetItem":              (*Handler).batchGetItem,
	"TransactWriteItems":        (*Handler).transactWriteItems,
	"TransactGetItems":          (*Handler).transactGetItems,
}

// Dispatch handles DynamoDB JSON API requests.
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	if target == "" {
		writeErr(w, newErr("MissingAuthenticationTokenException", "Missing X-Amz-Target header"))
		return
	}
	name := strings.TrimPrefix(target, "DynamoDB_20120810.")
	op, ok := operations[name]
	if !ok || name == target {
		writeErr(w, newErr("UnknownOperationException", "Unknown operation: "+target))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, newErr("SerializationException", err.Error()))
		return
	}

	h.mu.Lock()
	out, err := op(h, r, body)
	h.mu.Unlock()

	if err != nil {
		writeErr(w, err)
		return
	}
	awsresponses.WriteJSON(w, http.StatusOK, out)
}

// decode reads a request body, turning type errors into the
// SerializationException AWS returns and AttributeValue problems into
// ValidationExceptions.
func decode(body []byte, v any) error {
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			return ae
		}
		return newErr("SerializationException", err.Error())
	}
	return nil
}

// tableNameFromArn accepts a table name or ARN, as several APIs do.
func tableNameFromArn(arnOrName string) string {
	if strings.HasPrefix(arnOrName, "arn:") {
		if i := strings.Index(arnOrName, ":table/"); i >= 0 {
			name := arnOrName[i+len(":table/"):]
			if j := strings.IndexByte(name, '/'); j >= 0 {
				name = name[:j]
			}
			return name
		}
	}
	return arnOrName
}
