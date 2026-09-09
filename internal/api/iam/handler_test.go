// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam_test

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/iam"
	"opensnack/internal/resource"
)

//
// MockStore (same pattern as SQS/SNS)
//

type MockStore struct {
	data map[string]resource.Resource
}

func NewMockStore() *MockStore {
	return &MockStore{data: map[string]resource.Resource{}}
}

func key(id, ns string) string {
	return ns + "|" + id
}

func (m *MockStore) Create(r *resource.Resource) error {
	m.data[key(r.ID, r.Namespace)] = *r
	return nil
}

func (m *MockStore) Update(r *resource.Resource) error {
	m.data[key(r.ID, r.Namespace)] = *r
	return nil
}

func (m *MockStore) Get(id, service, typ, ns string) (*resource.Resource, error) {
	v, ok := m.data[key(id, ns)]
	if !ok {
		return nil, errors.New("not found")
	}
	return &v, nil
}

func (m *MockStore) List(service, typ, ns string) ([]resource.Resource, error) {
	out := []resource.Resource{}
	for _, v := range m.data {
		if v.Service == service && v.Type == typ && v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *MockStore) Delete(id, service, typ, ns string) error {
	delete(m.data, key(id, ns))
	return nil
}

//
// Test helper
//

func ctx(method, target string, body *strings.Reader) (*httptest.ResponseRecorder, *http.Request) {
	if body == nil {
		body = strings.NewReader("")
	}

	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Namespace isolation travels in the User-Agent (see k6/README.md):
	// Terraform cannot set custom headers, so a "custom-<ns>" suffix carries it.
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")

	return httptest.NewRecorder(), req
}

//
// ─────────────────────────────────────────────
// TESTS START HERE
// ─────────────────────────────────────────────
//

func TestCreateRole(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	body := strings.NewReader(`RoleName=MyRole&AssumeRolePolicyDocument=%7B%7D`)
	rec, req := ctx("POST", "/iam?Action=CreateRole", body)

	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "<RoleName>MyRole</RoleName>") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), "arn:aws:iam::000000000000:role/MyRole") {
		t.Fatalf("ARN missing: %s", rec.Body.String())
	}
}

func TestCreateRole_Idempotent(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	body := strings.NewReader(`RoleName=SameRole&AssumeRolePolicyDocument=%7B%7D`)
	rec1, req1 := ctx("POST", "/iam?Action=CreateRole", body)
	h.Dispatch(rec1, req1)

	body2 := strings.NewReader(`RoleName=SameRole&AssumeRolePolicyDocument=%7B%7D`)
	rec2, req2 := ctx("POST", "/iam?Action=CreateRole", body2)
	h.Dispatch(rec2, req2)

	if rec2.Code != 200 {
		t.Fatalf("idempotent create returned %d", rec2.Code)
	}
}

func TestGetRole(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	entry := map[string]any{
		"name":               "FetchRole",
		"assume_role_policy": "{}",
		"created_at":         time.Now().Format(time.RFC3339),
	}
	buf, _ := json.Marshal(entry)

	store.Create(&resource.Resource{
		ID:         "FetchRole",
		Namespace:  "ns1",
		Service:    "iam",
		Type:       "role",
		Attributes: buf,
	})

	rec, req := ctx("POST", "/iam?Action=GetRole&RoleName=FetchRole", nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "<RoleName>FetchRole</RoleName>") {
		t.Fatalf("body missing role: %s", rec.Body.String())
	}
}

func TestDeleteRole(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	entry := map[string]any{"name": "KillRole"}
	buf, _ := json.Marshal(entry)

	store.Create(&resource.Resource{
		ID:         "KillRole",
		Namespace:  "ns1",
		Service:    "iam",
		Type:       "role",
		Attributes: buf,
	})

	rec, req := ctx("POST", "/iam?Action=DeleteRole&RoleName=KillRole", nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("delete returned %d", rec.Code)
	}

	if _, err := store.Get("KillRole", "iam", "role", "ns1"); err == nil {
		t.Fatalf("role not deleted")
	}
}

func TestCreatePolicy(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	body := strings.NewReader(`PolicyName=MyPolicy&PolicyDocument=%7B%7D`)
	rec, req := ctx("POST", "/iam?Action=CreatePolicy", body)

	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "policy/MyPolicy") {
		t.Fatalf("bad ARN: %s", rec.Body.String())
	}
}

func TestGetPolicy(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	entry := map[string]any{
		"name":     "FetchPolicy",
		"document": "{}",
		"version":  "v1",
	}
	buf, _ := json.Marshal(entry)

	store.Create(&resource.Resource{
		ID:         "FetchPolicy",
		Namespace:  "ns1",
		Service:    "iam",
		Type:       "policy",
		Attributes: buf,
	})

	arn := "arn:aws:iam::000000000000:policy/FetchPolicy"
	rec, req := ctx("POST", "/iam?Action=GetPolicy&PolicyArn="+arn, nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200")
	}

	if !strings.Contains(rec.Body.String(), "<PolicyName>FetchPolicy</PolicyName>") {
		t.Fatalf("wrong response: %s", rec.Body.String())
	}
}

func TestGetPolicyVersion(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	entry := map[string]any{
		"name":     "VersionedPolicy",
		"document": `{"Statement":[]}`,
		"version":  "v1",
	}
	buf, _ := json.Marshal(entry)

	store.Create(&resource.Resource{
		ID:         "VersionedPolicy",
		Namespace:  "ns1",
		Service:    "iam",
		Type:       "policy",
		Attributes: buf,
	})

	arn := "arn:aws:iam::000000000000:policy/VersionedPolicy"
	url := "/iam?Action=GetPolicyVersion&PolicyArn=" + arn + "&VersionId=v1"

	rec, req := ctx("POST", url, nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200")
	}

	if !strings.Contains(rec.Body.String(), "<VersionId>v1</VersionId>") {
		t.Fatalf("missing version: %s", rec.Body.String())
	}
}

func TestAttachRolePolicy(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	rec, req := ctx("POST",
		"/iam?Action=AttachRolePolicy&RoleName=R1&PolicyArn=arn:aws:iam::000000000000:policy/P1",
		nil,
	)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200")
	}

	// attachment should exist
	_, err := store.Get("R1:P1", "iam", "attachment", "ns1")
	if err != nil {
		t.Fatalf("attachment not created")
	}
}

func TestListAttachedRolePolicies(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	// Seed attachments
	entry := map[string]any{
		"role":       "R2",
		"policy":     "P1",
		"policy_arn": "arn:aws:iam::000000000000:policy/P1",
	}
	buf, _ := json.Marshal(entry)

	store.Create(&resource.Resource{
		ID:         "R2:P1",
		Namespace:  "ns1",
		Service:    "iam",
		Type:       "attachment",
		Attributes: buf,
	})

	rec, req := ctx("POST", "/iam?Action=ListAttachedRolePolicies&RoleName=R2", nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200")
	}

	var resp iam.ListAttachedRolePoliciesResponse
	xml.Unmarshal(rec.Body.Bytes(), &resp)

	if len(resp.ListAttachedRolePoliciesResult.AttachedPolicies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(resp.ListAttachedRolePoliciesResult.AttachedPolicies))
	}
}

func TestDetachRolePolicy(t *testing.T) {
	store := NewMockStore()
	h := iam.NewHandler(store)

	entry := map[string]any{
		"role":       "DetachR",
		"policy":     "DetachP",
		"policy_arn": "arn:aws:iam::000000000000:policy/DetachP",
	}
	buf, _ := json.Marshal(entry)

	store.Create(&resource.Resource{
		ID:         "DetachR:DetachP",
		Namespace:  "ns1",
		Service:    "iam",
		Type:       "attachment",
		Attributes: buf,
	})

	rec, req := ctx("POST",
		"/iam?Action=DetachRolePolicy&RoleName=DetachR&PolicyArn=arn:aws:iam::000000000000:policy/DetachP",
		nil,
	)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if _, err := store.Get("DetachR:DetachP", "iam", "attachment", "ns1"); err == nil {
		t.Fatalf("attachment should be deleted")
	}
}

// Regression: these handlers wrote a 404 but carried on to dereference the nil
// resource, panicking the connection instead of returning NoSuchEntity.
func TestMissingPolicyDoesNotPanic(t *testing.T) {
	arn := "arn:aws:iam::000000000000:policy/ghost"

	for name, target := range map[string]string{
		"GetPolicy":          "/iam?Action=GetPolicy&PolicyArn=" + arn,
		"GetPolicyVersion":   "/iam?Action=GetPolicyVersion&PolicyArn=" + arn + "&VersionId=v1",
		"ListPolicyVersions": "/iam?Action=ListPolicyVersions&PolicyArn=" + arn,
	} {
		t.Run(name, func(t *testing.T) {
			h := iam.NewHandler(NewMockStore())

			rec, req := ctx("POST", target, nil)
			h.Dispatch(rec, req)

			if rec.Code != 404 {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "NoSuchEntity") {
				t.Fatalf("expected NoSuchEntity, got: %s", rec.Body.String())
			}
		})
	}
}
