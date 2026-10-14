// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MessageAttributeValue is a message attribute as both protocols carry it.
// BinaryValue is base64 on the wire; encoding/json does that for []byte.
type MessageAttributeValue struct {
	DataType         string   `json:"DataType"`
	StringValue      string   `json:"StringValue,omitempty"`
	BinaryValue      []byte   `json:"BinaryValue,omitempty"`
	StringListValues []string `json:"StringListValues,omitempty"`
	BinaryListValues [][]byte `json:"BinaryListValues,omitempty"`
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// md5OfAttributes is the digest SQS returns as MD5OfMessageAttributes: for
// each attribute in name order, the name, the data type, a transport byte
// (1 string, 2 binary) and the value, each length-prefixed with a 4-byte
// big-endian length.
func md5OfAttributes(attrs map[string]MessageAttributeValue) string {
	if len(attrs) == 0 {
		return ""
	}
	names := make([]string, 0, len(attrs))
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)

	var buf []byte
	put := func(b []byte) {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(b)))
		buf = append(buf, b...)
	}
	for _, n := range names {
		v := attrs[n]
		put([]byte(n))
		put([]byte(v.DataType))
		if isBinaryType(v.DataType) {
			buf = append(buf, 2)
			put(v.BinaryValue)
		} else {
			buf = append(buf, 1)
			put([]byte(v.StringValue))
		}
	}
	return md5Hex(buf)
}

func isBinaryType(dt string) bool { return strings.HasPrefix(dt, "Binary") }

var attrNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`)

// validateAttributes applies SQS's rules for message attributes.
func validateAttributes(attrs map[string]MessageAttributeValue) *apiError {
	if len(attrs) > 10 {
		return errInvalidParam("Number of message attributes [%d] exceeds the allowed maximum [10].", len(attrs))
	}
	for name, v := range attrs {
		if !attrNameRe.MatchString(name) || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
			return errInvalidParam("Message attribute name '%s' is invalid.", name)
		}
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "aws.") || strings.HasPrefix(lower, "amazon.") {
			return errInvalidParam("Message attribute name '%s' is reserved.", name)
		}
		if v.DataType == "" {
			return errInvalidParam("The message attribute '%s' must contain non-empty message attribute type.", name)
		}
		base, _, _ := strings.Cut(v.DataType, ".")
		switch base {
		case "String":
			if v.StringValue == "" {
				return errInvalidParam("The message attribute '%s' with type 'String' must use field 'String'.", name)
			}
		case "Number":
			if _, err := strconv.ParseFloat(v.StringValue, 64); err != nil {
				return errInvalidParam("Can't cast the value of message (user) attribute '%s' to a number.", name)
			}
		case "Binary":
			if len(v.BinaryValue) == 0 {
				return errInvalidParam("The message attribute '%s' with type 'Binary' must use field 'Binary'.", name)
			}
		default:
			return errInvalidParam("The type of message (user) attribute '%s' is invalid. You must use only the following supported type prefixes: Binary, Number, String.", name)
		}
	}
	return nil
}

func attributesSize(attrs map[string]MessageAttributeValue) int {
	n := 0
	for name, v := range attrs {
		n += len(name) + len(v.DataType) + len(v.StringValue) + len(v.BinaryValue)
	}
	return n
}

// validBody reports whether every character is one SQS accepts:
// #x9 | #xA | #xD | #x20-#xD7FF | #xE000-#xFFFD | #x10000-#x10FFFF.
func validBody(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == 0x9 || r == 0xA || r == 0xD:
		case r >= 0x20 && r <= 0xD7FF:
		case r >= 0xE000 && r <= 0xFFFD:
		case r >= 0x10000 && r <= 0x10FFFF:
		default:
			return false
		}
	}
	return true
}
