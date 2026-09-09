// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"sort"
)

const (
	// keyMaterialBytes is the size of the symmetric material backing a
	// SYMMETRIC_DEFAULT key: AES-256 wants 32 bytes.
	keyMaterialBytes = 32

	// ciphertextVersion prefixes every blob so the format can change later
	// without silently mis-parsing blobs minted by an older build.
	ciphertextVersion = 0x01

	// gcmNonceSize is the standard AES-GCM nonce size used by crypto/cipher.
	gcmNonceSize = 12

	// maxPlaintextBytes mirrors the AWS limit on Encrypt for a symmetric key.
	maxPlaintextBytes = 4096
)

// errInvalidCiphertext covers every way a blob can fail to open. The reason is
// deliberately not reported back to the caller, matching KMS.
var errInvalidCiphertext = errors.New("invalid ciphertext blob")

// newKeyMaterial returns freshly generated AES-256 key material.
func newKeyMaterial() ([]byte, error) {
	b := make([]byte, keyMaterialBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

func newGCM(material []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// encryptionContextAAD canonicalises an encryption context into the additional
// authenticated data for AES-GCM, which is what makes Decrypt fail when the
// context does not match. Keys are sorted so map order cannot change the AAD,
// and each key and value is length-prefixed so {"a": "bc"} and {"ab": "c"}
// cannot encode identically.
func encryptionContextAAD(ctx map[string]string) []byte {
	if len(ctx) == 0 {
		return nil
	}

	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var (
		aad []byte
		n   [4]byte
	)
	for _, k := range keys {
		v := ctx[k]

		binary.BigEndian.PutUint32(n[:], uint32(len(k)))
		aad = append(aad, n[:]...)
		aad = append(aad, k...)

		binary.BigEndian.PutUint32(n[:], uint32(len(v)))
		aad = append(aad, n[:]...)
		aad = append(aad, v...)
	}
	return aad
}

// encryptBlob seals plaintext under the key material with AES-256-GCM. The blob
// carries the key ID and nonce so that Decrypt is self-contained, the same way
// a real KMS ciphertext blob names the key that produced it:
//
//	version(1) | keyIDLen(1) | keyID | nonce(12) | ciphertext+tag
func encryptBlob(keyID string, material, plaintext []byte, ctx map[string]string) ([]byte, error) {
	if len(keyID) == 0 || len(keyID) > 255 {
		return nil, errors.New("key id does not fit the ciphertext blob header")
	}

	gcm, err := newGCM(material)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	blob := make([]byte, 0, 2+len(keyID)+len(nonce)+len(plaintext)+gcm.Overhead())
	blob = append(blob, ciphertextVersion, byte(len(keyID)))
	blob = append(blob, keyID...)
	blob = append(blob, nonce...)

	return gcm.Seal(blob, nonce, plaintext, encryptionContextAAD(ctx)), nil
}

// splitBlob returns the key ID a blob was sealed under along with the remaining
// nonce and ciphertext. Decrypt needs the key ID before it can load the material
// to open the blob, so this half runs without any key.
func splitBlob(blob []byte) (string, []byte, error) {
	if len(blob) < 2 || blob[0] != ciphertextVersion {
		return "", nil, errInvalidCiphertext
	}

	idLen := int(blob[1])
	if len(blob) < 2+idLen+gcmNonceSize {
		return "", nil, errInvalidCiphertext
	}

	return string(blob[2 : 2+idLen]), blob[2+idLen:], nil
}

// decryptBlob opens the nonce-and-ciphertext half of a blob returned by splitBlob.
func decryptBlob(material, body []byte, ctx map[string]string) ([]byte, error) {
	gcm, err := newGCM(material)
	if err != nil {
		return nil, err
	}

	if len(body) < gcm.NonceSize() {
		return nil, errInvalidCiphertext
	}

	nonce, sealed := body[:gcm.NonceSize()], body[gcm.NonceSize():]

	plaintext, err := gcm.Open(nil, nonce, sealed, encryptionContextAAD(ctx))
	if err != nil {
		return nil, errInvalidCiphertext
	}
	return plaintext, nil
}
