// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"net/http"

	"opensnack/internal/awsresponses"
	"opensnack/internal/util"
)

// writeJSONError writes an SQS JSON-protocol error. The x-amzn-query-error
// header carries the Query error code for query-compatible clients.
func writeJSONError(w http.ResponseWriter, e *apiError) {
	fault := "Sender"
	if !e.Sender {
		fault = "Receiver"
	}
	w.Header().Set("x-amzn-query-error", e.QueryCode+";"+fault)
	awsresponses.WriteJSON(w, e.Status, map[string]string{
		"__type":  "com.amazonaws.sqs#" + e.Code,
		"message": e.Message,
	})
}

func serveJSON[I any, O any](w http.ResponseWriter, r *http.Request, op func(*http.Request, *I) (*O, *apiError)) {
	in := new(I)
	if err := util.DecodeAWSJSON(r, in); err != nil {
		writeJSONError(w, newErr("SerializationException", "MalformedInput", "Invalid request body: %s", err.Error()))
		return
	}
	out, aerr := op(r, in)
	if aerr != nil {
		writeJSONError(w, aerr)
		return
	}
	awsresponses.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) dispatchJSON(w http.ResponseWriter, r *http.Request, op string) {
	switch op {
	case "CreateQueue":
		serveJSON(w, r, h.createQueue)
	case "GetQueueUrl":
		serveJSON(w, r, h.getQueueURL)
	case "ListQueues":
		serveJSON(w, r, h.listQueues)
	case "DeleteQueue":
		serveJSON(w, r, h.deleteQueue)
	case "GetQueueAttributes":
		serveJSON(w, r, h.getQueueAttributes)
	case "SetQueueAttributes":
		serveJSON(w, r, h.setQueueAttributes)
	case "TagQueue":
		serveJSON(w, r, h.tagQueue)
	case "UntagQueue":
		serveJSON(w, r, h.untagQueue)
	case "ListQueueTags":
		serveJSON(w, r, h.listQueueTags)
	case "ListDeadLetterSourceQueues":
		serveJSON(w, r, h.listDeadLetterSourceQueues)
	case "SendMessage":
		serveJSON(w, r, h.sendMessage)
	case "SendMessageBatch":
		serveJSON(w, r, h.sendMessageBatch)
	case "ReceiveMessage":
		serveJSON(w, r, h.receiveMessage)
	case "DeleteMessage":
		serveJSON(w, r, h.deleteMessage)
	case "DeleteMessageBatch":
		serveJSON(w, r, h.deleteMessageBatch)
	case "ChangeMessageVisibility":
		serveJSON(w, r, h.changeMessageVisibility)
	case "ChangeMessageVisibilityBatch":
		serveJSON(w, r, h.changeMessageVisibilityBatch)
	case "PurgeQueue":
		serveJSON(w, r, h.purgeQueue)
	default:
		writeJSONError(w, newErr("UnsupportedOperation", "AWS.SimpleQueueService.UnsupportedOperation", "Operation %s is not supported by opensnack.", op))
	}
}
