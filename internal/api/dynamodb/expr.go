// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file is the one parser for every DynamoDB expression language:
// condition, filter and key-condition expressions share the condition
// grammar, update expressions add SET/REMOVE/ADD/DELETE clauses, and
// projection expressions are a list of document paths. Placeholders
// (#name, :value) are resolved while parsing.

// ── Lexer ────────────────────────────────────────────────────────────────

type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokName  // #placeholder
	tokValue // :placeholder
	tokInt
	tokPunct
)

type token struct {
	kind tokKind
	text string
	pos  int
}

func lex(src string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#' || c == ':':
			j := i + 1
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			if j == i+1 {
				return nil, fmt.Errorf("Syntax error; token: %q, near: %q", string(c), near(src, i))
			}
			k := tokName
			if c == ':' {
				k = tokValue
			}
			toks = append(toks, token{k, src[i:j], i})
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			toks = append(toks, token{tokInt, src[i:j], i})
			i = j
		case isIdentChar(c):
			j := i
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			toks = append(toks, token{tokIdent, src[i:j], i})
			i = j
		case c == '<' || c == '>':
			if i+1 < len(src) && (src[i+1] == '=' || (c == '<' && src[i+1] == '>')) {
				toks = append(toks, token{tokPunct, src[i : i+2], i})
				i += 2
			} else {
				toks = append(toks, token{tokPunct, string(c), i})
				i++
			}
		case strings.IndexByte("()[],.=+-", c) >= 0:
			toks = append(toks, token{tokPunct, string(c), i})
			i++
		default:
			return nil, fmt.Errorf("Invalid character encountered; character: %q", string(c))
		}
	}
	toks = append(toks, token{tokEOF, "", len(src)})
	return toks, nil
}

func isIdentChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func near(src string, pos int) string {
	end := pos + 10
	if end > len(src) {
		end = len(src)
	}
	return src[pos:end]
}

// ── AST ──────────────────────────────────────────────────────────────────

// Operand is a PathExpr, ValueExpr, FuncExpr or ArithExpr.
type Operand interface{ operand() }

type PathElem struct {
	Name    string
	Index   int
	IsIndex bool
}

type PathExpr struct{ Elems []PathElem }

type ValueExpr struct {
	Ref string
	V   *AttributeValue
}

type FuncExpr struct {
	Name string
	Args []Operand
}

type ArithExpr struct {
	Op   string
	L, R Operand
}

func (*PathExpr) operand()  {}
func (*ValueExpr) operand() {}
func (*FuncExpr) operand()  {}
func (*ArithExpr) operand() {}

func (p *PathExpr) String() string {
	var b strings.Builder
	for i, e := range p.Elems {
		if e.IsIndex {
			fmt.Fprintf(&b, "[%d]", e.Index)
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(e.Name)
	}
	return b.String()
}

// Condition is a CmpCond, BetweenCond, InCond, LogicCond, NotCond or FuncCond.
type Condition interface{ condition() }

type CmpCond struct {
	Op   string
	L, R Operand
}

type BetweenCond struct{ X, Lo, Hi Operand }

type InCond struct {
	X    Operand
	List []Operand
}

type LogicCond struct {
	Op   string // AND, OR
	L, R Condition
}

type NotCond struct{ C Condition }

type FuncCond struct {
	Name string
	Args []Operand
}

func (*CmpCond) condition()     {}
func (*BetweenCond) condition() {}
func (*InCond) condition()      {}
func (*LogicCond) condition()   {}
func (*NotCond) condition()     {}
func (*FuncCond) condition()    {}

type SetAction struct {
	Path  *PathExpr
	Value Operand
}

type AddAction struct {
	Path  *PathExpr
	Value *ValueExpr
}

type UpdateExpr struct {
	Set    []SetAction
	Remove []*PathExpr
	Add    []AddAction
	Delete []AddAction
}

// ── Parser ───────────────────────────────────────────────────────────────

// ExprContext holds one request's placeholder maps and records which of
// them the request's expressions used, so unused ones can be rejected the
// way DynamoDB does.
type ExprContext struct {
	Names      map[string]string
	Values     map[string]*AttributeValue
	usedNames  map[string]bool
	usedValues map[string]bool
}

func NewExprContext(names map[string]string, values map[string]*AttributeValue) *ExprContext {
	return &ExprContext{Names: names, Values: values, usedNames: map[string]bool{}, usedValues: map[string]bool{}}
}

// CheckUnused reports placeholders the request defined but never used.
func (c *ExprContext) CheckUnused() error {
	var names, values []string
	for k := range c.Names {
		if !c.usedNames[k] {
			names = append(names, k)
		}
	}
	for k := range c.Values {
		if !c.usedValues[k] {
			values = append(values, k)
		}
	}
	sort.Strings(names)
	sort.Strings(values)
	if len(names) > 0 {
		return validationErr("Value provided in ExpressionAttributeNames unused in expressions: keys: {" + strings.Join(names, ", ") + "}")
	}
	if len(values) > 0 {
		return validationErr("Value provided in ExpressionAttributeValues unused in expressions: keys: {" + strings.Join(values, ", ") + "}")
	}
	return nil
}

type parser struct {
	ctx  *ExprContext
	kind string // "ConditionExpression", "UpdateExpression", ... for messages
	src  string
	toks []token
	pos  int
}

type exprError struct{ msg string }

func (e *exprError) Error() string { return e.msg }

func (p *parser) fail(format string, args ...any) {
	panic(&exprError{fmt.Sprintf("Invalid %s: %s", p.kind, fmt.Sprintf(format, args...))})
}

func (p *parser) syntax() {
	t := p.peek()
	if t.kind == tokEOF {
		p.fail("Syntax error; token: \"<EOF>\", near: %q", near(p.src, max(0, len(p.src)-10)))
	}
	p.fail("Syntax error; token: %q, near: %q", t.text, near(p.src, t.pos))
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }

func (p *parser) isPunct(s string) bool {
	t := p.peek()
	return t.kind == tokPunct && t.text == s
}

func (p *parser) isKeyword(s string) bool {
	t := p.peek()
	return t.kind == tokIdent && strings.EqualFold(t.text, s)
}

func (p *parser) expectPunct(s string) {
	if !p.isPunct(s) {
		p.syntax()
	}
	p.pos++
}

func newParser(ctx *ExprContext, kind, src string) (*parser, error) {
	if strings.TrimSpace(src) == "" {
		return nil, validationErr(fmt.Sprintf("Invalid %s: The expression can not be empty;", kind))
	}
	toks, err := lex(src)
	if err != nil {
		return nil, validationErr(fmt.Sprintf("Invalid %s: %s", kind, err.Error()))
	}
	if ctx == nil {
		ctx = NewExprContext(nil, nil)
	}
	return &parser{ctx: ctx, kind: kind, src: src, toks: toks}, nil
}

// run calls fn and converts a parse panic into a ValidationException.
func (p *parser) run(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*exprError); ok {
				err = validationErr(e.msg)
				return
			}
			panic(r)
		}
	}()
	fn()
	if p.peek().kind != tokEOF {
		p.syntax()
	}
	return nil
}

// ParseCondition parses a condition, filter or key-condition expression.
func ParseCondition(ctx *ExprContext, kind, src string) (Condition, error) {
	p, err := newParser(ctx, kind, src)
	if err != nil {
		return nil, err
	}
	var c Condition
	err = p.run(func() { c = p.parseOr() })
	return c, err
}

// ParseUpdate parses an update expression.
func ParseUpdate(ctx *ExprContext, src string) (*UpdateExpr, error) {
	p, err := newParser(ctx, "UpdateExpression", src)
	if err != nil {
		return nil, err
	}
	var u *UpdateExpr
	err = p.run(func() { u = p.parseUpdate() })
	if err != nil {
		return nil, err
	}
	if err := u.checkOverlap(); err != nil {
		return nil, err
	}
	return u, nil
}

// ParseProjection parses a projection expression.
func ParseProjection(ctx *ExprContext, src string) ([]*PathExpr, error) {
	p, err := newParser(ctx, "ProjectionExpression", src)
	if err != nil {
		return nil, err
	}
	var paths []*PathExpr
	err = p.run(func() {
		for {
			paths = append(paths, p.parsePath())
			if !p.isPunct(",") {
				break
			}
			p.next()
		}
	})
	return paths, err
}

func (p *parser) parseOr() Condition {
	l := p.parseAnd()
	for p.isKeyword("OR") {
		p.next()
		l = &LogicCond{Op: "OR", L: l, R: p.parseAnd()}
	}
	return l
}

func (p *parser) parseAnd() Condition {
	l := p.parseNot()
	for p.isKeyword("AND") {
		p.next()
		l = &LogicCond{Op: "AND", L: l, R: p.parseNot()}
	}
	return l
}

func (p *parser) parseNot() Condition {
	if p.isKeyword("NOT") {
		p.next()
		return &NotCond{C: p.parseNot()}
	}
	return p.parsePredicate()
}

var condFuncs = map[string]int{
	"attribute_exists":     1,
	"attribute_not_exists": 1,
	"attribute_type":       2,
	"begins_with":          2,
	"contains":             2,
}

func (p *parser) parsePredicate() Condition {
	if p.isPunct("(") {
		p.next()
		c := p.parseOr()
		p.expectPunct(")")
		return c
	}
	t := p.peek()
	if t.kind == tokIdent && p.toks[p.pos+1].kind == tokPunct && p.toks[p.pos+1].text == "(" {
		if n, ok := condFuncs[t.text]; ok {
			p.pos += 2
			args := p.parseArgs()
			if len(args) != n {
				p.fail("Incorrect number of operands for operator or function; operator or function: %s, number of operands: %d", t.text, len(args))
			}
			if _, ok := args[0].(*PathExpr); !ok {
				p.fail("Operator or function requires a document path; operator or function: %s", t.text)
			}
			return &FuncCond{Name: t.text, Args: args}
		}
	}

	l := p.parseOperand(false)
	switch {
	case p.isKeyword("BETWEEN"):
		p.next()
		lo := p.parseOperand(false)
		if !p.isKeyword("AND") {
			p.syntax()
		}
		p.next()
		return &BetweenCond{X: l, Lo: lo, Hi: p.parseOperand(false)}
	case p.isKeyword("IN"):
		p.next()
		p.expectPunct("(")
		list := p.parseArgsAfterParen(false)
		if len(list) > 100 {
			p.fail("The IN operator is provided with too many operands; number of operands: %d", len(list))
		}
		return &InCond{X: l, List: list}
	}
	t = p.peek()
	if t.kind == tokPunct {
		switch t.text {
		case "=", "<>", "<", "<=", ">", ">=":
			p.next()
			return &CmpCond{Op: t.text, L: l, R: p.parseOperand(false)}
		}
	}
	if f, ok := l.(*FuncExpr); ok {
		p.fail("The function is not allowed to be used this way in an expression; function: %s", f.Name)
	}
	p.syntax()
	return nil
}

func (p *parser) parseArgs() []Operand { return p.parseArgsAfterParen(false) }

// parseArgsAfterParen reads "a, b, c)" after the opening parenthesis.
func (p *parser) parseArgsAfterParen(update bool) []Operand {
	var args []Operand
	if p.isPunct(")") {
		p.syntax()
	}
	for {
		args = append(args, p.parseOperand(update))
		if p.isPunct(",") {
			p.next()
			continue
		}
		p.expectPunct(")")
		return args
	}
}

var operandFuncs = map[string]struct {
	n      int
	update bool
}{
	"size":          {1, false},
	"if_not_exists": {2, true},
	"list_append":   {2, true},
}

// parseOperand reads a path, a value placeholder or a function call. update
// allows the functions only valid on the right of SET.
func (p *parser) parseOperand(update bool) Operand {
	t := p.peek()
	switch t.kind {
	case tokValue:
		p.next()
		v, ok := p.ctx.Values[t.text]
		if !ok {
			p.fail("An expression attribute value used in expression is not defined; attribute value: %s", t.text)
		}
		p.ctx.usedValues[t.text] = true
		return &ValueExpr{Ref: t.text, V: v}
	case tokIdent:
		if p.toks[p.pos+1].kind == tokPunct && p.toks[p.pos+1].text == "(" {
			p.pos += 2
			f, ok := operandFuncs[t.text]
			if !ok {
				if _, isCond := condFuncs[t.text]; isCond {
					p.fail("The function is not allowed to be used this way in an expression; function: %s", t.text)
				}
				p.fail("Invalid function name; function: %s", t.text)
			}
			if f.update != update {
				p.fail("The function is not allowed in a %s; function: %s", strings.TrimSuffix(strings.ToLower(p.kind), "expression")+" expression", t.text)
			}
			args := p.parseArgsAfterParen(update)
			if len(args) != f.n {
				p.fail("Incorrect number of operands for operator or function; operator or function: %s, number of operands: %d", t.text, len(args))
			}
			if t.text != "list_append" {
				if _, ok := args[0].(*PathExpr); !ok {
					p.fail("Operator or function requires a document path; operator or function: %s", t.text)
				}
			}
			return &FuncExpr{Name: t.text, Args: args}
		}
		return p.parsePath()
	case tokName:
		return p.parsePath()
	}
	p.syntax()
	return nil
}

func (p *parser) parseName() string {
	t := p.next()
	switch t.kind {
	case tokIdent:
		if reservedWords[strings.ToUpper(t.text)] {
			p.fail("Attribute name is a reserved keyword; reserved keyword: %s", t.text)
		}
		return t.text
	case tokName:
		n, ok := p.ctx.Names[t.text]
		if !ok {
			p.fail("An expression attribute name used in the document path is not defined; attribute name: %s", t.text)
		}
		p.ctx.usedNames[t.text] = true
		return n
	}
	p.pos--
	p.syntax()
	return ""
}

func (p *parser) parsePath() *PathExpr {
	path := &PathExpr{Elems: []PathElem{{Name: p.parseName()}}}
	for {
		switch {
		case p.isPunct("."):
			p.next()
			path.Elems = append(path.Elems, PathElem{Name: p.parseName()})
		case p.isPunct("["):
			p.next()
			t := p.next()
			if t.kind != tokInt {
				p.pos--
				p.syntax()
			}
			n, err := strconv.Atoi(t.text)
			if err != nil {
				p.syntax()
			}
			p.expectPunct("]")
			path.Elems = append(path.Elems, PathElem{Index: n, IsIndex: true})
		default:
			return path
		}
	}
}

func (p *parser) parseUpdate() *UpdateExpr {
	u := &UpdateExpr{}
	seen := map[string]bool{}
	for p.peek().kind != tokEOF {
		t := p.next()
		clause := strings.ToUpper(t.text)
		if t.kind != tokIdent {
			p.pos--
			p.syntax()
		}
		if seen[clause] {
			p.fail("The \"%s\" section can only be used once in an update expression;", clause)
		}
		seen[clause] = true
		switch clause {
		case "SET":
			for {
				path := p.parsePath()
				p.expectPunct("=")
				u.Set = append(u.Set, SetAction{Path: path, Value: p.parseSetValue()})
				if !p.isPunct(",") {
					break
				}
				p.next()
			}
		case "REMOVE":
			for {
				u.Remove = append(u.Remove, p.parsePath())
				if !p.isPunct(",") {
					break
				}
				p.next()
			}
		case "ADD", "DELETE":
			for {
				path := p.parsePath()
				v, ok := p.parseOperand(false).(*ValueExpr)
				if !ok {
					p.fail("Syntax error; %s requires a value placeholder", clause)
				}
				if clause == "ADD" {
					u.Add = append(u.Add, AddAction{Path: path, Value: v})
				} else {
					u.Delete = append(u.Delete, AddAction{Path: path, Value: v})
				}
				if !p.isPunct(",") {
					break
				}
				p.next()
			}
		default:
			p.pos--
			p.syntax()
		}
	}
	return u
}

// parseSetValue reads "operand [+|- operand]".
func (p *parser) parseSetValue() Operand {
	l := p.parseOperand(true)
	if p.isPunct("+") || p.isPunct("-") {
		op := p.next().text
		return &ArithExpr{Op: op, L: l, R: p.parseOperand(true)}
	}
	return l
}

// checkOverlap rejects update expressions whose actions touch the same or
// nested document paths, as DynamoDB does.
func (u *UpdateExpr) checkOverlap() error {
	var paths []*PathExpr
	for _, a := range u.Set {
		paths = append(paths, a.Path)
	}
	paths = append(paths, u.Remove...)
	for _, a := range u.Add {
		paths = append(paths, a.Path)
	}
	for _, a := range u.Delete {
		paths = append(paths, a.Path)
	}
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			if pathsOverlap(paths[i], paths[j]) {
				return validationErr(fmt.Sprintf("Invalid UpdateExpression: Two document paths overlap with each other; must remove or rewrite one of these paths; path one: [%s], path two: [%s]", paths[i], paths[j]))
			}
		}
	}
	return nil
}

func pathsOverlap(a, b *PathExpr) bool {
	n := min(len(a.Elems), len(b.Elems))
	for i := 0; i < n; i++ {
		if a.Elems[i] != b.Elems[i] {
			return false
		}
	}
	return true
}

// topLevel returns the attribute name a path starts at.
func (p *PathExpr) topLevel() string { return p.Elems[0].Name }

// reservedWords is the subset of DynamoDB's reserved words most likely to
// appear as bare attribute names. The full list has ~570 entries; these are
// the ones real configurations trip over.
var reservedWords = map[string]bool{
	"ABORT": true, "ACTION": true, "ADD": true, "ALL": true, "AND": true, "BETWEEN": true,
	"BY": true, "COUNT": true, "DATA": true, "DATE": true, "DELETE": true, "DESC": true,
	"EXISTS": true, "FROM": true, "GROUP": true, "IN": true, "INDEX": true, "KEY": true,
	"KEYS": true, "LIMIT": true, "NAME": true, "NOT": true, "NULL": true, "OR": true,
	"ORDER": true, "REMOVE": true, "SET": true, "SIZE": true, "STATUS": true, "TABLE": true,
	"TIMESTAMP": true, "TTL": true, "TYPE": true, "UPDATE": true, "USER": true, "VALUE": true,
	"VALUES": true, "WHERE": true, "YEAR": true, "COMMENT": true, "TIME": true, "ZONE": true,
}
