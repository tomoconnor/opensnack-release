// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"opensnack/internal/util"
)

var aliasNameRe = regexp.MustCompile(`^alias/[a-zA-Z0-9/_-]{1,250}$`)

func validateAliasName(name string) *Error {
	if !aliasNameRe.MatchString(name) {
		return errValidation("1 validation error detected: Value '" + name +
			"' at 'aliasName' failed to satisfy constraint: Member must satisfy regular expression pattern: ^[a-zA-Z0-9:/_-]+$")
	}
	if strings.HasPrefix(name, "alias/aws/") {
		return &Error{http.StatusBadRequest, "NotAuthorizedException",
			"Cannot create, update or delete alias with reserved prefix alias/aws/"}
	}
	return nil
}

// aliasTarget loads the key an alias is to point at. It must be named by key
// ID or key ARN, belong to the customer and not be pending deletion.
func (h *Handler) aliasTarget(r *http.Request, ref string) (*keyRecord, *Error) {
	if _, alias := parseKeyRef(ref); alias != "" {
		return nil, errValidation("Aliases must refer to keys. Not aliases")
	}
	_, rec, kerr := h.resolveKey(r, ref, false)
	if kerr != nil {
		return nil, kerr
	}
	if rec.Metadata.KeyState == "PendingDeletion" {
		return nil, errInvalidState(rec.Metadata.ARN + " is pending deletion.")
	}
	if rec.Metadata.KeyManager == "AWS" {
		return nil, &Error{http.StatusBadRequest, "NotAuthorizedException", "Cannot point an alias at an AWS managed key"}
	}
	return rec, nil
}

// CreateAlias creates an alias for a customer managed key
func (h *Handler) CreateAlias(w http.ResponseWriter, r *http.Request) {
	var req CreateAliasInput
	if !decode(w, r, &req) {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if kerr := validateAliasName(req.AliasName); kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	rec, kerr := h.aliasTarget(r, req.TargetKeyID)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	ns := util.NamespaceFromHeader(r)
	if _, _, err := h.getAlias(ns, req.AliasName); err == nil {
		writeKMSError(w, &Error{http.StatusBadRequest, "AlreadyExistsException",
			"An alias with the name " + aliasArn(r, req.AliasName) + " already exists"})
		return
	}
	now := epoch(time.Now())
	if kerr := h.putAlias(ns, req.AliasName, &aliasRecord{
		AliasName: req.AliasName, TargetKeyID: rec.Metadata.KeyID, CreationDate: now, LastUpdatedDate: now,
	}, true); kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	writeKMSJSON(w, http.StatusOK, struct{}{})
}

// UpdateAlias points an alias at a different key of the same type and usage
func (h *Handler) UpdateAlias(w http.ResponseWriter, r *http.Request) {
	var req UpdateAliasInput
	if !decode(w, r, &req) {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if kerr := validateAliasName(req.AliasName); kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	ns := util.NamespaceFromHeader(r)
	_, ar, kerr := h.getAlias(ns, req.AliasName)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	rec, kerr := h.aliasTarget(r, req.TargetKeyID)
	if kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	if _, cur, err := h.getKey(ns, ar.TargetKeyID); err == nil &&
		(cur.Metadata.KeySpec != rec.Metadata.KeySpec || cur.Metadata.KeyUsage != rec.Metadata.KeyUsage) {
		writeKMSError(w, errValidation("The new target key must have the same key spec and key usage as the current target key"))
		return
	}
	ar.TargetKeyID = rec.Metadata.KeyID
	ar.LastUpdatedDate = epoch(time.Now())
	if kerr := h.putAlias(ns, req.AliasName, ar, false); kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	writeKMSJSON(w, http.StatusOK, struct{}{})
}

// DeleteAlias deletes an alias; the key is untouched
func (h *Handler) DeleteAlias(w http.ResponseWriter, r *http.Request) {
	var req DeleteAliasInput
	if !decode(w, r, &req) {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if kerr := validateAliasName(req.AliasName); kerr != nil {
		writeKMSError(w, kerr)
		return
	}
	ns := util.NamespaceFromHeader(r)
	if _, _, kerr := h.getAlias(ns, req.AliasName); kerr != nil {
		writeKMSError(w, errNotFound("Alias "+aliasArn(r, req.AliasName)+" is not found."))
		return
	}
	if err := h.Store.Delete(req.AliasName, "kms", "alias", ns); err != nil {
		writeKMSError(w, errInternal("Failed to delete alias: "+err.Error()))
		return
	}
	writeKMSJSON(w, http.StatusOK, struct{}{})
}

// ListAliases lists aliases sorted by name, optionally only those of one key
func (h *Handler) ListAliases(w http.ResponseWriter, r *http.Request) {
	var req ListAliasesInput
	if !decode(w, r, &req) {
		return
	}
	ns := util.NamespaceFromHeader(r)

	var keyID string
	if req.KeyID != "" {
		_, rec, kerr := h.resolveKey(r, req.KeyID, false)
		if kerr != nil {
			writeKMSError(w, kerr)
			return
		}
		keyID = rec.Metadata.KeyID
	}

	rows, err := h.Store.List("kms", "alias", ns)
	if err != nil {
		writeKMSError(w, errInternal("Failed to list aliases: "+err.Error()))
		return
	}
	aliases := make([]AliasListEntry, 0, len(rows))
	for _, row := range rows {
		var ar aliasRecord
		if json.Unmarshal(row.Attributes, &ar) != nil {
			continue
		}
		if keyID != "" && ar.TargetKeyID != keyID {
			continue
		}
		// Aliases of a key whose deletion has completed go with it.
		if _, _, kerr := h.getKey(ns, ar.TargetKeyID); kerr != nil && kerr.Type == "NotFoundException" {
			continue
		}
		aliases = append(aliases, AliasListEntry{
			AliasArn:        aliasArn(r, ar.AliasName),
			AliasName:       ar.AliasName,
			CreationDate:    ar.CreationDate,
			LastUpdatedDate: ar.LastUpdatedDate,
			TargetKeyID:     ar.TargetKeyID,
		})
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].AliasName < aliases[j].AliasName })

	start, end, next, perr := paginate(len(aliases), req.Limit, req.Marker, 50)
	if perr != nil {
		writeKMSError(w, &Error{http.StatusBadRequest, "InvalidMarkerException", "Invalid marker"})
		return
	}
	writeKMSJSON(w, http.StatusOK, ListAliasesOutput{Aliases: aliases[start:end], NextMarker: next, Truncated: next != ""})
}
