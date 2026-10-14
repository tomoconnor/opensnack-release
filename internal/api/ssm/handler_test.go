// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ssm_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"opensnack/internal/api/ssm"
	"opensnack/internal/resource"
)

func call(t *testing.T, h *ssm.Handler, action string, input any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(input)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonSSM."+action)
	rec := httptest.NewRecorder()
	h.Dispatch(rec, req)
	return rec
}

func ok(t *testing.T, h *ssm.Handler, action string, input, out any) {
	t.Helper()
	rec := call(t, h, action, input)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: expected 200, got %d: %s", action, rec.Code, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: bad JSON: %s", action, rec.Body.String())
		}
	}
}

func errType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Type string `json:"__type"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Type
}

func put(t *testing.T, h *ssm.Handler, name, typ, value string) {
	t.Helper()
	ok(t, h, "PutParameter", map[string]any{"Name": name, "Type": typ, "Value": value}, nil)
}

func names(ps []ssm.Parameter) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func TestGetParametersByPathRecursion(t *testing.T) {
	h := ssm.NewHandler(resource.NewMemStore())
	for _, n := range []string{"/app/a", "/app/b", "/app/db/user", "/app/db/deep/x", "/application/c", "/other/d", "plain"} {
		put(t, h, n, "String", "v")
	}
	put(t, h, "/app/db/pass", "SecureString", "secret")

	var out ssm.GetParametersByPathOutput
	ok(t, h, "GetParametersByPath", ssm.GetParametersByPathInput{Path: "/app"}, &out)
	if got := names(out.Parameters); !reflect.DeepEqual(got, []string{"/app/a", "/app/b"}) {
		t.Fatalf("one level: got %v", got)
	}

	// Recursive, paged three at a time
	var all []string
	in := ssm.GetParametersByPathInput{Path: "/app/", Recursive: true, MaxResults: 3}
	for pages := 0; ; pages++ {
		out = ssm.GetParametersByPathOutput{}
		ok(t, h, "GetParametersByPath", in, &out)
		all = append(all, names(out.Parameters)...)
		if out.NextToken == "" {
			break
		}
		if pages > 3 {
			t.Fatal("paging does not end")
		}
		in.NextToken = out.NextToken
	}
	want := []string{"/app/a", "/app/b", "/app/db/deep/x", "/app/db/pass", "/app/db/user"}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("recursive: got %v, want %v", all, want)
	}

	// ParameterFilters narrow the result; Name is not a valid key here
	out = ssm.GetParametersByPathOutput{}
	ok(t, h, "GetParametersByPath", map[string]any{
		"Path": "/app", "Recursive": true,
		"ParameterFilters": []map[string]any{{"Key": "Type", "Option": "Equals", "Values": []string{"SecureString"}}},
	}, &out)
	if got := names(out.Parameters); !reflect.DeepEqual(got, []string{"/app/db/pass"}) {
		t.Fatalf("Type filter: got %v", got)
	}
	if got := errType(t, call(t, h, "GetParametersByPath", map[string]any{
		"Path": "/app", "ParameterFilters": []map[string]any{{"Key": "Name", "Values": []string{"x"}}},
	})); got != "InvalidFilterKey" {
		t.Fatalf("expected InvalidFilterKey, got %s", got)
	}

	// The SecureString is only decrypted on request
	if out.Parameters[0].Value == "secret" {
		t.Fatal("SecureString returned in plaintext without WithDecryption")
	}
	out = ssm.GetParametersByPathOutput{}
	ok(t, h, "GetParametersByPath", ssm.GetParametersByPathInput{Path: "/app/db", WithDecryption: true}, &out)
	for _, p := range out.Parameters {
		if p.Name == "/app/db/pass" && p.Value != "secret" {
			t.Fatalf("expected the decrypted value, got %q", p.Value)
		}
	}
}

func TestSecureStringKeys(t *testing.T) {
	store := resource.NewMemStore()
	h := ssm.NewHandler(store)

	put(t, h, "/s/default", "SecureString", "one")

	var meta ssm.DescribeParametersOutput
	ok(t, h, "DescribeParameters", map[string]any{
		"ParameterFilters": []map[string]any{{"Key": "Name", "Option": "Equals", "Values": []string{"/s/default"}}},
	}, &meta)
	if len(meta.Parameters) != 1 || meta.Parameters[0].KeyId != "alias/aws/ssm" || meta.Parameters[0].DataType != "text" || meta.Parameters[0].Tier != "Standard" {
		t.Fatalf("unexpected metadata: %+v", meta.Parameters)
	}

	// A customer key, named by ARN through the KMS handler sharing the store
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-Amz-Target", "TrentService.CreateKey")
	h.KMS.Dispatch(rec, req)
	var key struct{ KeyMetadata struct{ KeyId, Arn string } }
	json.Unmarshal(rec.Body.Bytes(), &key)

	ok(t, h, "PutParameter", map[string]any{"Name": "/s/custom", "Type": "SecureString", "Value": "two", "KeyId": key.KeyMetadata.Arn}, nil)
	var got ssm.GetParameterOutput
	ok(t, h, "GetParameter", ssm.GetParameterInput{Name: "/s/custom", WithDecryption: true}, &got)
	if got.Parameter.Value != "two" {
		t.Fatalf("expected two, got %q", got.Parameter.Value)
	}

	if e := errType(t, call(t, h, "PutParameter", map[string]any{"Name": "/s/bad", "Type": "SecureString", "Value": "x", "KeyId": "alias/nope"})); e != "InvalidKeyId" {
		t.Fatalf("expected InvalidKeyId, got %s", e)
	}
}

func TestVersionsHistoryLabelsAndTags(t *testing.T) {
	h := ssm.NewHandler(resource.NewMemStore())

	ok(t, h, "PutParameter", map[string]any{"Name": "p", "Type": "String", "Value": "v1", "Description": "d",
		"Tags": []ssm.Tag{{Key: "team", Value: "team"}}}, nil)
	if e := errType(t, call(t, h, "PutParameter", map[string]any{"Name": "p", "Type": "String", "Value": "v2"})); e != "ParameterAlreadyExists" {
		t.Fatalf("expected ParameterAlreadyExists, got %s", e)
	}
	var v ssm.PutParameterOutput
	ok(t, h, "PutParameter", map[string]any{"Name": "p", "Value": "v2", "Overwrite": true}, &v)
	if v.Version != 2 {
		t.Fatalf("expected version 2, got %d", v.Version)
	}

	var hist ssm.GetParameterHistoryOutput
	ok(t, h, "GetParameterHistory", ssm.GetParameterHistoryInput{Name: "p"}, &hist)
	if len(hist.Parameters) != 2 || hist.Parameters[0].Value != "v1" || hist.Parameters[1].Value != "v2" ||
		hist.Parameters[1].Description != "d" || hist.Parameters[1].Type != "String" {
		t.Fatalf("unexpected history: %+v", hist.Parameters)
	}

	var lbl ssm.LabelParameterVersionOutput
	ok(t, h, "LabelParameterVersion", map[string]any{"Name": "p", "ParameterVersion": 1, "Labels": []string{"stable", "aws-bad"}}, &lbl)
	if !reflect.DeepEqual(lbl.InvalidLabels, []string{"aws-bad"}) {
		t.Fatalf("unexpected invalid labels: %v", lbl.InvalidLabels)
	}
	var got ssm.GetParameterOutput
	ok(t, h, "GetParameter", ssm.GetParameterInput{Name: "p:stable"}, &got)
	if got.Parameter.Value != "v1" || got.Parameter.Selector != ":stable" {
		t.Fatalf("label selector: %+v", got.Parameter)
	}
	ok(t, h, "GetParameter", ssm.GetParameterInput{Name: "p:2"}, &got)
	if got.Parameter.Value != "v2" {
		t.Fatalf("version selector: %+v", got.Parameter)
	}
	if got.Parameter.ARN != "arn:aws:ssm:us-east-1:000000000000:parameter/p" {
		t.Fatalf("unexpected ARN %s", got.Parameter.ARN)
	}

	ok(t, h, "AddTagsToResource", ssm.AddTagsToResourceInput{ResourceType: "Parameter", ResourceId: "p", Tags: []ssm.Tag{{Key: "a", Value: "1"}, {Key: "team", Value: "fleet"}}}, nil)
	ok(t, h, "RemoveTagsFromResource", ssm.RemoveTagsFromResourceInput{ResourceType: "Parameter", ResourceId: "p", TagKeys: []string{"a"}}, nil)
	var tags ssm.ListTagsForResourceOutput
	ok(t, h, "ListTagsForResource", ssm.ListTagsForResourceInput{ResourceType: "Parameter", ResourceId: "p"}, &tags)
	if !reflect.DeepEqual(tags.TagList, []ssm.Tag{{Key: "team", Value: "fleet"}}) {
		t.Fatalf("unexpected tags: %+v", tags.TagList)
	}
	if e := errType(t, call(t, h, "PutParameter", map[string]any{"Name": "p", "Value": "v3", "Overwrite": true, "Tags": []ssm.Tag{{Key: "x", Value: "y"}}})); e != "ValidationException" {
		t.Fatalf("tags with overwrite: expected ValidationException, got %s", e)
	}

	var desc ssm.DescribeParametersOutput
	ok(t, h, "DescribeParameters", map[string]any{
		"ParameterFilters": []map[string]any{{"Key": "tag:team", "Values": []string{"fleet"}}},
	}, &desc)
	if len(desc.Parameters) != 1 || desc.Parameters[0].Version != 2 {
		t.Fatalf("tag filter: %+v", desc.Parameters)
	}

	ok(t, h, "DeleteParameter", ssm.DeleteParameterInput{Name: "p"}, nil)
	if e := errType(t, call(t, h, "GetParameter", ssm.GetParameterInput{Name: "p"})); e != "ParameterNotFound" {
		t.Fatalf("expected ParameterNotFound, got %s", e)
	}
}
