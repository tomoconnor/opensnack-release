// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package route53

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// normalizeName turns a DNS name into the form Route 53 stores and returns:
// lowercase, a trailing dot, and every byte other than a-z, 0-9, '-', '_'
// and the label separator written as a three-digit octal escape (so "*"
// becomes \052). Escapes that are already present are kept as they are.
func normalizeName(s string) string {
	if s == "" || s == "." {
		return "."
	}
	s = strings.TrimSuffix(s, ".")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			b.WriteByte(c)
		case c == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]):
			b.WriteString(s[i : i+4])
			i += 3
		default:
			fmt.Fprintf(&b, "\\%03o", c)
		}
	}
	b.WriteByte('.')
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// labels splits a normalized name into its labels, root first, which is the
// order ListResourceRecordSets sorts by ("com.example.www").
func labels(name string) []string {
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return nil
	}
	parts := strings.Split(name, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}

func compareNames(a, b string) int {
	la, lb := labels(a), labels(b)
	for i := 0; i < len(la) && i < len(lb); i++ {
		if c := strings.Compare(la[i], lb[i]); c != 0 {
			return c
		}
	}
	return len(la) - len(lb)
}

// compareRecordKeys orders record sets the way ListResourceRecordSets does:
// by name with the labels reversed, then type, then set identifier.
func compareRecordKeys(name1, type1, id1, name2, type2, id2 string) int {
	if c := compareNames(name1, name2); c != 0 {
		return c
	}
	if c := strings.Compare(type1, type2); c != 0 {
		return c
	}
	return strings.Compare(id1, id2)
}

// inZone reports whether name is the zone apex or below it.
func inZone(name, zone string) bool {
	return name == zone || strings.HasSuffix(name, "."+zone)
}

var validTypes = map[string]bool{
	"A": true, "AAAA": true, "CAA": true, "CNAME": true, "DS": true, "HTTPS": true,
	"MX": true, "NAPTR": true, "NS": true, "PTR": true, "SOA": true, "SPF": true,
	"SRV": true, "SSHFP": true, "SVCB": true, "TLSA": true, "TXT": true,
}

// validateValue checks one resource record value the way Route 53 does and
// returns the InvalidChangeBatch message for a bad one.
func validateValue(rrType, v string) string {
	bad := func(code, why string) string {
		return fmt.Sprintf("[Invalid Resource Record: 'FATAL problem: %s (%s) encountered with '%s'']", code, why, v)
	}
	switch rrType {
	case "A":
		if a, err := netip.ParseAddr(v); err != nil || !a.Is4() {
			return bad("ARRDATAIllegalIPv4Address", "Value is not a valid IPv4 address")
		}
	case "AAAA":
		if a, err := netip.ParseAddr(v); err != nil || !a.Is6() || a.Zone() != "" {
			return bad("AAAARRDATAIllegalIPv6Address", "Value is not a valid IPv6 address")
		}
	case "CNAME", "NS", "PTR":
		if !validHostname(v) {
			return bad("DomainLabelEmpty", "Domain label is empty")
		}
	case "MX":
		f := strings.Fields(v)
		if len(f) != 2 || !validUint(f[0], 65535) || !validHostname(f[1]) {
			return bad("MXRRDATANotTwoFields", "MX record must contain a priority and a domain name")
		}
	case "SRV":
		f := strings.Fields(v)
		if len(f) != 4 || !validUint(f[0], 65535) || !validUint(f[1], 65535) || !validUint(f[2], 65535) || !validHostname(f[3]) {
			return bad("SRVRRDATANotFourFields", "SRV record must contain priority, weight, port and a domain name")
		}
	case "CAA":
		f := strings.SplitN(v, " ", 3)
		if len(f) != 3 || !validUint(f[0], 255) || f[1] == "" || len(f[2]) < 2 || f[2][0] != '"' || f[2][len(f[2])-1] != '"' {
			return bad("CAARRDATANotThreeFields", "CAA record must contain flags, a tag and a quoted value")
		}
	case "TXT", "SPF":
		if v == "" {
			return bad("InvalidCharacterString", "Value should be enclosed in quotation marks")
		}
		strs, ok := characterStrings(v)
		if !ok {
			return bad("InvalidCharacterString", "Value should be enclosed in quotation marks")
		}
		for _, s := range strs {
			if len(s) > 255 {
				return bad("CharacterStringTooLong", "Value is too long")
			}
		}
	}
	return ""
}

func validUint(s string, max uint64) bool {
	n, err := strconv.ParseUint(s, 10, 32)
	return err == nil && n <= max
}

func validHostname(s string) bool {
	if s == "" || s == "." {
		return s == "."
	}
	s = strings.TrimSuffix(s, ".")
	if len(s) > 253 || strings.ContainsAny(s, " \t\"") {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if l == "" || len(l) > 63 {
			return false
		}
	}
	return true
}

// characterStrings splits a TXT value into its character strings: quoted
// strings (with \" and \\ escapes) or bare words, separated by whitespace.
func characterStrings(v string) ([]string, bool) {
	var out []string
	i := 0
	for i < len(v) {
		for i < len(v) && (v[i] == ' ' || v[i] == '\t') {
			i++
		}
		if i >= len(v) {
			break
		}
		var b strings.Builder
		if v[i] == '"' {
			i++
			closed := false
			for i < len(v) {
				c := v[i]
				if c == '\\' && i+1 < len(v) {
					if i+3 < len(v) && isDigit(v[i+1]) && isDigit(v[i+2]) && isDigit(v[i+3]) {
						b.WriteByte('x')
						i += 4
						continue
					}
					b.WriteByte(v[i+1])
					i += 2
					continue
				}
				if c == '"' {
					closed = true
					i++
					break
				}
				b.WriteByte(c)
				i++
			}
			if !closed {
				return nil, false
			}
		} else {
			for i < len(v) && v[i] != ' ' && v[i] != '\t' {
				if v[i] == '"' {
					return nil, false
				}
				b.WriteByte(v[i])
				i++
			}
		}
		out = append(out, b.String())
	}
	return out, true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
