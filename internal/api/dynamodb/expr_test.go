// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustItem(t *testing.T, js string) Item {
	t.Helper()
	var it Item
	if err := json.Unmarshal([]byte(js), &it); err != nil {
		t.Fatalf("bad item %s: %v", js, err)
	}
	return it
}

func mustValues(t *testing.T, js string) map[string]*AttributeValue {
	t.Helper()
	var v map[string]*AttributeValue
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		t.Fatalf("bad values %s: %v", js, err)
	}
	return v
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const sample = `{
	"pk": {"S": "user#1"},
	"age": {"N": "42"},
	"name": {"S": "Ada Lovelace"},
	"tags": {"SS": ["a", "b"]},
	"nums": {"NS": ["1", "2.5"]},
	"list": {"L": [{"S": "x"}, {"N": "7"}]},
	"map": {"M": {"inner": {"S": "deep"}, "n": {"N": "3"}}},
	"flag": {"BOOL": true},
	"nothing": {"NULL": true}
}`

func TestConditionEvaluation(t *testing.T) {
	it := mustItem(t, sample)
	values := mustValues(t, `{
		":age": {"N": "42"}, ":age2": {"N": "42.0"}, ":lo": {"N": "40"}, ":hi": {"N": "50"},
		":pre": {"S": "user#"}, ":a": {"S": "a"}, ":z": {"S": "z"}, ":x": {"S": "x"},
		":three": {"N": "3"}, ":deep": {"S": "deep"}, ":S": {"S": "S"}, ":SS": {"S": "SS"},
		":t": {"BOOL": true}, ":love": {"S": "Love"}, ":n7": {"N": "7"}, ":two": {"N": "2"}
	}`)
	names := map[string]string{"#n": "name", "#m": "map", "#s": "size"}

	cases := []struct {
		expr string
		want bool
	}{
		{"age = :age", true},
		{"age = :age2", true}, // numeric equality ignores representation
		{"age <> :age", false},
		{"age < :hi AND age > :lo", true},
		{"age <= :age AND age >= :age", true},
		{"age BETWEEN :lo AND :hi", true},
		{"age BETWEEN :hi AND :lo", false},
		{"age IN (:lo, :age)", true},
		{"age IN (:lo, :hi)", false},
		{"begins_with(pk, :pre)", true},
		{"begins_with(#n, :pre)", false},
		{"contains(tags, :a)", true},
		{"contains(tags, :z)", false},
		{"contains(#n, :love)", true},
		{"contains(list, :x)", true},
		{"contains(list, :n7)", true},
		{"contains(nums, :two)", false},
		{"attribute_exists(age)", true},
		{"attribute_not_exists(age)", true == false},
		{"attribute_not_exists(missing)", true},
		{"attribute_exists(#m.inner)", true},
		{"attribute_exists(list[1])", true},
		{"attribute_exists(list[2])", false},
		{"#m.inner = :deep", true},
		{"#m.n = :three", true},
		{"list[0] = :x", true},
		{"attribute_type(pk, :S)", true},
		{"attribute_type(tags, :SS)", true},
		{"attribute_type(pk, :SS)", false},
		{"size(tags) = :two", true},
		{"size(#m) = :two", true},
		{"size(list) < :three", true},
		{"size(missing) = :two", false},
		{"flag = :t", true},
		{"NOT age = :age", false},
		{"NOT (age = :lo OR age = :hi)", true},
		{"age = :lo OR age = :age AND flag = :t", true},
		{"(age = :lo OR age = :age) AND NOT flag = :t", false},
		{"missing = :x", false},
		{"missing <> :x", true},
		{"missing < :x", false},
		{"pk < :age", false}, // different types never order
		{"age between :lo and :hi", true},
		{"not attribute_exists(missing) and age in (:age)", true},
	}
	for _, tc := range cases {
		ctx := NewExprContext(names, values)
		c, err := ParseCondition(ctx, "ConditionExpression", tc.expr)
		if err != nil {
			t.Errorf("%s: parse error %v", tc.expr, err)
			continue
		}
		if got := EvalCondition(c, it); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.expr, got, tc.want)
		}
	}

	// Conditions on a missing item, as for a new PutItem.
	ctx := NewExprContext(map[string]string{"#l": "LockID"}, nil)
	c, err := ParseCondition(ctx, "ConditionExpression", "attribute_not_exists(#l)")
	if err != nil || !EvalCondition(c, nil) {
		t.Fatalf("attribute_not_exists on a missing item should pass (err %v)", err)
	}
}

func TestConditionParseErrors(t *testing.T) {
	values := mustValues(t, `{":v": {"S": "x"}}`)
	cases := map[string]string{
		"a = :missing":           "attribute value used in expression is not defined",
		"#nope = :v":             "attribute name used in the document path is not defined",
		"a =":                    "Syntax error",
		"a = :v AND":             "Syntax error",
		"(a = :v":                "Syntax error",
		"a = :v)":                "Syntax error",
		"size(a)":                "not allowed to be used this way",
		"foo(a) = :v":            "Invalid function name",
		"attribute_exists(:v)":   "requires a document path",
		"begins_with(a)":         "Incorrect number of operands",
		"a BETWEEN :v":           "Syntax error",
		"a IN ()":                "Syntax error",
		"a = :v $ b":             "Invalid character",
		"status = :v":            "reserved keyword",
		"":                       "can not be empty",
		"if_not_exists(a, :v)":   "not allowed",
		"a = list_append(a, :v)": "not allowed",
	}
	for expr, want := range cases {
		ctx := NewExprContext(nil, values)
		_, err := ParseCondition(ctx, "ConditionExpression", expr)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error containing %q", expr, err, want)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "ValidationException") {
			t.Errorf("%q: error is not a ValidationException: %v", expr, err)
		}
	}
}

func TestUnusedPlaceholders(t *testing.T) {
	ctx := NewExprContext(map[string]string{"#a": "a", "#b": "b"}, mustValues(t, `{":v": {"S": "x"}, ":w": {"S": "y"}}`))
	if _, err := ParseCondition(ctx, "ConditionExpression", "#a = :v"); err != nil {
		t.Fatal(err)
	}
	err := ctx.CheckUnused()
	if err == nil || !strings.Contains(err.Error(), "{#b}") {
		t.Fatalf("expected unused #b, got %v", err)
	}
	if _, err := ParseCondition(ctx, "FilterExpression", "#b = :w"); err != nil {
		t.Fatal(err)
	}
	if err := ctx.CheckUnused(); err != nil {
		t.Fatalf("all used across expressions, got %v", err)
	}
}

func TestUpdateExpressions(t *testing.T) {
	values := mustValues(t, `{
		":one": {"N": "1"}, ":ten": {"N": "10"}, ":c": {"S": "c"}, ":ab": {"SS": ["a", "z"]},
		":more": {"L": [{"S": "y"}]}, ":new": {"S": "new"}, ":zero": {"N": "0"},
		":rm": {"SS": ["a", "b"]}, ":half": {"N": "0.5"}, ":ns": {"NS": ["1.0"]}
	}`)
	cases := []struct {
		expr    string
		check   string // JSON of selected attributes after the update
		attrs   []string
		touched string
	}{
		{"SET age = age + :one", `{"age":{"N":"43"}}`, []string{"age"}, "age"},
		{"SET age = :ten - age", `{"age":{"N":"-32"}}`, []string{"age"}, "age"},
		{"SET age = age + :half", `{"age":{"N":"42.5"}}`, []string{"age"}, "age"},
		{"SET counter = if_not_exists(counter, :zero) + :one", `{"counter":{"N":"1"}}`, []string{"counter"}, "counter"},
		{"SET age = if_not_exists(age, :zero)", `{"age":{"N":"42"}}`, []string{"age"}, "age"},
		{"SET list = list_append(list, :more)", `{"list":{"L":[{"S":"x"},{"N":"7"},{"S":"y"}]}}`, []string{"list"}, "list"},
		{"SET list = list_append(:more, list)", `{"list":{"L":[{"S":"y"},{"S":"x"},{"N":"7"}]}}`, []string{"list"}, "list"},
		{"SET #m.inner = :new, list[0] = :new", `{"list":{"L":[{"S":"new"},{"N":"7"}]},"map":{"M":{"inner":{"S":"new"},"n":{"N":"3"}}}}`, []string{"map", "list"}, "list,map"},
		{"SET list[9] = :new", `{"list":{"L":[{"S":"x"},{"N":"7"},{"S":"new"}]}}`, []string{"list"}, "list"},
		{"REMOVE age, #m.n, list[0]", `{"list":{"L":[{"N":"7"}]},"map":{"M":{"inner":{"S":"deep"}}}}`, []string{"age", "map", "list"}, "age,list,map"},
		{"ADD age :one, tags :ab", `{"age":{"N":"43"},"tags":{"SS":["a","b","z"]}}`, []string{"age", "tags"}, "age,tags"},
		{"ADD fresh :ten", `{"fresh":{"N":"10"}}`, []string{"fresh"}, "fresh"},
		{"DELETE tags :rm", `{}`, []string{"tags"}, "tags"},
		{"set age = :ten remove flag add tags :ab delete nums :ns", `{"age":{"N":"10"},"nums":{"NS":["2.5"]}}`, []string{"age", "flag", "nums"}, "age,flag,nums,tags"},
	}
	for _, tc := range cases {
		ctx := NewExprContext(map[string]string{"#m": "map"}, values)
		u, err := ParseUpdate(ctx, tc.expr)
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		old := mustItem(t, sample)
		got, touched, err := ApplyUpdate(u, old)
		if err != nil {
			t.Errorf("%s: apply: %v", tc.expr, err)
			continue
		}
		if s := toJSON(pick(got, tc.attrs)); s != tc.check {
			t.Errorf("%s:\n got  %s\n want %s", tc.expr, s, tc.check)
		}
		if s := strings.Join(touched, ","); s != tc.touched {
			t.Errorf("%s: touched %s want %s", tc.expr, s, tc.touched)
		}
		if toJSON(old) != toJSON(mustItem(t, sample)) {
			t.Errorf("%s: the original item was modified", tc.expr)
		}
	}
}

func TestUpdateErrors(t *testing.T) {
	values := mustValues(t, `{":one": {"N": "1"}, ":s": {"S": "s"}, ":l": {"L": []}}`)
	cases := map[string]string{
		"SET a = :one, a = :s":           "overlap",
		"SET #m.x = :one REMOVE #m":      "overlap",
		"SET pk = pk + :one":             "Incorrect operand type",
		"SET x = missing + :one":         "does not exist",
		"SET name = list_append(:s, :l)": "reserved keyword",
		"SET x = list_append(:s, :l)":    "Incorrect operand type",
		"SET nope.inner = :one":          "document path provided in the update expression is invalid",
		"ADD pk :one":                    "Incorrect operand type",
		"DELETE tags :s":                 "Incorrect operand type",
		"SET a = :one SET b = :one":      "only be used once",
		"FROB a":                         "Syntax error",
		"SET a = size(b)":                "not allowed",
		"ADD a b":                        "requires a value",
	}
	for expr, want := range cases {
		ctx := NewExprContext(map[string]string{"#m": "map"}, values)
		u, err := ParseUpdate(ctx, expr)
		if err == nil {
			_, _, err = ApplyUpdate(u, mustItem(t, sample))
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error containing %q", expr, err, want)
		}
	}
}

func TestProjection(t *testing.T) {
	it := mustItem(t, sample)
	ctx := NewExprContext(map[string]string{"#m": "map"}, nil)
	paths, err := ParseProjection(ctx, "pk, #m.inner, list[1], missing")
	if err != nil {
		t.Fatal(err)
	}
	got := toJSON(Project(it, paths))
	want := `{"list":{"L":[{"N":"7"}]},"map":{"M":{"inner":{"S":"deep"}}},"pk":{"S":"user#1"}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestAttributeValueRoundTrip(t *testing.T) {
	js := `{"b":{"B":"aGk="},"bs":{"BS":["aGk=","eW8="]},"e":{"L":[]},"m":{"M":{}},"n":{"N":"1.50"},"no":{"BOOL":false},"nul":{"NULL":true}}`
	it := mustItem(t, js)
	if got := toJSON(it); got != js {
		t.Fatalf("round trip:\n got  %s\n want %s", got, js)
	}
	if it["b"].S != "hi" {
		t.Fatalf("binary not decoded: %q", it["b"].S)
	}
	var bad Item
	if err := json.Unmarshal([]byte(`{"x":{"N":"abc"}}`), &bad); err == nil {
		t.Fatal("expected a number error")
	}
	if err := json.Unmarshal([]byte(`{"x":{"S":"a","N":"1"}}`), &bad); err == nil {
		t.Fatal("expected a multiple-type error")
	}
}

func TestKeyConditionValidation(t *testing.T) {
	v := &indexView{hash: "pk", rng: "sk"}
	values := mustValues(t, `{":p": {"S": "p"}, ":a": {"S": "a"}, ":b": {"S": "b"}}`)
	good := []string{
		"pk = :p",
		"pk = :p AND sk = :a",
		"pk = :p AND sk BETWEEN :a AND :b",
		"pk = :p AND begins_with(sk, :a)",
		"sk > :a AND pk = :p",
		":p = pk",
	}
	bad := []string{
		"sk = :a",
		"pk > :p",
		"pk = :p OR sk = :a",
		"pk = :p AND other = :a",
		"pk = :p AND sk = :a AND sk = :b",
		"pk = :p AND contains(sk, :a)",
		"NOT pk = :p",
		"pk = :p AND sk <> :a",
	}
	for _, e := range good {
		c, err := ParseCondition(NewExprContext(nil, values), "KeyConditionExpression", e)
		if err == nil {
			err = validateKeyCondition(c, v)
		}
		if err != nil {
			t.Errorf("%q should be valid: %v", e, err)
		}
	}
	for _, e := range bad {
		c, err := ParseCondition(NewExprContext(nil, values), "KeyConditionExpression", e)
		if err == nil {
			err = validateKeyCondition(c, v)
		}
		if err == nil {
			t.Errorf("%q should be rejected", e)
		}
	}
}

func TestLegacyConditions(t *testing.T) {
	it := mustItem(t, sample)
	v := mustValues(t, `{"age": {"N": "42"}, "lo": {"N": "1"}, "pre": {"S": "user"}}`)
	f := false
	c, err := expectedCondition(map[string]legacyExpected{
		"age":     {Value: v["age"]},
		"missing": {Exists: &f},
	}, "")
	if err != nil || !EvalCondition(c, it) {
		t.Fatalf("Expected should pass: %v", err)
	}
	c, err = legacyConditions(map[string]legacyCondition{
		"pk":  {ComparisonOperator: "BEGINS_WITH", AttributeValueList: []*AttributeValue{v["pre"]}},
		"age": {ComparisonOperator: "BETWEEN", AttributeValueList: []*AttributeValue{v["lo"], v["age"]}},
	}, "AND")
	if err != nil || !EvalCondition(c, it) {
		t.Fatalf("legacy conditions should pass: %v", err)
	}
}
