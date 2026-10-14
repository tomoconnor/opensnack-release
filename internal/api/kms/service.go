// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms

import (
	"crypto/rand"
	"errors"
	"io"
	"net/http"
)

// The functions here are the in-process KMS other services call for
// encryption at rest (SSM SecureString today, SSE later). They act in the
// caller's tenant and region, taken from r, and return *Error on failure.

func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return errInternal(err.Error())
}

// EncryptFor seals plaintext under the key keyRef names (key ID, key ARN,
// alias name or alias ARN) and returns the blob and the key's ARN.
func (h *Handler) EncryptFor(r *http.Request, keyRef string, plaintext []byte, ctx map[string]string) ([]byte, string, error) {
	md, material, kerr := h.loadForCrypto(r, keyRef)
	if kerr != nil {
		return nil, "", kerr
	}
	blob, err := encryptBlob(md.KeyID, material, plaintext, ctx)
	if err != nil {
		return nil, "", errInternal("Failed to encrypt: " + err.Error())
	}
	return blob, md.ARN, nil
}

// DecryptFor opens a blob from EncryptFor or GenerateDataKeyFor and returns
// the plaintext and the ARN of the key that sealed it.
func (h *Handler) DecryptFor(r *http.Request, blob []byte, ctx map[string]string) ([]byte, string, error) {
	keyID, body, err := splitBlob(blob)
	if err != nil {
		return nil, "", &Error{http.StatusBadRequest, "InvalidCiphertextException",
			"The ciphertext refers to a key that does not exist or is malformed"}
	}
	md, material, kerr := h.loadForCrypto(r, keyID)
	if kerr != nil {
		return nil, "", kerr
	}
	plaintext, err := decryptBlob(material, body, ctx)
	if err != nil {
		return nil, "", &Error{http.StatusBadRequest, "InvalidCiphertextException",
			"The ciphertext could not be decrypted with the supplied encryption context"}
	}
	return plaintext, md.ARN, nil
}

// GenerateDataKeyFor returns n random bytes in plaintext and sealed under keyRef.
func (h *Handler) GenerateDataKeyFor(r *http.Request, keyRef string, n int, ctx map[string]string) (plaintext, blob []byte, keyARN string, err error) {
	plaintext = make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, plaintext); err != nil {
		return nil, nil, "", errInternal("Failed to generate data key: " + err.Error())
	}
	blob, keyARN, err = h.EncryptFor(r, keyRef, plaintext, ctx)
	if err != nil {
		return nil, nil, "", err
	}
	return plaintext, blob, keyARN, nil
}

// KeyARN resolves keyRef to the ARN of the key it names, creating an AWS
// managed key for alias/aws/<service> on first use.
func (h *Handler) KeyARN(r *http.Request, keyRef string) (string, error) {
	_, rec, kerr := h.resolveKey(r, keyRef, true)
	if kerr != nil {
		return "", kerr
	}
	return rec.Metadata.ARN, nil
}
