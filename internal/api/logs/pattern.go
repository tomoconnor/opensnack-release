// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Pattern is a compiled CloudWatch Logs filter pattern. Three syntaxes are
// supported, picked by the first character as AWS does:
//
//	ERROR "disk full" -retry ?WARN %code [0-9]+%   terms (AND, quoted phrases,
//	                                                -exclusion, ?OR, %regex%)
//	{ $.level = "error" && $.latency > 300 }       JSON selectors
//	[ip, user, ..., status = 5*, bytes > 1000]     space-delimited fields
//
// Term matching is case-sensitive substring matching. An empty pattern
// matches every event.
type Pattern struct {
	raw string

	// term patterns
	all, any, none []termMatcher

	// JSON patterns
	expr node

	// space-delimited patterns
	fields []fieldSpec
}

type termMatcher struct {
	text string
	re   *regexp.Regexp
}

func (t termMatcher) match(msg string) bool {
	if t.re != nil {
		return t.re.MatchString(msg)
	}
	return strings.Contains(msg, t.text)
}

type fieldSpec struct {
	ellipsis bool
	name     string
	cond     node
}

// Match reports whether the message matches, and gives back what the
// pattern extracted (for metric filter values and dimensions).
func (p *Pattern) Match(msg string) (bool, Extracted) {
	switch {
	case p.expr != nil:
		var doc any
		if err := json.Unmarshal([]byte(strings.TrimSpace(msg)), &doc); err != nil {
			return false, Extracted{}
		}
		ex := Extracted{json: doc}
		return p.expr.eval(&ex), ex
	case p.fields != nil:
		ex := Extracted{named: map[string]string{}}
		return matchFields(p.fields, splitFields(msg), &ex), ex
	}
	for _, t := range p.none {
		if t.match(msg) {
			return false, Extracted{}
		}
	}
	for _, t := range p.all {
		if !t.match(msg) {
			return false, Extracted{}
		}
	}
	if len(p.any) > 0 {
		for _, t := range p.any {
			if t.match(msg) {
				return true, Extracted{}
			}
		}
		return false, Extracted{}
	}
	return true, Extracted{}
}

// Extracted holds the parsed event a pattern matched against.
type Extracted struct {
	json  any
	named map[string]string
}

// Lookup resolves "$.a.b[0]" against a JSON event or "$name" against a
// space-delimited one.
func (e Extracted) Lookup(sel string) (any, bool) {
	s, err := parseSelector(sel)
	if err != nil {
		return nil, false
	}
	return e.resolve(s)
}

func (e *Extracted) resolve(s selector) (any, bool) {
	if s.name != "" {
		v, ok := e.named[s.name]
		return v, ok
	}
	cur := e.json
	for _, p := range s.path {
		switch {
		case p.idx >= 0:
			arr, ok := cur.([]any)
			if !ok || p.idx >= len(arr) {
				return nil, false
			}
			cur = arr[p.idx]
		default:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := m[p.key]
			if !ok {
				return nil, false
			}
			cur = v
		}
	}
	return cur, true
}

// ParsePattern compiles a filter pattern, returning an error AWS would
// report as InvalidParameterException.
func ParsePattern(s string) (*Pattern, error) {
	p := &Pattern{raw: s}
	t := strings.TrimSpace(s)
	switch {
	case t == "":
		return p, nil
	case strings.HasPrefix(t, "{"):
		if !strings.HasSuffix(t, "}") {
			return nil, fmt.Errorf("invalid JSON filter pattern: missing closing brace")
		}
		n, err := parseExpr(t[1:len(t)-1], true)
		if err != nil {
			return nil, err
		}
		p.expr = n
		return p, nil
	case strings.HasPrefix(t, "["):
		if !strings.HasSuffix(t, "]") {
			return nil, fmt.Errorf("invalid space-delimited filter pattern: missing closing bracket")
		}
		specs, err := parseFieldSpecs(t[1 : len(t)-1])
		if err != nil {
			return nil, err
		}
		if specs == nil {
			specs = []fieldSpec{}
		}
		p.fields = specs
		return p, nil
	}
	return p, p.parseTerms(t)
}

func (p *Pattern) parseTerms(s string) error {
	i := 0
	for i < len(s) {
		if s[i] == ' ' || s[i] == '\t' {
			i++
			continue
		}
		prefix := byte(0)
		if s[i] == '?' || s[i] == '-' {
			prefix = s[i]
			i++
		}
		var m termMatcher
		switch {
		case i < len(s) && s[i] == '"':
			j := i + 1
			var b strings.Builder
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return fmt.Errorf("invalid filter pattern: unterminated quote")
			}
			m.text = b.String()
			i = j + 1
		case i < len(s) && s[i] == '%':
			j := strings.IndexByte(s[i+1:], '%')
			if j < 0 {
				return fmt.Errorf("invalid filter pattern: unterminated regular expression")
			}
			re, err := regexp.Compile(s[i+1 : i+1+j])
			if err != nil {
				return fmt.Errorf("invalid filter pattern regular expression: %v", err)
			}
			m.re = re
			i = i + j + 2
		default:
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' {
				j++
			}
			m.text = s[i:j]
			i = j
		}
		if m.re == nil && m.text == "" {
			if prefix == '-' {
				return fmt.Errorf("invalid filter pattern: empty exclusion")
			}
			continue
		}
		switch prefix {
		case '?':
			p.any = append(p.any, m)
		case '-':
			p.none = append(p.none, m)
		default:
			p.all = append(p.all, m)
		}
	}
	return nil
}

//
// Space-delimited events
//

// splitFields breaks a message on whitespace, keeping "quoted" and
// [bracketed] runs together as one field without their delimiters.
func splitFields(msg string) []string {
	var out []string
	i := 0
	for i < len(msg) {
		c := msg[i]
		if c == ' ' || c == '\t' {
			i++
			continue
		}
		switch c {
		case '"', '[':
			end := byte('"')
			if c == '[' {
				end = ']'
			}
			j := strings.IndexByte(msg[i+1:], end)
			if j >= 0 {
				out = append(out, msg[i+1:i+1+j])
				i = i + j + 2
				continue
			}
		}
		j := i
		for j < len(msg) && msg[j] != ' ' && msg[j] != '\t' {
			j++
		}
		out = append(out, msg[i:j])
		i = j
	}
	return out
}

func parseFieldSpecs(s string) ([]fieldSpec, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var specs []fieldSpec
	for _, part := range splitTopLevel(s, ',') {
		part = strings.TrimSpace(part)
		if part == "..." {
			specs = append(specs, fieldSpec{ellipsis: true})
			continue
		}
		if part == "" {
			return nil, fmt.Errorf("invalid space-delimited filter pattern: empty field")
		}
		name := part
		if i := strings.IndexAny(part, "=!<> \t("); i >= 0 {
			name = strings.TrimSpace(part[:i])
		}
		if !isIdent(name) {
			return nil, fmt.Errorf("invalid space-delimited filter pattern field %q", part)
		}
		spec := fieldSpec{name: name}
		if rest := strings.TrimSpace(part[len(name):]); rest != "" {
			n, err := parseExpr(part, false)
			if err != nil {
				return nil, err
			}
			spec.cond = n
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// splitTopLevel splits on sep outside quotes.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	start, inQuote := 0, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case sep:
			if !inQuote {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func matchFields(specs []fieldSpec, fields []string, ex *Extracted) bool {
	if len(specs) == 0 {
		return len(fields) == 0
	}
	sp := specs[0]
	if sp.ellipsis {
		for k := 0; k <= len(fields); k++ {
			if matchFields(specs[1:], fields[k:], ex) {
				return true
			}
		}
		return false
	}
	if len(fields) == 0 {
		return false
	}
	prev, had := ex.named[sp.name]
	ex.named[sp.name] = fields[0]
	if (sp.cond == nil || sp.cond.eval(ex)) && matchFields(specs[1:], fields[1:], ex) {
		return true
	}
	if had {
		ex.named[sp.name] = prev
	} else {
		delete(ex.named, sp.name)
	}
	return false
}

//
// Expressions shared by JSON and space-delimited patterns
//

type node interface {
	eval(ex *Extracted) bool
}

type andNode struct{ l, r node }
type orNode struct{ l, r node }

func (n andNode) eval(ex *Extracted) bool { return n.l.eval(ex) && n.r.eval(ex) }
func (n orNode) eval(ex *Extracted) bool  { return n.l.eval(ex) || n.r.eval(ex) }

type pathElem struct {
	key string
	idx int
}

type selector struct {
	name string // space-delimited field
	path []pathElem
}

type cmpNode struct {
	sel selector
	op  string // = != < > <= >= IS NOT-EXISTS EXISTS
	val literal
}

type literal struct {
	text  string
	num   float64
	isNum bool
	re    *regexp.Regexp // glob (*) or %regex%
	kw    string         // NULL TRUE FALSE for IS
}

func (n cmpNode) eval(ex *Extracted) bool {
	v, ok := ex.resolve(n.sel)
	switch n.op {
	case "NOT EXISTS":
		return !ok
	case "EXISTS":
		return ok
	case "IS":
		if !ok {
			return false
		}
		switch n.val.kw {
		case "NULL":
			return v == nil
		case "TRUE":
			return v == true
		case "FALSE":
			return v == false
		}
		return false
	}
	if !ok || v == nil {
		return false
	}
	var fieldStr string
	var fieldNum float64
	fieldIsNum := false
	switch x := v.(type) {
	case float64:
		fieldNum, fieldIsNum = x, true
		fieldStr = strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		fieldStr = x
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			fieldNum, fieldIsNum = f, true
		}
	case bool:
		fieldStr = strconv.FormatBool(x)
	default:
		return false
	}
	switch n.op {
	case "=", "!=":
		var eq bool
		switch {
		case n.val.re != nil:
			eq = n.val.re.MatchString(fieldStr)
		case n.val.isNum && fieldIsNum:
			eq = fieldNum == n.val.num
		default:
			eq = fieldStr == n.val.text
		}
		if n.op == "=" {
			return eq
		}
		return !eq
	}
	if !n.val.isNum || !fieldIsNum {
		return false
	}
	switch n.op {
	case "<":
		return fieldNum < n.val.num
	case ">":
		return fieldNum > n.val.num
	case "<=":
		return fieldNum <= n.val.num
	case ">=":
		return fieldNum >= n.val.num
	}
	return false
}

type exprParser struct {
	toks []string
	pos  int
	json bool
}

func parseExpr(s string, jsonSel bool) (node, error) {
	toks, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks, json: jsonSel}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("invalid filter pattern: unexpected %q", p.toks[p.pos])
	}
	return n, nil
}

func (p *exprParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *exprParser) next() string {
	t := p.peek()
	p.pos++
	return t
}

func (p *exprParser) or() (node, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.peek() == "||" {
		p.next()
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = orNode{l, r}
	}
	return l, nil
}

func (p *exprParser) and() (node, error) {
	l, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.peek() == "&&" {
		p.next()
		r, err := p.primary()
		if err != nil {
			return nil, err
		}
		l = andNode{l, r}
	}
	return l, nil
}

func (p *exprParser) primary() (node, error) {
	if p.peek() == "(" {
		p.next()
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.next() != ")" {
			return nil, fmt.Errorf("invalid filter pattern: missing )")
		}
		return n, nil
	}
	selTok := p.next()
	if selTok == "" {
		return nil, fmt.Errorf("invalid filter pattern: expected a selector")
	}
	var sel selector
	if p.json {
		s, err := parseSelector(selTok)
		if err != nil || s.name != "" {
			return nil, fmt.Errorf("invalid filter pattern selector %q", selTok)
		}
		sel = s
	} else {
		if !isIdent(selTok) {
			return nil, fmt.Errorf("invalid filter pattern field %q", selTok)
		}
		sel = selector{name: selTok}
	}
	op := p.next()
	switch strings.ToUpper(op) {
	case "IS":
		kw := strings.ToUpper(p.next())
		if kw != "NULL" && kw != "TRUE" && kw != "FALSE" {
			return nil, fmt.Errorf("invalid filter pattern: IS must be followed by NULL, TRUE or FALSE")
		}
		return cmpNode{sel: sel, op: "IS", val: literal{kw: kw}}, nil
	case "NOT":
		if strings.ToUpper(p.next()) != "EXISTS" {
			return nil, fmt.Errorf("invalid filter pattern: expected NOT EXISTS")
		}
		return cmpNode{sel: sel, op: "NOT EXISTS"}, nil
	case "EXISTS":
		return cmpNode{sel: sel, op: "EXISTS"}, nil
	case "=", "!=", "<", ">", "<=", ">=":
	default:
		return nil, fmt.Errorf("invalid filter pattern: expected an operator after %s", selTok)
	}
	lit, err := parseLiteral(p.next())
	if err != nil {
		return nil, err
	}
	if (op != "=" && op != "!=") && !lit.isNum {
		return nil, fmt.Errorf("invalid filter pattern: %s needs a numeric value", op)
	}
	return cmpNode{sel: sel, op: op, val: lit}, nil
}

func parseLiteral(t string) (literal, error) {
	if t == "" || t == ")" || t == "&&" || t == "||" {
		return literal{}, fmt.Errorf("invalid filter pattern: expected a value")
	}
	if strings.HasPrefix(t, "%") && strings.HasSuffix(t, "%") && len(t) >= 2 {
		re, err := regexp.Compile(t[1 : len(t)-1])
		if err != nil {
			return literal{}, fmt.Errorf("invalid filter pattern regular expression: %v", err)
		}
		return literal{text: t, re: re}, nil
	}
	quoted := strings.HasPrefix(t, `"`)
	text := t
	if quoted {
		text = unquote(t)
	}
	lit := literal{text: text}
	if !quoted {
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			lit.num, lit.isNum = f, true
		}
	}
	if strings.Contains(text, "*") {
		parts := strings.Split(text, "*")
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		lit.re = regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
	}
	return lit, nil
}

func unquote(t string) string {
	t = t[1:]
	if strings.HasSuffix(t, `"`) {
		t = t[:len(t)-1]
	}
	return strings.ReplaceAll(t, `\"`, `"`)
}

func tokenize(s string) ([]string, error) {
	var toks []string
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '(' || c == ')':
			toks = append(toks, string(c))
			i++
		case strings.HasPrefix(s[i:], "&&") || strings.HasPrefix(s[i:], "||"):
			toks = append(toks, s[i:i+2])
			i += 2
		case strings.HasPrefix(s[i:], "!=") || strings.HasPrefix(s[i:], "<=") || strings.HasPrefix(s[i:], ">="):
			toks = append(toks, s[i:i+2])
			i += 2
		case c == '=' || c == '<' || c == '>':
			toks = append(toks, string(c))
			i++
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("invalid filter pattern: unterminated quote")
			}
			toks = append(toks, s[i:j+1])
			i = j + 1
		case c == '%':
			j := strings.IndexByte(s[i+1:], '%')
			if j < 0 {
				return nil, fmt.Errorf("invalid filter pattern: unterminated regular expression")
			}
			toks = append(toks, s[i:i+j+2])
			i = i + j + 2
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\n()=!<>&|\"", rune(s[j])) {
				j++
			}
			if j == i {
				return nil, fmt.Errorf("invalid filter pattern: unexpected %q", string(c))
			}
			toks = append(toks, s[i:j])
			i = j
		}
	}
	return toks, nil
}

// parseSelector reads "$.a.b[0]" (JSON) or "$name" (a space-delimited field).
func parseSelector(s string) (selector, error) {
	if !strings.HasPrefix(s, "$") {
		return selector{}, fmt.Errorf("selector must start with $")
	}
	rest := s[1:]
	if rest != "" && rest[0] != '.' && rest[0] != '[' {
		if !isIdent(rest) {
			return selector{}, fmt.Errorf("bad selector %q", s)
		}
		return selector{name: rest}, nil
	}
	sel := selector{path: []pathElem{}}
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			j := 0
			for j < len(rest) && rest[j] != '.' && rest[j] != '[' {
				j++
			}
			if j == 0 {
				return selector{}, fmt.Errorf("bad selector %q", s)
			}
			sel.path = append(sel.path, pathElem{key: rest[:j], idx: -1})
			rest = rest[j:]
		case '[':
			j := strings.IndexByte(rest, ']')
			if j < 0 {
				return selector{}, fmt.Errorf("bad selector %q", s)
			}
			n, err := strconv.Atoi(rest[1:j])
			if err != nil || n < 0 {
				return selector{}, fmt.Errorf("bad selector index in %q", s)
			}
			sel.path = append(sel.path, pathElem{idx: n})
			rest = rest[j+1:]
		default:
			return selector{}, fmt.Errorf("bad selector %q", s)
		}
	}
	return sel, nil
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}
