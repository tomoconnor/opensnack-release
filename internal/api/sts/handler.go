// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sts

import (
	"net/http"

	"opensnack/internal/awsresponses"
)

const (
	APIVersion = "2011-06-15"
	stsAccount = "000000000000"
	stsArn     = "arn:aws:iam::000000000000:user/opensnack"
	stsUserId  = "opensnack"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidParameterValue", "Failed to parse form", "")
		return
	}
	action := r.FormValue("Action")

	switch action {
	case "GetCallerIdentity":
		h.GetCallerIdentity(w, r)
	default:
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidAction",
			"Unknown STS action",
			action,
		)
	}
}

func (h *Handler) GetCallerIdentity(w http.ResponseWriter, r *http.Request) {
	resp := GetCallerIdentityResponse{
		GetCallerIdentityResult: GetCallerIdentityResult{
			Arn:     stsArn,
			UserId:  stsUserId,
			Account: stsAccount,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}
