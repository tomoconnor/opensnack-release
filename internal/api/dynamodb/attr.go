// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"
)

// AttributeValue is one DynamoDB value. Type is the wire tag ("S", "N", "B",
// "SS", "NS", "BS", "M", "L", "NULL", "BOOL"). Strings and numbers live in
// S; binary values are kept as raw bytes in S too, so sets of all three kinds
// share Set.
type AttributeValue struct {
	Type string
	S    string
	Set  []string
	M    map[string]*AttributeValue
	L    []*AttributeValue
	Bool bool
}

// Item is a whole item, keyed by top-level attribute name.
type Item map[string]*AttributeValue

func strAV(s string) *AttributeValue { return &AttributeValue{Type: "S", S: s} }
func numAV(n string) *AttributeValue { return &AttributeValue{Type: "N", S: n} }
func boolAV(b bool) *AttributeValue  { return &AttributeValue{Type: "BOOL", Bool: b} }
func nullAV() *AttributeValue        { return &AttributeValue{Type: "NULL", Bool: true} }
func binAV(b []byte) *AttributeValue { return &AttributeValue{Type: "B", S: string(b)} }
func listAV(l ...*AttributeValue) *AttributeValue {
	if l == nil {
		l = []*AttributeValue{}
	}
	return &AttributeValue{Type: "L", L: l}
}

func (v *AttributeValue) MarshalJSON() ([]byte, error) {
	var inner any
	switch v.Type {
	case "S", "N":
		inner = v.S
	case "B":
		inner = base64.StdEncoding.EncodeToString([]byte(v.S))
	case "SS", "NS":
		inner = v.Set
	case "BS":
		out := make([]string, len(v.Set))
		for i, s := range v.Set {
			out[i] = base64.StdEncoding.EncodeToString([]byte(s))
		}
		inner = out
	case "M":
		m := v.M
		if m == nil {
			m = map[string]*AttributeValue{}
		}
		inner = m
	case "L":
		l := v.L
		if l == nil {
			l = []*AttributeValue{}
		}
		inner = l
	case "NULL":
		inner = true
	case "BOOL":
		inner = v.Bool
	default:
		return nil, fmt.Errorf("attribute value has no type")
	}
	return json.Marshal(map[string]any{v.Type: inner})
}

func (v *AttributeValue) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 1 {
		return validationErr("Supplied AttributeValue has more than one datatypes set, must contain exactly one of the supported datatypes")
	}
	for typ, body := range raw {
		*v = AttributeValue{Type: typ}
		switch typ {
		case "S":
			return json.Unmarshal(body, &v.S)
		case "N":
			if err := json.Unmarshal(body, &v.S); err != nil {
				return err
			}
			if _, ok := parseNumber(v.S); !ok {
				return validationErr("A value provided cannot be converted into a number")
			}
		case "B":
			var b []byte
			if err := json.Unmarshal(body, &b); err != nil {
				return err
			}
			v.S = string(b)
		case "SS", "NS":
			if err := json.Unmarshal(body, &v.Set); err != nil {
				return err
			}
			if typ == "NS" {
				for _, n := range v.Set {
					if _, ok := parseNumber(n); !ok {
						return validationErr("A value provided cannot be converted into a number")
					}
				}
			}
		case "BS":
			var bs [][]byte
			if err := json.Unmarshal(body, &bs); err != nil {
				return err
			}
			for _, b := range bs {
				v.Set = append(v.Set, string(b))
			}
		case "M":
			return json.Unmarshal(body, &v.M)
		case "L":
			return json.Unmarshal(body, &v.L)
		case "NULL":
			v.Bool = true
		case "BOOL":
			return json.Unmarshal(body, &v.Bool)
		default:
			return validationErr("Supplied AttributeValue has an unknown datatype: " + typ)
		}
	}
	return nil
}

func (v *AttributeValue) isSet() bool {
	return v.Type == "SS" || v.Type == "NS" || v.Type == "BS"
}

func (v *AttributeValue) clone() *AttributeValue {
	if v == nil {
		return nil
	}
	c := *v
	if v.Set != nil {
		c.Set = append([]string(nil), v.Set...)
	}
	if v.M != nil {
		c.M = make(map[string]*AttributeValue, len(v.M))
		for k, e := range v.M {
			c.M[k] = e.clone()
		}
	}
	if v.L != nil {
		c.L = make([]*AttributeValue, len(v.L))
		for i, e := range v.L {
			c.L[i] = e.clone()
		}
	}
	return &c
}

func (it Item) clone() Item {
	if it == nil {
		return nil
	}
	out := make(Item, len(it))
	for k, v := range it {
		out[k] = v.clone()
	}
	return out
}

// parseNumber reads a DynamoDB number. big.Rat keeps 38 digits of precision
// exactly, which float64 would not.
func parseNumber(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

// formatNumber renders a computed number the way DynamoDB does: plain
// decimal, no exponent, no trailing zeros.
func formatNumber(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	s := r.FloatString(38)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// canonical returns a string that is equal for equal values of a scalar
// key type, so numbers like "1" and "1.0" are the same key.
func (v *AttributeValue) canonical() string {
	switch v.Type {
	case "N":
		if r, ok := parseNumber(v.S); ok {
			return "N:" + r.RatString()
		}
	case "B":
		return "B:" + base64.StdEncoding.EncodeToString([]byte(v.S))
	}
	return v.Type + ":" + v.S
}

// equalAV is DynamoDB equality: same type and same value. Sets compare
// without regard to order; numbers compare numerically.
func equalAV(a, b *AttributeValue) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case "N":
		x, ok1 := parseNumber(a.S)
		y, ok2 := parseNumber(b.S)
		return ok1 && ok2 && x.Cmp(y) == 0
	case "S", "B":
		return a.S == b.S
	case "BOOL", "NULL":
		return a.Bool == b.Bool
	case "SS", "BS", "NS":
		if len(a.Set) != len(b.Set) {
			return false
		}
		have := map[string]bool{}
		for _, s := range a.Set {
			have[(&AttributeValue{Type: a.Type[:1], S: s}).canonical()] = true
		}
		for _, s := range b.Set {
			if !have[(&AttributeValue{Type: b.Type[:1], S: s}).canonical()] {
				return false
			}
		}
		return true
	case "L":
		if len(a.L) != len(b.L) {
			return false
		}
		for i := range a.L {
			if !equalAV(a.L[i], b.L[i]) {
				return false
			}
		}
		return true
	case "M":
		if len(a.M) != len(b.M) {
			return false
		}
		for k, x := range a.M {
			if !equalAV(x, b.M[k]) {
				return false
			}
		}
		return true
	}
	return false
}

// compareScalar orders two values of the same scalar type (N, S or B). ok is
// false when they can't be ordered.
func compareScalar(a, b *AttributeValue) (int, bool) {
	if a == nil || b == nil || a.Type != b.Type {
		return 0, false
	}
	switch a.Type {
	case "N":
		x, ok1 := parseNumber(a.S)
		y, ok2 := parseNumber(b.S)
		if !ok1 || !ok2 {
			return 0, false
		}
		return x.Cmp(y), true
	case "S", "B":
		return strings.Compare(a.S, b.S), true
	}
	return 0, false
}

// sizeOf implements the size() function.
func sizeOf(v *AttributeValue) (int, bool) {
	switch v.Type {
	case "S":
		return utf8.RuneCountInString(v.S), true
	case "B":
		return len(v.S), true
	case "SS", "NS", "BS":
		return len(v.Set), true
	case "L":
		return len(v.L), true
	case "M":
		return len(v.M), true
	}
	return 0, false
}

// itemSize approximates DynamoDB's item size accounting (names plus values).
func itemSize(it Item) int {
	n := 0
	for k, v := range it {
		n += len(k) + valueSize(v)
	}
	return n
}

func valueSize(v *AttributeValue) int {
	switch v.Type {
	case "S", "B", "N":
		return len(v.S)
	case "SS", "NS", "BS":
		n := 0
		for _, s := range v.Set {
			n += len(s)
		}
		return n
	case "L":
		n := 3
		for _, e := range v.L {
			n += 1 + valueSize(e)
		}
		return n
	case "M":
		n := 3
		for k, e := range v.M {
			n += 1 + len(k) + valueSize(e)
		}
		return n
	}
	return 1
}

// setUnion and setDifference implement ADD and DELETE on sets.
func setUnion(a, b *AttributeValue) *AttributeValue {
	out := a.clone()
	seen := map[string]bool{}
	elem := a.Type[:1]
	for _, s := range a.Set {
		seen[(&AttributeValue{Type: elem, S: s}).canonical()] = true
	}
	for _, s := range b.Set {
		c := (&AttributeValue{Type: elem, S: s}).canonical()
		if !seen[c] {
			seen[c] = true
			out.Set = append(out.Set, s)
		}
	}
	return out
}

func setDifference(a, b *AttributeValue) *AttributeValue {
	elem := a.Type[:1]
	drop := map[string]bool{}
	for _, s := range b.Set {
		drop[(&AttributeValue{Type: elem, S: s}).canonical()] = true
	}
	out := &AttributeValue{Type: a.Type}
	for _, s := range a.Set {
		if !drop[(&AttributeValue{Type: elem, S: s}).canonical()] {
			out.Set = append(out.Set, s)
		}
	}
	return out
}

// sortedKeys returns the map's keys in order, for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validateValue rejects values DynamoDB refuses to store: empty sets and
// duplicate set members.
func validateValue(v *AttributeValue) error {
	switch v.Type {
	case "SS", "NS", "BS":
		if len(v.Set) == 0 {
			return validationErr("One or more parameter values were invalid: An string set  may not be empty")
		}
		seen := map[string]bool{}
		for _, s := range v.Set {
			c := (&AttributeValue{Type: v.Type[:1], S: s}).canonical()
			if seen[c] {
				return validationErr("One or more parameter values were invalid: Input collection contains duplicates")
			}
			seen[c] = true
		}
	case "L":
		for _, e := range v.L {
			if e == nil {
				return validationErr("Supplied AttributeValue is empty, must contain exactly one of the supported datatypes")
			}
			if err := validateValue(e); err != nil {
				return err
			}
		}
	case "M":
		for _, e := range v.M {
			if e == nil {
				return validationErr("Supplied AttributeValue is empty, must contain exactly one of the supported datatypes")
			}
			if err := validateValue(e); err != nil {
				return err
			}
		}
	case "":
		return validationErr("Supplied AttributeValue is empty, must contain exactly one of the supported datatypes")
	}
	return nil
}
