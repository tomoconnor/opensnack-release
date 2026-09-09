// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs_test

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/sqs"
	"opensnack/internal/resource"
)

//
// ─────────────────────────────────────────────────────────────
// Mock Store
// ─────────────────────────────────────────────────────────────
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
	var out []resource.Resource
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
// ─────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────
//

func newContext(method, target string, body *strings.Reader) (*httptest.ResponseRecorder, *http.Request) {
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
// ─────────────────────────────────────────────────────────────
// TESTS
// ─────────────────────────────────────────────────────────────
//

// -------------------------------------------------------------
// TestCreateQueue
// -------------------------------------------------------------
func TestCreateQueue_Basic(t *testing.T) {
	store := NewMockStore()
	h := sqs.NewHandler(store)

	form := strings.NewReader("QueueName=testqueue")
	rec, req := newContext("POST", "/sqs?Action=CreateQueue", form)

	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp sqs.CreateQueueResponse
	if xml.Unmarshal(rec.Body.Bytes(), &resp) != nil {
		t.Fatalf("invalid XML returned: %s", rec.Body.String())
	}

	expectedURL := "http://localhost:4566/000000000000/testqueue"
	if resp.CreateQueueResult.QueueUrl != expectedURL {
		t.Fatalf("wrong queue URL: %s", resp.CreateQueueResult.QueueUrl)
	}
}

// -------------------------------------------------------------
// TestCreateQueue_Idempotent
// -------------------------------------------------------------
func TestCreateQueue_Idempotent(t *testing.T) {
	store := NewMockStore()
	h := sqs.NewHandler(store)

	// First create
	form := strings.NewReader("QueueName=dupq")
	rec1, req1 := newContext("POST", "/sqs?Action=CreateQueue", form)
	h.Dispatch(rec1, req1)

	// Second create (should not error)
	form2 := strings.NewReader("QueueName=dupq")
	rec2, req2 := newContext("POST", "/sqs?Action=CreateQueue", form2)
	h.Dispatch(rec2, req2)

	if rec2.Code != 200 {
		t.Fatalf("expected 200 on idempotent create, got %d", rec2.Code)
	}
}

// -------------------------------------------------------------
// TestListQueues
// -------------------------------------------------------------
func TestListQueues(t *testing.T) {
	store := NewMockStore()
	h := sqs.NewHandler(store)

	// Create multiple queues
	names := []string{"q1", "q2", "q3"}
	for _, name := range names {
		attr := map[string]any{
			"name":       name,
			"created_at": time.Now(),
		}
		buf, _ := json.Marshal(attr)
		store.Create(&resource.Resource{
			ID:         name,
			Namespace:  "ns1",
			Service:    "sqs",
			Type:       "queue",
			Attributes: buf,
		})
	}

	rec, req := newContext("POST", "/sqs?Action=ListQueues", nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp sqs.ListQueuesResponse
	xml.Unmarshal(rec.Body.Bytes(), &resp)

	if len(resp.ListQueuesResult.QueueUrls) != 3 {
		t.Fatalf("expected 3 queues, got %d", len(resp.ListQueuesResult.QueueUrls))
	}
}

// -------------------------------------------------------------
// TestGetQueueUrl_Found
// -------------------------------------------------------------
func TestGetQueueUrl_Found(t *testing.T) {
	store := NewMockStore()
	h := sqs.NewHandler(store)

	entry := map[string]any{"name": "foundq", "created_at": time.Now()}
	buf, _ := json.Marshal(entry)

	store.Create(&resource.Resource{
		ID:         "foundq",
		Namespace:  "ns1",
		Service:    "sqs",
		Type:       "queue",
		Attributes: buf,
	})

	rec, req := newContext("POST", "/sqs?Action=GetQueueUrl&QueueName=foundq", nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "<QueueUrl>http://localhost:4566/000000000000/foundq</QueueUrl>") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

// -------------------------------------------------------------
// TestGetQueueUrl_NotFound
// -------------------------------------------------------------
func TestGetQueueUrl_NotFound(t *testing.T) {
	store := NewMockStore()
	h := sqs.NewHandler(store)

	rec, req := newContext("POST", "/sqs?Action=GetQueueUrl&QueueName=nope", nil)
	h.Dispatch(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "AWS.SimpleQueueService.NonExistentQueue") {
		t.Fatalf("expected NonExistentQueue error: %s", body)
	}
}

// -------------------------------------------------------------
// TestDeleteQueue
// -------------------------------------------------------------
func TestDeleteQueue(t *testing.T) {
	store := NewMockStore()
	h := sqs.NewHandler(store)

	// Precreate queue
	entry := map[string]any{"name": "delq", "created_at": time.Now()}
	buf, _ := json.Marshal(entry)
	store.Create(&resource.Resource{
		ID:         "delq",
		Namespace:  "ns1",
		Service:    "sqs",
		Type:       "queue",
		Attributes: buf,
	})

	// Delete
	rec, req := newContext("POST", "/sqs?Action=DeleteQueue&QueueUrl=http://localhost:4566/000000000000/delq", nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Body must be empty
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got: %s", rec.Body.String())
	}
}
