// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudwatch

import (
	"fmt"
	"strings"
)

// Composite alarm rules:
//
//	ALARM("cpu-high") AND NOT OK(disk) OR (TRUE AND INSUFFICIENT_DATA(arn:...:alarm:x))
//
// AND binds tighter than OR. Alarms may be named or given by ARN.

type rule interface {
	eval(state func(name string) string) bool
	collect(names *[]string)
}

type ruleState struct{ want, alarm string }
type ruleConst bool
type ruleNot struct{ r rule }
type ruleAnd struct{ l, r rule }
type ruleOr struct{ l, r rule }

func (r ruleState) eval(state func(string) string) bool { return state(r.alarm) == r.want }
func (r ruleConst) eval(func(string) string) bool       { return bool(r) }
func (r ruleNot) eval(s func(string) string) bool       { return !r.r.eval(s) }
func (r ruleAnd) eval(s func(string) string) bool       { return r.l.eval(s) && r.r.eval(s) }
func (r ruleOr) eval(s func(string) string) bool        { return r.l.eval(s) || r.r.eval(s) }

func (r ruleState) collect(n *[]string) { *n = append(*n, r.alarm) }
func (r ruleConst) collect(*[]string)   {}
func (r ruleNot) collect(n *[]string)   { r.r.collect(n) }
func (r ruleAnd) collect(n *[]string)   { r.l.collect(n); r.r.collect(n) }
func (r ruleOr) collect(n *[]string)    { r.l.collect(n); r.r.collect(n) }

type parsedRule struct{ rule }

// names lists the alarms a rule refers to, by name.
func (p parsedRule) names() []string {
	var n []string
	p.collect(&n)
	return n
}

func parseRule(s string) (parsedRule, error) {
	p := &ruleParser{s: s}
	r, err := p.or()
	if err != nil {
		return parsedRule{}, err
	}
	p.skip()
	if p.i != len(p.s) {
		return parsedRule{}, fmt.Errorf("unexpected %q", p.s[p.i:])
	}
	return parsedRule{r}, nil
}

type ruleParser struct {
	s string
	i int
}

func (p *ruleParser) skip() {
	for p.i < len(p.s) && strings.ContainsRune(" \t\n\r", rune(p.s[p.i])) {
		p.i++
	}
}

func (p *ruleParser) keyword(kw string) bool {
	p.skip()
	if strings.HasPrefix(strings.ToUpper(p.s[p.i:]), kw) {
		end := p.i + len(kw)
		if end == len(p.s) || !isWordChar(p.s[end]) {
			p.i = end
			return true
		}
	}
	return false
}

func isWordChar(c byte) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

func (p *ruleParser) or() (rule, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.keyword("OR") {
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = ruleOr{l, r}
	}
	return l, nil
}

func (p *ruleParser) and() (rule, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.keyword("AND") {
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = ruleAnd{l, r}
	}
	return l, nil
}

func (p *ruleParser) unary() (rule, error) {
	if p.keyword("NOT") {
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		return ruleNot{r}, nil
	}
	p.skip()
	if p.i < len(p.s) && p.s[p.i] == '(' {
		p.i++
		r, err := p.or()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.i >= len(p.s) || p.s[p.i] != ')' {
			return nil, fmt.Errorf("missing )")
		}
		p.i++
		return r, nil
	}
	if p.keyword("TRUE") {
		return ruleConst(true), nil
	}
	if p.keyword("FALSE") {
		return ruleConst(false), nil
	}
	for _, st := range []string{stateInsufficient, stateAlarm, stateOK} {
		if p.keyword(st) {
			p.skip()
			if p.i >= len(p.s) || p.s[p.i] != '(' {
				return nil, fmt.Errorf("expected ( after %s", st)
			}
			p.i++
			p.skip()
			var name string
			if p.i < len(p.s) && p.s[p.i] == '"' {
				end := strings.IndexByte(p.s[p.i+1:], '"')
				if end < 0 {
					return nil, fmt.Errorf("unterminated quote")
				}
				name = p.s[p.i+1 : p.i+1+end]
				p.i += end + 2
			} else {
				end := strings.IndexByte(p.s[p.i:], ')')
				if end < 0 {
					return nil, fmt.Errorf("missing )")
				}
				name = strings.TrimSpace(p.s[p.i : p.i+end])
				p.i += end
			}
			p.skip()
			if p.i >= len(p.s) || p.s[p.i] != ')' {
				return nil, fmt.Errorf("missing )")
			}
			p.i++
			if j := strings.Index(name, ":alarm:"); j >= 0 {
				name = name[j+len(":alarm:"):]
			}
			if name == "" {
				return nil, fmt.Errorf("empty alarm name")
			}
			return ruleState{st, name}, nil
		}
	}
	return nil, fmt.Errorf("unexpected %q", p.s[p.i:])
}

func (e *Engine) evaluateCompositeLocked(scope string, a *alarm) {
	r, err := parseRule(str(a.Attrs, "AlarmRule"))
	if err != nil {
		return
	}
	states := map[string]string{}
	for _, other := range listAlarms(e.store, scope) {
		states[other.name()] = other.state()
	}
	state, reason := stateOK, "All the rule's conditions are false"
	if r.eval(func(n string) string { return states[n] }) {
		state, reason = stateAlarm, "The alarm rule evaluated to true"
	}
	_, res := getAlarm(e.store, scope, a.name())
	if res == nil {
		return
	}
	e.transitionLocked(scope, a, res, state, reason, "")
}
