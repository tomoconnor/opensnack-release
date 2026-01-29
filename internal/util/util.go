// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package util

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// RandomHex returns a securely generated random hex string
// of the given byte length. For example, RandomHex(16) → 32 hex chars.
func RandomHex(n int) string {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		// Should never happen on modern systems; fallback to zeros
		for i := range b {
			b[i] = 0
		}
	}
	return hex.EncodeToString(b)
}

// DeterministicHex returns a deterministic hex string of the given length
// based on the input string. For example, DeterministicHex("zone-id", 12) → 12 hex chars.
// The same input will always produce the same output.
func DeterministicHex(input string, n int) string {
	hash := md5Sum(input)
	// MD5 produces 32 hex characters, take first n
	if n > len(hash) {
		n = len(hash)
	}
	return hash[:n]
}

func StaticIDHash(input string, n int) string {
	// MD5 Hash but truncated to n chars
	hash := md5Sum(input)
	return hash[:n]
}

func md5Sum(input string) string {
	// Import "crypto/md5"
	h := md5.New()
	h.Write([]byte(input))
	return hex.EncodeToString(h.Sum(nil))
}

func ExtractBucketFromARN(arn string) string {
	// Example ARN: arn:aws:s3:::mybucket
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return ""
	}
	resource := parts[5] // :::mybucket
	return strings.TrimPrefix(resource, ":::")
}

func DecodeAWSJSON(r *http.Request, v any) error {
	body := r.Body
	defer body.Close()

	// Always drain the body
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}

	if len(data) == 0 {
		return nil
	}

	return json.Unmarshal(data, v)
}
