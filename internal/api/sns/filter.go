// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sns

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Subscription filter policies.
//
// A policy is a JSON object. Every key must match (AND); a key's value is an
// array of conditions of which any may match (OR); "$or" takes an array of
// sub-policies. With FilterPolicyScope=MessageAttributes keys name message
// attributes; with MessageBody they are paths into the JSON body, nesting
// with nested objects. Conditions are exact strings, numbers, booleans,
// null, or one of the operators prefix, suffix, equals-ignore-case,
// anything-but, numeric and exists.

func validateFilterPolicy(policy string) error {
	var p map[string]any
	if err := json.Unmarshal([]byte(policy), &p); err != nil {
		return errors.New("failed to parse JSON")
	}
	return validatePolicyObject(p)
}

func validatePolicyObject(p map[string]any) error {
	for k, v := range p {
		switch v := v.(type) {
		case []any:
			if k == "$or" {
				for _, sub := range v {
					obj, ok := sub.(map[string]any)
					if !ok {
						return errors.New("$or must contain objects")
					}
					if err := validatePolicyObject(obj); err != nil {
						return err
					}
				}
				continue
			}
			for _, c := range v {
				if op, ok := c.(map[string]any); ok {
					for name := range op {
						switch name {
						case "prefix", "suffix", "equals-ignore-case", "anything-but", "numeric", "exists", "cidr", "wildcard":
						default:
							return errors.New("Unrecognized match type " + name)
						}
					}
				}
			}
		case map[string]any:
			if err := validatePolicyObject(v); err != nil {
				return err
			}
		default:
			return errors.New("\"" + k + "\" must be an object or an array")
		}
	}
	return nil
}

// filterMatches reports whether a message passes a subscription's filter
// policy. No policy matches everything.
func filterMatches(policy, scope string, attrs map[string]MessageAttribute, body string) (bool, error) {
	if policy == "" {
		return true, nil
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(policy), &p); err != nil {
		return false, err
	}
	if scope == "MessageBody" {
		var doc map[string]any
		if json.Unmarshal([]byte(body), &doc) != nil {
			return false, nil
		}
		return matchObject(p, doc), nil
	}
	doc := map[string]any{}
	for name, a := range attrs {
		switch {
		case a.DataType == "String.Array":
			var arr []any
			if json.Unmarshal([]byte(a.StringValue), &arr) == nil {
				doc[name] = arr
			}
		case strings.HasPrefix(a.DataType, "Number"):
			if n, err := strconv.ParseFloat(a.StringValue, 64); err == nil {
				doc[name] = n
			}
		case strings.HasPrefix(a.DataType, "String"):
			doc[name] = a.StringValue
		}
		// Binary attributes never match a filter.
	}
	return matchFlat(p, doc), nil
}

// matchFlat matches attribute policies: nested objects aren't allowed there.
func matchFlat(p map[string]any, doc map[string]any) bool {
	for k, v := range p {
		if k == "$or" {
			if !matchOr(v, doc, matchFlat) {
				return false
			}
			continue
		}
		conds, ok := v.([]any)
		if !ok {
			return false
		}
		val, present := doc[k]
		if !matchConditions(conds, val, present) {
			return false
		}
	}
	return true
}

func matchObject(p map[string]any, doc map[string]any) bool {
	for k, v := range p {
		if k == "$or" {
			if !matchOr(v, doc, matchObject) {
				return false
			}
			continue
		}
		val, present := doc[k]
		switch v := v.(type) {
		case map[string]any:
			sub, ok := val.(map[string]any)
			if !ok || !matchObject(v, sub) {
				return false
			}
		case []any:
			if !matchConditions(v, val, present) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func matchOr(v any, doc map[string]any, match func(map[string]any, map[string]any) bool) bool {
	subs, _ := v.([]any)
	for _, s := range subs {
		if obj, ok := s.(map[string]any); ok && match(obj, doc) {
			return true
		}
	}
	return false
}

// matchConditions reports whether any condition matches the value (any
// element, when the value is an array).
func matchConditions(conds []any, val any, present bool) bool {
	values := []any{val}
	if arr, ok := val.([]any); ok {
		values = arr
	}
	for _, c := range conds {
		if op, ok := c.(map[string]any); ok {
			if _, ok := op["exists"]; ok {
				want, _ := op["exists"].(bool)
				if present == want {
					return true
				}
				continue
			}
			if !present {
				continue
			}
			for _, v := range values {
				if matchOperator(op, v) {
					return true
				}
			}
			continue
		}
		if !present {
			continue
		}
		for _, v := range values {
			if equalValue(c, v) {
				return true
			}
		}
	}
	return false
}

func equalValue(cond, v any) bool {
	switch c := cond.(type) {
	case nil:
		return v == nil
	case string:
		s, ok := v.(string)
		return ok && s == c
	case float64:
		n, ok := v.(float64)
		return ok && n == c
	case bool:
		b, ok := v.(bool)
		return ok && b == c
	}
	return false
}

func matchOperator(op map[string]any, v any) bool {
	for name, arg := range op {
		switch name {
		case "prefix", "suffix", "equals-ignore-case":
			s, ok := v.(string)
			if !ok {
				return false
			}
			if m, ok := arg.(map[string]any); ok && name == "prefix" {
				// {"prefix": {"equals-ignore-case": "x"}}
				p, _ := m["equals-ignore-case"].(string)
				return strings.HasPrefix(strings.ToLower(s), strings.ToLower(p))
			}
			a, _ := arg.(string)
			switch name {
			case "prefix":
				return strings.HasPrefix(s, a)
			case "suffix":
				return strings.HasSuffix(s, a)
			default:
				return strings.EqualFold(s, a)
			}
		case "anything-but":
			switch a := arg.(type) {
			case []any:
				for _, x := range a {
					if equalValue(x, v) {
						return false
					}
				}
				return true
			case map[string]any:
				return !matchOperator(a, v)
			default:
				return !equalValue(a, v)
			}
		case "numeric":
			n, ok := v.(float64)
			if !ok {
				return false
			}
			args, _ := arg.([]any)
			for i := 0; i+1 < len(args); i += 2 {
				opName, _ := args[i].(string)
				bound, ok := args[i+1].(float64)
				if !ok {
					return false
				}
				var pass bool
				switch opName {
				case "=":
					pass = n == bound
				case "<":
					pass = n < bound
				case "<=":
					pass = n <= bound
				case ">":
					pass = n > bound
				case ">=":
					pass = n >= bound
				}
				if !pass {
					return false
				}
			}
			return true
		}
	}
	return false
}
