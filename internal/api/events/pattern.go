// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
)

// Pattern is a compiled EventBridge event pattern.
//
// A pattern is a JSON object whose leaves are arrays of matchers. An event
// matches when every field the pattern names matches (AND), where a leaf
// matches when any of its matchers does (OR). Arrays in the event match when
// any element does. "$or" at any level matches when any of its sub-patterns
// does.
type Pattern struct {
	fields []fieldPattern
	or     [][]*Pattern // each "$or" key holds alternatives
}

type fieldPattern struct {
	name   string
	nested *Pattern   // the pattern value was an object
	leaf   []*matcher // the pattern value was an array
}

type matchKind int

const (
	mExact matchKind = iota
	mPrefix
	mSuffix
	mEqualsIgnoreCase
	mPrefixIgnoreCase
	mSuffixIgnoreCase
	mWildcard
	mAnythingBut
	mNumeric
	mExists
	mCIDR
)

type matcher struct {
	kind matchKind
	// value is the literal for exact matches: a string, float64, bool or nil.
	value  any
	str    string
	exists bool
	// numeric holds (op, value) pairs that must all hold.
	numeric []numCond
	cidr    netip.Prefix
	// not is the set anything-but excludes: every entry must fail to match.
	not []*matcher
}

type numCond struct {
	op string
	v  float64
}

// CompilePattern parses and validates an event pattern. Errors read like
// AWS's InvalidEventPatternException messages.
func CompilePattern(s string) (*Pattern, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("Event pattern is not valid. Reason: %v", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("Event pattern is not valid. Reason: trailing data")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Event pattern is not valid. Reason: Filter is not an object")
	}
	return compileObject(obj, "")
}

func compileObject(obj map[string]any, path string) (*Pattern, error) {
	p := &Pattern{}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := obj[k]
		if k == "$or" {
			alts, ok := v.([]any)
			if !ok || len(alts) < 2 {
				return nil, invalid(path+k, "$or must be an array of at least two objects")
			}
			var compiled []*Pattern
			for _, a := range alts {
				ao, ok := a.(map[string]any)
				if !ok {
					return nil, invalid(path+k, "$or must contain objects")
				}
				sub, err := compileObject(ao, path)
				if err != nil {
					return nil, err
				}
				compiled = append(compiled, sub)
			}
			p.or = append(p.or, compiled)
			continue
		}
		switch t := v.(type) {
		case map[string]any:
			sub, err := compileObject(t, path+k+".")
			if err != nil {
				return nil, err
			}
			p.fields = append(p.fields, fieldPattern{name: k, nested: sub})
		case []any:
			if len(t) == 0 {
				return nil, invalid(path+k, "Empty arrays are not allowed")
			}
			var ms []*matcher
			for _, e := range t {
				m, err := compileMatcher(e, path+k)
				if err != nil {
					return nil, err
				}
				ms = append(ms, m)
			}
			p.fields = append(p.fields, fieldPattern{name: k, leaf: ms})
		default:
			return nil, invalid(path+k, "Match value must be String, number, true, false, or null")
		}
	}
	return p, nil
}

func invalid(field, reason string) error {
	return fmt.Errorf("Event pattern is not valid. Reason: %s at %s", reason, field)
}

func literal(v any) (any, bool) {
	switch t := v.(type) {
	case nil, string, bool:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return nil, false
}

func compileMatcher(v any, field string) (*matcher, error) {
	if lit, ok := literal(v); ok {
		return &matcher{kind: mExact, value: lit}, nil
	}
	obj, ok := v.(map[string]any)
	if !ok || len(obj) != 1 {
		return nil, invalid(field, "Match value must be String, number, true, false, null, or a single-key object")
	}
	for op, arg := range obj {
		switch op {
		case "prefix", "suffix":
			return compileAffix(op, arg, field)
		case "equals-ignore-case":
			s, ok := arg.(string)
			if !ok {
				return nil, invalid(field, "equals-ignore-case match pattern must be a string")
			}
			return &matcher{kind: mEqualsIgnoreCase, str: s}, nil
		case "wildcard":
			s, ok := arg.(string)
			if !ok {
				return nil, invalid(field, "wildcard match pattern must be a string")
			}
			if strings.Contains(s, "**") {
				return nil, invalid(field, "Consecutive wildcard characters are not allowed")
			}
			return &matcher{kind: mWildcard, str: s}, nil
		case "exists":
			b, ok := arg.(bool)
			if !ok {
				return nil, invalid(field, "exists match pattern must be either true or false")
			}
			return &matcher{kind: mExists, exists: b}, nil
		case "cidr":
			s, ok := arg.(string)
			if !ok {
				return nil, invalid(field, "cidr match pattern must be a string")
			}
			pfx, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, invalid(field, "Malformed CIDR, one '/' required")
			}
			return &matcher{kind: mCIDR, cidr: pfx.Masked()}, nil
		case "numeric":
			return compileNumeric(arg, field)
		case "anything-but":
			return compileAnythingBut(arg, field)
		default:
			return nil, invalid(field, "Unrecognized match type "+op)
		}
	}
	return nil, invalid(field, "empty match object")
}

// compileAffix handles {"prefix": "x"} and {"prefix": {"equals-ignore-case": "x"}}.
func compileAffix(op string, arg any, field string) (*matcher, error) {
	kind, ci := mPrefix, mPrefixIgnoreCase
	if op == "suffix" {
		kind, ci = mSuffix, mSuffixIgnoreCase
	}
	switch t := arg.(type) {
	case string:
		return &matcher{kind: kind, str: t}, nil
	case map[string]any:
		if s, ok := t["equals-ignore-case"].(string); ok && len(t) == 1 {
			return &matcher{kind: ci, str: strings.ToLower(s)}, nil
		}
	}
	return nil, invalid(field, op+" match pattern must be a string")
}

func compileNumeric(arg any, field string) (*matcher, error) {
	arr, ok := arg.([]any)
	if !ok || len(arr) == 0 || len(arr)%2 != 0 || len(arr) > 4 {
		return nil, invalid(field, "Value of numeric must be an array of operator/number pairs")
	}
	m := &matcher{kind: mNumeric}
	for i := 0; i < len(arr); i += 2 {
		op, ok := arr[i].(string)
		if !ok {
			return nil, invalid(field, "Unrecognized numeric range operator")
		}
		switch op {
		case "=", "<", "<=", ">", ">=":
		default:
			return nil, invalid(field, "Unrecognized numeric range operator: "+op)
		}
		n, ok := arr[i+1].(json.Number)
		if !ok {
			return nil, invalid(field, "Value of "+op+" must be numeric")
		}
		f, err := n.Float64()
		if err != nil {
			return nil, invalid(field, "Value of "+op+" must be numeric")
		}
		m.numeric = append(m.numeric, numCond{op, f})
	}
	if len(m.numeric) == 2 {
		lo, hi := m.numeric[0], m.numeric[1]
		if !strings.HasPrefix(lo.op, ">") || !strings.HasPrefix(hi.op, "<") || lo.v >= hi.v {
			return nil, invalid(field, "Bad numeric range")
		}
	}
	return m, nil
}

func compileAnythingBut(arg any, field string) (*matcher, error) {
	m := &matcher{kind: mAnythingBut}
	if lit, ok := literal(arg); ok {
		if arg == nil {
			return nil, invalid(field, "Value of anything-but must be an array or single string/number value")
		}
		m.not = []*matcher{{kind: mExact, value: lit}}
		return m, nil
	}
	switch t := arg.(type) {
	case []any:
		if len(t) == 0 {
			return nil, invalid(field, "Empty arrays are not allowed")
		}
		for _, e := range t {
			lit, ok := literal(e)
			if !ok || e == nil {
				return nil, invalid(field, "Inside anything but list, start|null|boolean is not supported.")
			}
			m.not = append(m.not, &matcher{kind: mExact, value: lit})
		}
		return m, nil
	case map[string]any:
		if len(t) != 1 {
			break
		}
		for op, inner := range t {
			switch op {
			case "prefix", "suffix":
				sub, err := compileAffix(op, inner, field)
				if err != nil {
					return nil, err
				}
				m.not = []*matcher{sub}
				return m, nil
			case "equals-ignore-case", "wildcard":
				vals, ok := inner.([]any)
				if !ok {
					vals = []any{inner}
				}
				for _, v := range vals {
					sub, err := compileMatcher(map[string]any{op: v}, field)
					if err != nil {
						return nil, err
					}
					m.not = append(m.not, sub)
				}
				return m, nil
			}
		}
	}
	return nil, invalid(field, "Unsupported anything-but pattern")
}

// Match reports whether the event (a JSON document) matches.
func (p *Pattern) Match(event []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(event))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return false
	}
	return p.MatchValue(v)
}

// MatchValue matches an already-decoded event. Numbers may be json.Number or
// float64.
func (p *Pattern) MatchValue(event any) bool {
	return p.matchObject(event, true)
}

// matchObject matches p against v, which should be an object. present is
// false when the field holding v was missing, so only exists:false and
// similar can match beneath it.
func (p *Pattern) matchObject(v any, present bool) bool {
	// An array of objects matches when any element does (EventBridge
	// flattens arrays along the path).
	if arr, ok := v.([]any); ok && present {
		for _, e := range arr {
			if p.matchObject(e, true) {
				return true
			}
		}
		return len(arr) == 0 && p.matchObject(nil, false)
	}
	obj, _ := v.(map[string]any)
	for _, f := range p.fields {
		child, ok := obj[f.name]
		if f.nested != nil {
			if !f.nested.matchObject(child, ok) {
				return false
			}
			continue
		}
		if !matchLeaf(f.leaf, child, ok) {
			return false
		}
	}
	for _, alts := range p.or {
		hit := false
		for _, a := range alts {
			if a.matchObject(v, present) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func matchLeaf(ms []*matcher, v any, present bool) bool {
	if arr, ok := v.([]any); ok && present {
		if len(arr) == 0 {
			return matchLeaf(ms, nil, false)
		}
		for _, e := range arr {
			if matchLeaf(ms, e, true) {
				return true
			}
		}
		return false
	}
	for _, m := range ms {
		if m.match(v, present) {
			return true
		}
	}
	return false
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	}
	return 0, false
}

func (m *matcher) match(v any, present bool) bool {
	if m.kind == mExists {
		_, isObj := v.(map[string]any)
		return m.exists == (present && !isObj)
	}
	if !present {
		return false
	}
	if _, isObj := v.(map[string]any); isObj {
		return false
	}
	s, isStr := v.(string)
	switch m.kind {
	case mExact:
		switch want := m.value.(type) {
		case nil:
			return v == nil
		case string:
			return isStr && s == want
		case bool:
			b, ok := v.(bool)
			return ok && b == want
		case float64:
			f, ok := number(v)
			return ok && f == want
		}
	case mPrefix:
		return isStr && strings.HasPrefix(s, m.str)
	case mSuffix:
		return isStr && strings.HasSuffix(s, m.str)
	case mPrefixIgnoreCase:
		return isStr && strings.HasPrefix(strings.ToLower(s), m.str)
	case mSuffixIgnoreCase:
		return isStr && strings.HasSuffix(strings.ToLower(s), m.str)
	case mEqualsIgnoreCase:
		return isStr && strings.EqualFold(s, m.str)
	case mWildcard:
		return isStr && wildcardMatch(m.str, s)
	case mCIDR:
		if !isStr {
			return false
		}
		addr, err := netip.ParseAddr(s)
		return err == nil && m.cidr.Contains(addr)
	case mNumeric:
		f, ok := number(v)
		if !ok {
			return false
		}
		for _, c := range m.numeric {
			if !compare(f, c) {
				return false
			}
		}
		return true
	case mAnythingBut:
		// Only strings and numbers can satisfy anything-but; the excluded
		// set's kind decides which.
		if !isStr {
			if _, ok := number(v); !ok {
				return false
			}
		}
		for _, n := range m.not {
			if n.match(v, true) {
				return false
			}
		}
		if n := m.not[0]; n.kind != mExact {
			return isStr // prefix/suffix/ignore-case/wildcard only speak strings
		}
		return true
	}
	return false
}

func compare(f float64, c numCond) bool {
	const eps = 1e-9
	switch c.op {
	case "=":
		return math.Abs(f-c.v) < eps
	case "<":
		return f < c.v
	case "<=":
		return f <= c.v
	case ">":
		return f > c.v
	case ">=":
		return f >= c.v
	}
	return false
}

// wildcardMatch matches s against a pattern where * stands for any run of
// characters and \* for a literal star.
func wildcardMatch(pattern, s string) bool {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '\\' && i+1 < len(pattern) {
			i++
			cur.WriteByte(pattern[i])
			continue
		}
		if c == '*' {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	parts = append(parts, cur.String())
	if len(parts) == 1 {
		return s == parts[0]
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return len(s) >= len(last) && strings.HasSuffix(s, last)
}
