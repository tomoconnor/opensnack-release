// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// params reads EC2 Query parameters, where lists are flattened as
// Name.1, Name.2 and structures as Name.1.Field.
type params url.Values

func (p params) get(k string) string {
	if v := p[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (p params) has(k string) bool {
	_, ok := p[k]
	return ok
}

func (p params) boolean(k string) bool {
	return strings.EqualFold(p.get(k), "true")
}

// optBool returns nil when k is absent.
func (p params) optBool(k string) *bool {
	if !p.has(k) {
		return nil
	}
	b := p.boolean(k)
	return &b
}

func (p params) integer(k string, def int) int {
	if n, err := strconv.Atoi(p.get(k)); err == nil {
		return n
	}
	return def
}

func (p params) optInt(k string) *int {
	n, err := strconv.Atoi(p.get(k))
	if err != nil {
		return nil
	}
	return &n
}

// list returns the values of prefix.1, prefix.2, ... (indexes need not be
// contiguous).
func (p params) list(prefix string) []string {
	idx := map[int]string{}
	for k, v := range p {
		rest, ok := strings.CutPrefix(k, prefix+".")
		if !ok || len(v) == 0 {
			continue
		}
		if n, err := strconv.Atoi(rest); err == nil {
			idx[n] = v[0]
		}
	}
	return ordered(idx)
}

// sub returns the structures prefix.N.*, each with the prefix.N. stripped.
func (p params) sub(prefix string) []params {
	idx := map[int]params{}
	for k, v := range p {
		rest, ok := strings.CutPrefix(k, prefix+".")
		if !ok {
			continue
		}
		num, field, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		if idx[n] == nil {
			idx[n] = params{}
		}
		idx[n][field] = v
	}
	return ordered(idx)
}

func ordered[T any](m map[int]T) []T {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := make([]T, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// tags reads prefix.N.Key / prefix.N.Value.
func (p params) tags(prefix string) []Tag {
	var out []Tag
	for _, t := range p.sub(prefix) {
		out = append(out, Tag{Key: t.get("Key"), Value: t.get("Value")})
	}
	return out
}

// tagSpecs returns the tags in TagSpecification.N for resourceType.
func (p params) tagSpecs(resourceType string) []Tag {
	var out []Tag
	for _, ts := range p.sub("TagSpecification") {
		if ts.get("ResourceType") == resourceType {
			out = append(out, ts.tags("Tag")...)
		}
	}
	return out
}

type filter struct {
	name   string
	values []string
}

func (p params) filters() []filter {
	var out []filter
	for _, f := range p.sub("Filter") {
		out = append(out, filter{name: f.get("Name"), values: f.list("Value")})
	}
	return out
}
