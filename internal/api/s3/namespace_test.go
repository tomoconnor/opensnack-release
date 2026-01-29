// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3_test

import (
	"encoding/json"
	"testing"
	"time"

	"opensnack/internal/api/s3"
	"opensnack/internal/resource"
)

type MockStore struct {
	data map[string]resource.Resource
}

func NewMockStore() *MockStore { return &MockStore{data: map[string]resource.Resource{}} }

// func (m *MockStore) Create(r *resource.Resource) error {
// 	m.data[r.Namespace+"|"+r.ID] = *r
// 	return nil
// }

// func (m *MockStore) Get(id, service, typ, namespace string) (*resource.Resource, error) {
// 	r, ok := m.data[namespace+"|"+id]
// 	if !ok {
// 		return nil, echo.NewHTTPError(404)
// 	}
// 	return &r, nil
// }

// func (m *MockStore) List(service, typ, namespace string) ([]resource.Resource, error) {
// 	var out []resource.Resource
// 	for k, v := range m.data {
// 		if v.Service == service && v.Type == typ && v.Namespace == namespace {
// 			_ = k
// 			out = append(out, v)
// 		}
// 	}
// 	return out, nil
// }

// func (m *MockStore) Delete(id, service, typ, namespace string) error {
// 	delete(m.data, namespace+"|"+id)
// 	return nil
// }

func TestNamespaceIsolation(t *testing.T) {
	store := NewMockStore()

	entry := s3.BucketEntry{Name: "b1", CreationDate: time.Now()}
	buf, _ := json.Marshal(entry)

	// Same bucket name in two namespaces
	store.Create(&resource.Resource{
		ID:         "b1",
		Namespace:  "ns1",
		Service:    "s3",
		Type:       "bucket",
		Attributes: buf,
	})
	store.Create(&resource.Resource{
		ID:         "b1",
		Namespace:  "ns2",
		Service:    "s3",
		Type:       "bucket",
		Attributes: buf,
	})

	ns1list, _ := store.List("s3", "bucket", "ns1")
	ns2list, _ := store.List("s3", "bucket", "ns2")

	if len(ns1list) != 1 || len(ns2list) != 1 {
		t.Fatalf("namespace isolation broken")
	}

	if ns1list[0].Namespace == ns2list[0].Namespace {
		t.Fatalf("namespaces should be isolated")
	}
}
