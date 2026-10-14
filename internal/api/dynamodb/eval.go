// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// getPath resolves a document path against an item. It returns nil when any
// step is missing or of the wrong type.
func getPath(it Item, p *PathExpr) *AttributeValue {
	cur := it[p.Elems[0].Name]
	for _, e := range p.Elems[1:] {
		if cur == nil {
			return nil
		}
		if e.IsIndex {
			if cur.Type != "L" || e.Index >= len(cur.L) {
				return nil
			}
			cur = cur.L[e.Index]
		} else {
			if cur.Type != "M" {
				return nil
			}
			cur = cur.M[e.Name]
		}
	}
	return cur
}

func invalidPathErr() error {
	return validationErr("The document path provided in the update expression is invalid for update")
}

// parentOf walks to the container that holds the path's last element.
func parentOf(it Item, p *PathExpr) (*AttributeValue, error) {
	if len(p.Elems) == 1 {
		return nil, nil
	}
	parent := getPath(it, &PathExpr{Elems: p.Elems[:len(p.Elems)-1]})
	if parent == nil {
		return nil, invalidPathErr()
	}
	last := p.Elems[len(p.Elems)-1]
	if last.IsIndex && parent.Type != "L" || !last.IsIndex && parent.Type != "M" {
		return nil, invalidPathErr()
	}
	return parent, nil
}

func setPath(it Item, p *PathExpr, v *AttributeValue) error {
	if p.Elems[0].IsIndex {
		return invalidPathErr()
	}
	parent, err := parentOf(it, p)
	if err != nil {
		return err
	}
	last := p.Elems[len(p.Elems)-1]
	switch {
	case parent == nil:
		it[last.Name] = v
	case last.IsIndex:
		if last.Index >= len(parent.L) {
			parent.L = append(parent.L, v)
		} else {
			parent.L[last.Index] = v
		}
	default:
		if parent.M == nil {
			parent.M = map[string]*AttributeValue{}
		}
		parent.M[last.Name] = v
	}
	return nil
}

func removePath(it Item, p *PathExpr) {
	if len(p.Elems) == 1 {
		delete(it, p.Elems[0].Name)
		return
	}
	parent, err := parentOf(it, p)
	if err != nil || parent == nil {
		return
	}
	last := p.Elems[len(p.Elems)-1]
	if last.IsIndex {
		if last.Index < len(parent.L) {
			parent.L = append(parent.L[:last.Index], parent.L[last.Index+1:]...)
		}
		return
	}
	delete(parent.M, last.Name)
}

// evalOperand computes an operand's value. A nil result means the operand
// refers to something absent. Errors are only for update-time misuse.
func evalOperand(op Operand, it Item) (*AttributeValue, error) {
	switch o := op.(type) {
	case *PathExpr:
		return getPath(it, o), nil
	case *ValueExpr:
		return o.V, nil
	case *FuncExpr:
		switch o.Name {
		case "size":
			v := getPath(it, o.Args[0].(*PathExpr))
			if v == nil {
				return nil, nil
			}
			n, ok := sizeOf(v)
			if !ok {
				return nil, nil
			}
			return numAV(fmt.Sprint(n)), nil
		case "if_not_exists":
			if v := getPath(it, o.Args[0].(*PathExpr)); v != nil {
				return v, nil
			}
			return evalOperand(o.Args[1], it)
		case "list_append":
			a, err := evalOperand(o.Args[0], it)
			if err != nil {
				return nil, err
			}
			b, err := evalOperand(o.Args[1], it)
			if err != nil {
				return nil, err
			}
			if a == nil || b == nil {
				return nil, missingOperandErr()
			}
			for _, x := range []*AttributeValue{a, b} {
				if x.Type != "L" {
					return nil, operandTypeErr("list_append", x.Type)
				}
			}
			out := listAV()
			for _, x := range a.L {
				out.L = append(out.L, x.clone())
			}
			for _, x := range b.L {
				out.L = append(out.L, x.clone())
			}
			return out, nil
		}
	case *ArithExpr:
		a, err := evalOperand(o.L, it)
		if err != nil {
			return nil, err
		}
		b, err := evalOperand(o.R, it)
		if err != nil {
			return nil, err
		}
		if a == nil || b == nil {
			return nil, missingOperandErr()
		}
		for _, x := range []*AttributeValue{a, b} {
			if x.Type != "N" {
				return nil, operandTypeErr(o.Op, x.Type)
			}
		}
		x, _ := parseNumber(a.S)
		y, _ := parseNumber(b.S)
		if o.Op == "+" {
			return numAV(formatNumber(new(big.Rat).Add(x, y))), nil
		}
		return numAV(formatNumber(new(big.Rat).Sub(x, y))), nil
	}
	return nil, fmt.Errorf("unknown operand %T", op)
}

func missingOperandErr() error {
	return validationErr("The provided expression refers to an attribute that does not exist in the item")
}

func operandTypeErr(op, typ string) error {
	return validationErr(fmt.Sprintf("Invalid UpdateExpression: Incorrect operand type for operator or function; operator or function: %s, operand type: %s", op, typeName(typ)))
}

func typeName(t string) string {
	switch t {
	case "M":
		return "MAP"
	case "L":
		return "LIST"
	case "NULL":
		return "NULL"
	case "BOOL":
		return "BOOLEAN"
	}
	return t
}

// EvalCondition evaluates a condition against an item. A nil item behaves
// as an item with no attributes.
func EvalCondition(c Condition, it Item) bool {
	switch c := c.(type) {
	case *LogicCond:
		if c.Op == "AND" {
			return EvalCondition(c.L, it) && EvalCondition(c.R, it)
		}
		return EvalCondition(c.L, it) || EvalCondition(c.R, it)
	case *NotCond:
		return !EvalCondition(c.C, it)
	case *CmpCond:
		a, _ := evalOperand(c.L, it)
		b, _ := evalOperand(c.R, it)
		if a == nil || b == nil {
			// A missing attribute is "not equal" to anything.
			return c.Op == "<>"
		}
		switch c.Op {
		case "=":
			return equalAV(a, b)
		case "<>":
			return !equalAV(a, b)
		}
		n, ok := compareScalar(a, b)
		if !ok {
			return false
		}
		switch c.Op {
		case "<":
			return n < 0
		case "<=":
			return n <= 0
		case ">":
			return n > 0
		case ">=":
			return n >= 0
		}
	case *BetweenCond:
		x, _ := evalOperand(c.X, it)
		lo, _ := evalOperand(c.Lo, it)
		hi, _ := evalOperand(c.Hi, it)
		a, ok1 := compareScalar(lo, x)
		b, ok2 := compareScalar(x, hi)
		return ok1 && ok2 && a <= 0 && b <= 0
	case *InCond:
		x, _ := evalOperand(c.X, it)
		for _, o := range c.List {
			if v, _ := evalOperand(o, it); equalAV(x, v) {
				return true
			}
		}
		return false
	case *FuncCond:
		return evalFuncCond(c, it)
	}
	return false
}

func evalFuncCond(c *FuncCond, it Item) bool {
	path := c.Args[0].(*PathExpr)
	v := getPath(it, path)
	switch c.Name {
	case "attribute_exists":
		return v != nil
	case "attribute_not_exists":
		return v == nil
	}
	arg, _ := evalOperand(c.Args[1], it)
	if v == nil || arg == nil {
		return false
	}
	switch c.Name {
	case "attribute_type":
		return arg.Type == "S" && v.Type == arg.S
	case "begins_with":
		return (v.Type == "S" || v.Type == "B") && v.Type == arg.Type && strings.HasPrefix(v.S, arg.S)
	case "contains":
		switch v.Type {
		case "S", "B":
			return arg.Type == v.Type && strings.Contains(v.S, arg.S)
		case "SS", "NS", "BS":
			if arg.Type != v.Type[:1] {
				return false
			}
			for _, s := range v.Set {
				if equalAV(&AttributeValue{Type: arg.Type, S: s}, arg) {
					return true
				}
			}
		case "L":
			for _, e := range v.L {
				if equalAV(e, arg) {
					return true
				}
			}
		}
	}
	return false
}

// ApplyUpdate runs an update expression against a copy of old (which may be
// nil) and returns the new item and the top-level attributes it touched.
// Every right-hand side is evaluated against the original item.
func ApplyUpdate(u *UpdateExpr, old Item) (Item, []string, error) {
	if old == nil {
		old = Item{}
	}
	it := old.clone()
	touched := map[string]bool{}

	for _, a := range u.Set {
		v, err := evalOperand(a.Value, old)
		if err != nil {
			return nil, nil, err
		}
		if v == nil {
			return nil, nil, missingOperandErr()
		}
		if err := setPath(it, a.Path, v.clone()); err != nil {
			return nil, nil, err
		}
		touched[a.Path.topLevel()] = true
	}

	// Removing list elements shifts later ones, so remove higher indexes
	// first; the paths all refer to positions in the original item.
	removes := append([]*PathExpr(nil), u.Remove...)
	sort.SliceStable(removes, func(i, j int) bool {
		a, b := removes[i].Elems, removes[j].Elems
		la, lb := a[len(a)-1], b[len(b)-1]
		return la.IsIndex && lb.IsIndex && la.Index > lb.Index
	})
	for _, p := range removes {
		removePath(it, p)
		touched[p.topLevel()] = true
	}

	for _, a := range u.Add {
		cur := getPath(it, a.Path)
		v := a.Value.V
		switch {
		case v.Type == "N":
			if cur == nil {
				cur = numAV("0")
			}
			if cur.Type != "N" {
				return nil, nil, operandTypeErr("ADD", cur.Type)
			}
			x, _ := parseNumber(cur.S)
			y, _ := parseNumber(v.S)
			if err := setPath(it, a.Path, numAV(formatNumber(new(big.Rat).Add(x, y)))); err != nil {
				return nil, nil, err
			}
		case v.isSet():
			next := v.clone()
			if cur != nil {
				if cur.Type != v.Type {
					return nil, nil, operandTypeErr("ADD", cur.Type)
				}
				next = setUnion(cur, v)
			}
			if err := setPath(it, a.Path, next); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, operandTypeErr("ADD", v.Type)
		}
		touched[a.Path.topLevel()] = true
	}

	for _, a := range u.Delete {
		cur := getPath(it, a.Path)
		v := a.Value.V
		if !v.isSet() {
			return nil, nil, operandTypeErr("DELETE", v.Type)
		}
		touched[a.Path.topLevel()] = true
		if cur == nil {
			continue
		}
		if cur.Type != v.Type {
			return nil, nil, operandTypeErr("DELETE", cur.Type)
		}
		next := setDifference(cur, v)
		if len(next.Set) == 0 {
			removePath(it, a.Path)
		} else if err := setPath(it, a.Path, next); err != nil {
			return nil, nil, err
		}
	}

	return it, sortedKeys(touched), nil
}

// Project builds an item holding only the given paths.
func Project(it Item, paths []*PathExpr) Item {
	out := Item{}
	for _, p := range paths {
		v := getPath(it, p)
		if v == nil {
			continue
		}
		projectInto(out, p.Elems, v)
	}
	return out
}

func projectInto(out Item, elems []PathElem, v *AttributeValue) {
	name := elems[0].Name
	if len(elems) == 1 {
		out[name] = v.clone()
		return
	}
	cur := out[name]
	if cur == nil {
		if elems[1].IsIndex {
			cur = listAV()
		} else {
			cur = &AttributeValue{Type: "M", M: map[string]*AttributeValue{}}
		}
		out[name] = cur
	}
	for i := 1; i < len(elems); i++ {
		e := elems[i]
		lastStep := i == len(elems)-1
		if e.IsIndex {
			// Projected list elements are packed in path order.
			if lastStep {
				cur.L = append(cur.L, v.clone())
				return
			}
			next := &AttributeValue{Type: "M", M: map[string]*AttributeValue{}}
			if elems[i+1].IsIndex {
				next = listAV()
			}
			cur.L = append(cur.L, next)
			cur = next
			continue
		}
		if lastStep {
			cur.M[e.Name] = v.clone()
			return
		}
		next := cur.M[e.Name]
		if next == nil {
			if elems[i+1].IsIndex {
				next = listAV()
			} else {
				next = &AttributeValue{Type: "M", M: map[string]*AttributeValue{}}
			}
			cur.M[e.Name] = next
		}
		cur = next
	}
}
