// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

// The pre-expression parameters (Expected, KeyConditions, QueryFilter,
// ScanFilter) are translated into the same condition AST the expression
// parser builds, so there is one evaluator.

type legacyCondition struct {
	AttributeValueList []*AttributeValue
	ComparisonOperator string
}

type legacyExpected struct {
	Value              *AttributeValue
	Exists             *bool
	ComparisonOperator string
	AttributeValueList []*AttributeValue
}

func legacyConditions(m map[string]legacyCondition, conditionalOperator string) (Condition, error) {
	var out Condition
	for _, name := range sortedKeys(m) {
		c, err := legacyComparison(name, m[name].ComparisonOperator, m[name].AttributeValueList)
		if err != nil {
			return nil, err
		}
		out = joinConditions(out, c, conditionalOperator)
	}
	return out, nil
}

func expectedCondition(m map[string]legacyExpected, conditionalOperator string) (Condition, error) {
	var out Condition
	for _, name := range sortedKeys(m) {
		e := m[name]
		path := &PathExpr{Elems: []PathElem{{Name: name}}}
		var c Condition
		switch {
		case e.ComparisonOperator != "":
			list := e.AttributeValueList
			if e.Value != nil && list == nil {
				list = []*AttributeValue{e.Value}
			}
			var err error
			if c, err = legacyComparison(name, e.ComparisonOperator, list); err != nil {
				return nil, err
			}
		case e.Exists != nil && !*e.Exists:
			if e.Value != nil {
				return nil, validationErr("One or more parameter values were invalid: Value cannot be used when Exists is false for Attribute: " + name)
			}
			c = &FuncCond{Name: "attribute_not_exists", Args: []Operand{path}}
		case e.Value != nil:
			c = &CmpCond{Op: "=", L: path, R: &ValueExpr{V: e.Value}}
		default:
			return nil, validationErr("One or more parameter values were invalid: Value must be provided when Exists is true for Attribute: " + name)
		}
		out = joinConditions(out, c, conditionalOperator)
	}
	return out, nil
}

func joinConditions(a, b Condition, op string) Condition {
	if a == nil {
		return b
	}
	if op == "OR" {
		return &LogicCond{Op: "OR", L: a, R: b}
	}
	return &LogicCond{Op: "AND", L: a, R: b}
}

func legacyComparison(name, op string, list []*AttributeValue) (Condition, error) {
	path := &PathExpr{Elems: []PathElem{{Name: name}}}
	want := map[string]int{
		"EQ": 1, "NE": 1, "LE": 1, "LT": 1, "GE": 1, "GT": 1,
		"NOT_NULL": 0, "NULL": 0, "CONTAINS": 1, "NOT_CONTAINS": 1,
		"BEGINS_WITH": 1, "BETWEEN": 2,
	}
	n, known := want[op]
	if op == "IN" {
		if len(list) == 0 {
			return nil, validationErr("One or more parameter values were invalid: Invalid number of argument(s) for the IN ComparisonOperator")
		}
	} else if !known {
		return nil, validationErr("1 validation error detected: Value '" + op + "' at 'comparisonOperator' failed to satisfy constraint: Member must satisfy enum value set")
	} else if len(list) != n {
		return nil, validationErr("One or more parameter values were invalid: Invalid number of argument(s) for the " + op + " ComparisonOperator")
	}
	val := func(i int) Operand { return &ValueExpr{V: list[i]} }
	cmp := map[string]string{"EQ": "=", "NE": "<>", "LE": "<=", "LT": "<", "GE": ">=", "GT": ">"}
	switch op {
	case "EQ", "NE", "LE", "LT", "GE", "GT":
		return &CmpCond{Op: cmp[op], L: path, R: val(0)}, nil
	case "NOT_NULL":
		return &FuncCond{Name: "attribute_exists", Args: []Operand{path}}, nil
	case "NULL":
		return &FuncCond{Name: "attribute_not_exists", Args: []Operand{path}}, nil
	case "CONTAINS":
		return &FuncCond{Name: "contains", Args: []Operand{path, val(0)}}, nil
	case "NOT_CONTAINS":
		return &LogicCond{Op: "AND",
			L: &FuncCond{Name: "attribute_exists", Args: []Operand{path}},
			R: &NotCond{C: &FuncCond{Name: "contains", Args: []Operand{path, val(0)}}}}, nil
	case "BEGINS_WITH":
		return &FuncCond{Name: "begins_with", Args: []Operand{path, val(0)}}, nil
	case "BETWEEN":
		return &BetweenCond{X: path, Lo: val(0), Hi: val(1)}, nil
	}
	in := &InCond{X: path}
	for i := range list {
		in.List = append(in.List, val(i))
	}
	return in, nil
}
