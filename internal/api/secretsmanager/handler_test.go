// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package secretsmanager_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	sm "opensnack/internal/api/secretsmanager"
	"opensnack/internal/resource"
)

func call(t *testing.T, h *sm.Handler, action string, input any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(input)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager."+action)
	rec := httptest.NewRecorder()
	h.Dispatch(rec, req)
	return rec
}

func ok(t *testing.T, h *sm.Handler, action string, input, out any) {
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
	// A map, not a struct: encoding/json matches keys case-insensitively,
	// and SDKs before the schema-based serde read only "Message".
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	typ, _ := body["__type"].(string)
	msg, _ := body["Message"].(string)
	return typ + ": " + msg
}

func str(s string) *string { return &s }

func stagesOf(t *testing.T, h *sm.Handler, id string) map[string][]string {
	t.Helper()
	var out sm.ListSecretVersionIdsOutput
	ok(t, h, "ListSecretVersionIds", sm.ListSecretVersionIdsInput{SecretId: id}, &out)
	m := map[string][]string{}
	for _, v := range out.Versions {
		s := append([]string{}, v.VersionStages...)
		sort.Strings(s)
		m[v.VersionId] = s
	}
	return m
}

func TestVersionStages(t *testing.T) {
	h := sm.NewHandler(resource.NewMemStore())

	var created sm.CreateSecretOutput
	ok(t, h, "CreateSecret", sm.CreateSecretInput{Name: "team", SecretString: str("v1")}, &created)
	v1 := created.VersionId

	var put sm.PutSecretValueOutput
	ok(t, h, "PutSecretValue", sm.PutSecretValueInput{SecretId: created.ARN, SecretString: str("v2")}, &put)
	v2 := put.VersionId
	ok(t, h, "PutSecretValue", sm.PutSecretValueInput{SecretId: "team", SecretString: str("v3")}, &put)
	v3 := put.VersionId

	got := stagesOf(t, h, "team")
	if len(got) != 2 || got[v3][0] != "AWSCURRENT" || got[v2][0] != "AWSPREVIOUS" {
		t.Fatalf("unexpected stages after two puts: %v", got)
	}

	// v1 is deprecated: hidden by default, still listed with IncludeDeprecated
	var all sm.ListSecretVersionIdsOutput
	ok(t, h, "ListSecretVersionIds", sm.ListSecretVersionIdsInput{SecretId: "team", IncludeDeprecated: true}, &all)
	if len(all.Versions) != 3 {
		t.Fatalf("expected 3 versions with IncludeDeprecated, got %+v", all.Versions)
	}

	for _, tc := range []struct {
		in   sm.GetSecretValueInput
		want string
	}{
		{sm.GetSecretValueInput{SecretId: "team"}, "v3"},
		{sm.GetSecretValueInput{SecretId: "team", VersionStage: "AWSPREVIOUS"}, "v2"},
		{sm.GetSecretValueInput{SecretId: "team", VersionId: v1}, "v1"},
		{sm.GetSecretValueInput{SecretId: created.ARN, VersionId: v3, VersionStage: "AWSCURRENT"}, "v3"},
	} {
		var out sm.GetSecretValueOutput
		ok(t, h, "GetSecretValue", tc.in, &out)
		if out.SecretString == nil || *out.SecretString != tc.want {
			t.Fatalf("%+v: expected %s, got %+v", tc.in, tc.want, out)
		}
	}
	if e := errType(t, call(t, h, "GetSecretValue", sm.GetSecretValueInput{SecretId: "team", VersionStage: "NOPE"})); !strings.HasPrefix(e, "ResourceNotFoundException") {
		t.Fatalf("expected ResourceNotFoundException, got %s", e)
	}

	// A custom label, then moving AWSCURRENT back to v1
	ok(t, h, "PutSecretValue", sm.PutSecretValueInput{SecretId: "team", SecretString: str("v4"), VersionStages: []string{"BETA"}}, &put)
	v4 := put.VersionId
	got = stagesOf(t, h, "team")
	if got[v4][0] != "BETA" || got[v3][0] != "AWSCURRENT" {
		t.Fatalf("a non-current put must not move AWSCURRENT: %v", got)
	}
	if e := errType(t, call(t, h, "UpdateSecretVersionStage", sm.UpdateSecretVersionStageInput{
		SecretId: "team", VersionStage: "AWSCURRENT", MoveToVersionId: v1,
	})); !strings.HasPrefix(e, "InvalidParameterException") {
		t.Fatalf("moving AWSCURRENT without RemoveFromVersionId: expected InvalidParameterException, got %s", e)
	}
	ok(t, h, "UpdateSecretVersionStage", sm.UpdateSecretVersionStageInput{
		SecretId: "team", VersionStage: "AWSCURRENT", MoveToVersionId: v1, RemoveFromVersionId: v3,
	}, nil)
	got = stagesOf(t, h, "team")
	if got[v1][0] != "AWSCURRENT" || got[v3][0] != "AWSPREVIOUS" || len(got[v2]) != 0 {
		t.Fatalf("unexpected stages after moving AWSCURRENT: %v", got)
	}
}

func TestDeleteRestoreAndRecreate(t *testing.T) {
	h := sm.NewHandler(resource.NewMemStore())

	var created sm.CreateSecretOutput
	ok(t, h, "CreateSecret", sm.CreateSecretInput{Name: "gone", SecretString: str("x")}, &created)

	var del sm.DeleteSecretOutput
	ok(t, h, "DeleteSecret", map[string]any{"SecretId": "gone", "RecoveryWindowInDays": 7}, &del)

	// Pending deletion: described with DeletedDate, unreadable, name taken
	var desc sm.DescribeSecretOutput
	ok(t, h, "DescribeSecret", sm.SecretIdInput{SecretId: created.ARN}, &desc)
	if desc.DeletedDate == 0 || desc.DeletedDate != del.DeletionDate {
		t.Fatalf("expected DeletedDate %v, got %+v", del.DeletionDate, desc)
	}
	if e := errType(t, call(t, h, "GetSecretValue", sm.GetSecretValueInput{SecretId: "gone"})); !strings.Contains(e, "marked for deletion") {
		t.Fatalf("expected marked for deletion, got %s", e)
	}
	if e := errType(t, call(t, h, "CreateSecret", sm.CreateSecretInput{Name: "gone"})); !strings.Contains(e, "already scheduled for deletion") {
		t.Fatalf("expected already scheduled for deletion, got %s", e)
	}
	var list sm.ListSecretsOutput
	ok(t, h, "ListSecrets", sm.ListSecretsInput{}, &list)
	if len(list.SecretList) != 0 {
		t.Fatalf("a secret pending deletion must not be listed: %+v", list)
	}

	ok(t, h, "RestoreSecret", sm.SecretIdInput{SecretId: "gone"}, nil)
	var val sm.GetSecretValueOutput
	ok(t, h, "GetSecretValue", sm.GetSecretValueInput{SecretId: created.ARN}, &val)

	// Force delete frees the name at once, and the new secret gets a new ARN
	ok(t, h, "DeleteSecret", map[string]any{"SecretId": "gone", "ForceDeleteWithoutRecovery": true}, nil)
	if e := errType(t, call(t, h, "DescribeSecret", sm.SecretIdInput{SecretId: "gone"})); !strings.HasPrefix(e, "ResourceNotFoundException") {
		t.Fatalf("expected ResourceNotFoundException, got %s", e)
	}
	var again sm.CreateSecretOutput
	ok(t, h, "CreateSecret", sm.CreateSecretInput{Name: "gone"}, &again)
	if again.ARN == created.ARN {
		t.Fatal("expected a new ARN suffix for a new secret")
	}
	if e := errType(t, call(t, h, "DescribeSecret", sm.SecretIdInput{SecretId: created.ARN})); !strings.HasPrefix(e, "ResourceNotFoundException") {
		t.Fatalf("the old ARN must not find the new secret, got %s", e)
	}
}

func TestARNStableAndPolicyExact(t *testing.T) {
	h := sm.NewHandler(resource.NewMemStore())

	var created sm.CreateSecretOutput
	ok(t, h, "CreateSecret", sm.CreateSecretInput{Name: "a-b-c", Description: "d1", Tags: []sm.Tag{{Key: "k", Value: "v"}}}, &created)
	if !strings.HasPrefix(created.ARN, "arn:aws:secretsmanager:us-east-1:000000000000:secret:a-b-c-") || len(created.ARN) != len("arn:aws:secretsmanager:us-east-1:000000000000:secret:a-b-c-")+6 {
		t.Fatalf("unexpected ARN %s", created.ARN)
	}

	var pol sm.GetResourcePolicyOutput
	ok(t, h, "GetResourcePolicy", sm.SecretIdInput{SecretId: created.ARN}, &pol)
	if pol.ResourcePolicy != "" {
		t.Fatalf("expected no policy, got %s", pol.ResourcePolicy)
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
	ok(t, h, "PutResourcePolicy", sm.PutResourcePolicyInput{SecretId: created.ARN, ResourcePolicy: policy}, nil)
	ok(t, h, "GetResourcePolicy", sm.SecretIdInput{SecretId: "a-b-c"}, &pol)
	if pol.ResourcePolicy != policy {
		t.Fatalf("policy not returned exactly: %s", pol.ResourcePolicy)
	}

	ok(t, h, "UpdateSecret", sm.UpdateSecretInput{SecretId: created.ARN, Description: str("d2")}, nil)
	ok(t, h, "TagResource", sm.TagResourceInput{SecretId: created.ARN, Tags: []sm.Tag{{Key: "k2", Value: "v2"}}}, nil)
	ok(t, h, "UntagResource", sm.UntagResourceInput{SecretId: created.ARN, TagKeys: []string{"k"}}, nil)
	ok(t, h, "DeleteResourcePolicy", sm.SecretIdInput{SecretId: created.ARN}, nil)

	var desc sm.DescribeSecretOutput
	ok(t, h, "DescribeSecret", sm.SecretIdInput{SecretId: created.ARN}, &desc)
	if desc.ARN != created.ARN || desc.Description != "d2" || len(desc.Tags) != 1 || desc.Tags[0].Key != "k2" {
		t.Fatalf("unexpected secret after updates: %+v", desc)
	}
	pol = sm.GetResourcePolicyOutput{}
	ok(t, h, "GetResourcePolicy", sm.SecretIdInput{SecretId: created.ARN}, &pol)
	if pol.ResourcePolicy != "" {
		t.Fatalf("policy not deleted: %s", pol.ResourcePolicy)
	}
}

// Terraform writes a secret's version and its policy at the same time; neither
// write may lose the other.
func TestConcurrentWritesKeepEachOther(t *testing.T) {
	h := sm.NewHandler(resource.NewMemStore())
	var created sm.CreateSecretOutput
	ok(t, h, "CreateSecret", sm.CreateSecretInput{Name: "busy"}, &created)

	const n = 20
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			if i%2 == 0 {
				call(t, h, "PutSecretValue", sm.PutSecretValueInput{SecretId: "busy", SecretString: str("v"), VersionStages: []string{"S" + string(rune('a'+i))}})
			} else {
				call(t, h, "TagResource", sm.TagResourceInput{SecretId: "busy", Tags: []sm.Tag{{Key: string(rune('a' + i)), Value: "x"}}})
			}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}

	var desc sm.DescribeSecretOutput
	ok(t, h, "DescribeSecret", sm.SecretIdInput{SecretId: "busy"}, &desc)
	if len(desc.VersionIdsToStages) != n/2 || len(desc.Tags) != n/2 {
		t.Fatalf("lost updates: %d versions, %d tags", len(desc.VersionIdsToStages), len(desc.Tags))
	}
}
