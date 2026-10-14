// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"strconv"
	"strings"
)

// matches applies Describe* filters: every filter must match, and a filter
// matches when any of its values matches any of the resource's values.
// Values may use the * and ? wildcards.
func matches(fs []filter, o object) (bool, error) {
	if len(fs) == 0 {
		return true, nil
	}
	vals := o.filterValues()
	for _, f := range fs {
		have, err := filterField(f.name, vals, *o.tagList())
		if err != nil {
			return false, err
		}
		if !anyMatch(f.values, have) {
			return false, nil
		}
	}
	return true, nil
}

// matchVals is matches for things without tags (zones, images, types).
func matchVals(fs []filter, vals map[string][]string) (bool, error) {
	for _, f := range fs {
		have, err := filterField(f.name, vals, nil)
		if err != nil {
			return false, err
		}
		if !anyMatch(f.values, have) {
			return false, nil
		}
	}
	return true, nil
}

func filterField(name string, vals map[string][]string, tags []Tag) ([]string, error) {
	switch {
	case strings.HasPrefix(name, "tag:"):
		key := name[4:]
		var out []string
		for _, t := range tags {
			if t.Key == key {
				out = append(out, t.Value)
			}
		}
		return out, nil
	case name == "tag-key":
		var out []string
		for _, t := range tags {
			out = append(out, t.Key)
		}
		return out, nil
	case name == "tag-value":
		var out []string
		for _, t := range tags {
			out = append(out, t.Value)
		}
		return out, nil
	}
	v, ok := vals[name]
	if !ok {
		return nil, errorf("InvalidParameterValue", "The filter '%s' is invalid", name)
	}
	return v, nil
}

func anyMatch(patterns, values []string) bool {
	for _, p := range patterns {
		for _, v := range values {
			if glob(p, v) {
				return true
			}
		}
	}
	return false
}

// glob matches * (any run, including /) and ? (one character).
func glob(pattern, s string) bool {
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == s
	}
	p, str := []rune(pattern), []rune(s)
	// Iterative wildcard match with backtracking to the last star.
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(str) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == str[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func b2s(b bool) string { return strconv.FormatBool(b) }

func i2s(i int) string { return strconv.Itoa(i) }

// nonEmpty drops empty strings, so an unset field matches no filter value.
func nonEmpty(vs ...string) []string {
	var out []string
	for _, v := range vs {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
