// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sns_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/sns"
	"opensnack/internal/resource"
)

//
// MockStore (identical pattern to SQS & S3 tests)
//

type MockStore struct {
	data map[string]resource.Resource
}

func NewMockStore() *MockStore {
	return &MockStore{data: map[string]resource.Resource{}}
}

func key(id, ns string) string { return ns + "|" + id }

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

// Helper
func newCtx(method, target string, body *strings.Reader) (*httptest.ResponseRecorder, *http.Request) {
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
// TESTS
//

func TestCreateTopic(t *testing.T) {
	store := NewMockStore()
	h := sns.NewHandler(store)

	body := strings.NewReader("Name=mytopic")
	rec, req := newCtx("POST", "/sns?Action=CreateTopic", body)

	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "<TopicArn>arn:aws:sns:us-east-1:000000000000:mytopic</TopicArn>") {
		t.Fatalf("bad response: %s", rec.Body.String())
	}
}

func TestCreateTopic_Idempotent(t *testing.T) {
	store := NewMockStore()
	h := sns.NewHandler(store)

	body := strings.NewReader("Name=dup")
	rec1, req1 := newCtx("POST", "/sns?Action=CreateTopic", body)
	h.Dispatch(rec1, req1)

	body2 := strings.NewReader("Name=dup")
	rec2, req2 := newCtx("POST", "/sns?Action=CreateTopic", body2)
	h.Dispatch(rec2, req2)

	if rec2.Code != 200 {
		t.Fatalf("expected 200 for idempotent create")
	}
}

func TestListTopics(t *testing.T) {
	store := NewMockStore()
	h := sns.NewHandler(store)

	// seed two topics
	for _, name := range []string{"a", "b"} {
		buf, _ := json.Marshal(map[string]any{
			"name": name, "created_at": time.Now(),
		})
		store.Create(&resource.Resource{
			ID:         name,
			Namespace:  "ns1",
			Service:    "sns",
			Type:       "topic",
			Attributes: buf,
		})
	}

	rec, req := newCtx("POST", "/sns?Action=ListTopics", nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "<TopicArn>arn:aws:sns:us-east-1:000000000000:a</TopicArn>") {
		t.Fatalf("bad list response: %s", rec.Body.String())
	}
}

func TestDeleteTopic(t *testing.T) {
	store := NewMockStore()
	h := sns.NewHandler(store)

	name := "deadtopic"
	buf, _ := json.Marshal(map[string]any{"name": name})
	store.Create(&resource.Resource{
		ID:         name,
		Namespace:  "ns1",
		Service:    "sns",
		Type:       "topic",
		Attributes: buf,
	})

	arn := "arn:aws:sns:us-east-1:000000000000:" + name

	rec, req := newCtx("POST", "/sns?Action=DeleteTopic&TopicArn="+arn, nil)
	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestPublish(t *testing.T) {
	store := NewMockStore()
	h := sns.NewHandler(store)

	body := strings.NewReader("Message=hello")
	rec, req := newCtx("POST", "/sns?Action=Publish", body)

	h.Dispatch(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "<MessageId>") {
		t.Fatalf("Publish should return MessageId")
	}
}

// Regression: these handlers wrote an error but carried on to dereference the
// nil resource, panicking the connection instead of returning NotFound.
func TestMissingTopicDoesNotPanic(t *testing.T) {
	arn := "arn:aws:sns:us-east-1:000000000000:ghost"

	for name, target := range map[string]string{
		"GetTopicAttributes":        "/sns?Action=GetTopicAttributes&TopicArn=" + arn,
		"SetTopicAttributes":        "/sns?Action=SetTopicAttributes&TopicArn=" + arn + "&AttributeName=DisplayName&AttributeValue=x",
		"GetSubscriptionAttributes": "/sns?Action=GetSubscriptionAttributes&SubscriptionArn=" + arn + ":sub",
	} {
		t.Run(name, func(t *testing.T) {
			h := sns.NewHandler(NewMockStore())

			rec, req := newCtx("POST", target, nil)
			h.Dispatch(rec, req)

			if rec.Code == 200 {
				t.Fatalf("expected an error status, got 200: %s", rec.Body.String())
			}
		})
	}
}
