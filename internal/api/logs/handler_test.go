// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/logs"
	"opensnack/internal/resource"

	"github.com/labstack/echo/v4"
)

//
// MockStore (same as other services)
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

func (m *MockStore) Get(id, s, t, ns string) (*resource.Resource, error) {
	v, ok := m.data[key(id, ns)]
	if !ok {
		return nil, echo.NewHTTPError(404)
	}
	return &v, nil
}

func (m *MockStore) List(s, t, ns string) ([]resource.Resource, error) {
	out := []resource.Resource{}
	for _, r := range m.data {
		if r.Service == s && r.Type == t && r.Namespace == ns {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *MockStore) Delete(id, s, t, ns string) error {
	delete(m.data, key(id, ns))
	return nil
}

//
// Helpers
//

func ctx(method, target string, body *strings.Reader, targetHeader string) (echo.Context, *httptest.ResponseRecorder, *echo.Echo) {
	e := echo.New()

	if body == nil {
		body = strings.NewReader("")
	}

	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", targetHeader)
	req.Header.Set("X-Opensnack-Namespace", "ns1")

	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec, e
}

//
// TESTS
//

func TestCreateLogGroup(t *testing.T) {
	store := NewMockStore()
	h := logs.NewHandler(store)

	body := `{"logGroupName":"MyGroup"}`
	c, rec, _ := ctx("POST", "/logs", strings.NewReader(body), "Logs_20140328.CreateLogGroup")
	_ = h.Dispatch(c)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if _, err := store.Get("MyGroup", "logs", "log_group", "ns1"); err != nil {
		t.Fatalf("log group not created")
	}
}

func TestDescribeLogGroups(t *testing.T) {
	store := NewMockStore()
	h := logs.NewHandler(store)

	// seed
	attrs := map[string]any{"group": "G1", "arn": logs.LogGroupArn("G1"), "created_at": time.Now().UnixMilli()}
	buf, _ := json.Marshal(attrs)

	store.Create(&resource.Resource{
		ID:         "G1",
		Namespace:  "ns1",
		Service:    "logs",
		Type:       "log_group",
		Attributes: buf,
	})

	c, rec, _ := ctx("POST", "/logs", strings.NewReader("{}"), "Logs_20140328.DescribeLogGroups")
	_ = h.Dispatch(c)

	if !strings.Contains(rec.Body.String(), `"logGroupName":"G1"`) {
		t.Fatalf("missing log group: %s", rec.Body.String())
	}
}

func TestCreateLogStream(t *testing.T) {
	store := NewMockStore()
	h := logs.NewHandler(store)

	body := `{"logGroupName":"GroupA","logStreamName":"Stream1"}`
	c, rec, _ := ctx("POST", "/logs", strings.NewReader(body), "Logs_20140328.CreateLogStream")
	_ = h.Dispatch(c)

	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}

	if _, err := store.Get("GroupA/Stream1", "logs", "log_stream", "ns1"); err != nil {
		t.Fatalf("log stream not created")
	}
}

func TestDescribeLogStreams(t *testing.T) {
	store := NewMockStore()
	h := logs.NewHandler(store)

	attrs := map[string]any{"group": "G2", "stream": "S1", "arn": logs.LogStreamArn("G2", "S1"), "created_at": time.Now().UnixMilli()}
	buf, _ := json.Marshal(attrs)

	store.Create(&resource.Resource{
		ID: "G2/S1", Namespace: "ns1",
		Service: "logs", Type: "log_stream",
		Attributes: buf,
	})

	body := `{"logGroupName":"G2"}`
	c, rec, _ := ctx("POST", "/logs", strings.NewReader(body), "Logs_20140328.DescribeLogStreams")
	_ = h.Dispatch(c)

	if !strings.Contains(rec.Body.String(), `"logStreamName":"S1"`) {
		t.Fatalf("missing S1: %s", rec.Body.String())
	}
}

func TestPutLogEvents(t *testing.T) {
	store := NewMockStore()
	h := logs.NewHandler(store)

	body := `{"logGroupName":"G1","logStreamName":"S1","logEvents":[{"timestamp":1,"message":"hi"}]}`
	c, rec, _ := ctx("POST", "/logs", strings.NewReader(body), "Logs_20140328.PutLogEvents")

	_ = h.Dispatch(c)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), `"nextSequenceToken":"0"`) {
		t.Fatalf("missing nextSequenceToken: %s", rec.Body.String())
	}
}
