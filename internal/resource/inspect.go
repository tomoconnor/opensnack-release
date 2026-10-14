// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package resource

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Filter narrows an inspection query. Empty fields match anything.
type Filter struct {
	Namespace string
	Region    string
	// RegionSet distinguishes "global only" (Region "" with RegionSet) from
	// "any region".
	RegionSet bool
	Service   string
	Type      string
	ID        string
	// HasAttr keeps only resources whose top-level attributes include this
	// key with a non-null value.
	HasAttr string
}

// Inspector is the read-everything view the internal API uses. Unlike Store,
// it sees the namespace and region as separate fields.
type Inspector interface {
	Find(f Filter) ([]Resource, error)
	Count(namespace string) (map[string]int, error)
	Namespaces() (map[string]int, error)
	DeleteNamespace(namespace string) (int64, error)
	// S3Bytes is the total size of a namespace's stored S3 objects.
	S3Bytes(namespace string) (int64, error)
}

func (s *GormStore) query(f Filter) *gorm.DB {
	q := s.db.Model(&Resource{})
	if f.Namespace != "" {
		q = q.Where("namespace = ?", f.Namespace)
	}
	if f.Region != "" || f.RegionSet {
		q = q.Where("region = ?", f.Region)
	}
	if f.Service != "" {
		q = q.Where("service = ?", f.Service)
	}
	if f.Type != "" {
		q = q.Where("type = ?", f.Type)
	}
	if f.ID != "" {
		q = q.Where("id = ?", f.ID)
	}
	if f.HasAttr != "" {
		q = q.Where("attributes ->> ? IS NOT NULL", f.HasAttr)
	}
	return q
}

func (s *GormStore) Find(f Filter) ([]Resource, error) {
	var out []Resource
	err := s.query(f).Order("service, type, region, id").Find(&out).Error
	return out, err
}

// Count returns the number of resources per service in a namespace.
func (s *GormStore) Count(namespace string) (map[string]int, error) {
	var rows []struct {
		Service string
		N       int
	}
	err := s.db.Model(&Resource{}).Select("service, count(*) as n").
		Where("namespace = ?", namespace).Group("service").Scan(&rows).Error
	out := map[string]int{}
	for _, r := range rows {
		out[r.Service] = r.N
	}
	return out, err
}

func (s *GormStore) Namespaces() (map[string]int, error) {
	var rows []struct {
		Namespace string
		N         int
	}
	err := s.db.Model(&Resource{}).Select("namespace, count(*) as n").
		Group("namespace").Scan(&rows).Error
	out := map[string]int{}
	for _, r := range rows {
		out[r.Namespace] = r.N
	}
	return out, err
}

func (s *GormStore) S3Bytes(namespace string) (int64, error) {
	var n int64
	err := s.db.Model(&Resource{}).
		Select("COALESCE(SUM((attributes->>'size')::bigint), 0)").
		Where("namespace = ? AND service = 's3' AND type LIKE 'object%' AND attributes->>'size' IS NOT NULL", namespace).
		Scan(&n).Error
	return n, err
}

func (s *GormStore) DeleteNamespace(namespace string) (int64, error) {
	res := s.db.Where("namespace = ?", namespace).Delete(&Resource{})
	return res.RowsAffected, res.Error
}

// MemStore is an in-memory Store and Inspector with the same scope rules as
// the GormStore. Tests use it; it is not meant for production.
type MemStore struct {
	mu   sync.Mutex
	rows map[string]Resource
}

func NewMemStore() *MemStore {
	return &MemStore{rows: map[string]Resource{}}
}

func memKey(ns, region, service, typ, id string) string {
	return strings.Join([]string{ns, region, service, typ, id}, "\x00")
}

func (m *MemStore) Create(res *Resource) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns, region := scopeOf(res)
	k := memKey(ns, region, res.Service, res.Type, res.ID)
	if _, ok := m.rows[k]; ok {
		return gorm.ErrDuplicatedKey
	}
	now := time.Now().UTC()
	row := *res
	row.Namespace, row.Region = ns, region
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	row.UpdatedAt = now
	m.rows[k] = row
	res.Region, res.CreatedAt, res.UpdatedAt = region, row.CreatedAt, row.UpdatedAt
	return nil
}

func (m *MemStore) Update(res *Resource) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns, region := scopeOf(res)
	k := memKey(ns, region, res.Service, res.Type, res.ID)
	row, ok := m.rows[k]
	if !ok {
		return nil // matches GormStore: updating nothing isn't an error
	}
	row.Attributes = res.Attributes
	row.UpdatedAt = time.Now().UTC()
	m.rows[k] = row
	res.UpdatedAt = row.UpdatedAt
	return nil
}

func (m *MemStore) Get(id, service, typ, namespace string) (*Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns, region := SplitScope(namespace)
	row, ok := m.rows[memKey(ns, region, service, typ, id)]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	fromRow(&row)
	return &row, nil
}

func (m *MemStore) List(service, typ, namespace string) ([]Resource, error) {
	ns, region := SplitScope(namespace)
	rows, _ := m.Find(Filter{Namespace: ns, Region: region, RegionSet: true, Service: service, Type: typ})
	for i := range rows {
		fromRow(&rows[i])
	}
	return rows, nil
}

func (m *MemStore) Delete(id, service, typ, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns, region := SplitScope(namespace)
	delete(m.rows, memKey(ns, region, service, typ, id))
	return nil
}

func (m *MemStore) Find(f Filter) ([]Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Resource
	for _, r := range m.rows {
		if (f.Namespace == "" || r.Namespace == f.Namespace) &&
			(!(f.Region != "" || f.RegionSet) || r.Region == f.Region) &&
			(f.Service == "" || r.Service == f.Service) &&
			(f.Type == "" || r.Type == f.Type) &&
			(f.ID == "" || r.ID == f.ID) &&
			(f.HasAttr == "" || hasAttr(r.Attributes, f.HasAttr)) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return memKey(a.Service, a.Type, a.Region, a.ID, "") < memKey(b.Service, b.Type, b.Region, b.ID, "")
	})
	return out, nil
}

func (m *MemStore) Count(namespace string) (map[string]int, error) {
	rows, _ := m.Find(Filter{Namespace: namespace})
	out := map[string]int{}
	for _, r := range rows {
		out[r.Service]++
	}
	return out, nil
}

func (m *MemStore) Namespaces() (map[string]int, error) {
	rows, _ := m.Find(Filter{})
	out := map[string]int{}
	for _, r := range rows {
		out[r.Namespace]++
	}
	return out, nil
}

func (m *MemStore) S3Bytes(namespace string) (int64, error) {
	rows, _ := m.Find(Filter{Namespace: namespace, Service: "s3"})
	var n int64
	for _, r := range rows {
		var attrs struct {
			Size int64 `json:"size"`
		}
		if strings.HasPrefix(r.Type, "object") && json.Unmarshal(r.Attributes, &attrs) == nil {
			n += attrs.Size
		}
	}
	return n, nil
}

func (m *MemStore) DeleteNamespace(namespace string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, r := range m.rows {
		if r.Namespace == namespace {
			delete(m.rows, k)
			n++
		}
	}
	return n, nil
}

func hasAttr(attrs []byte, key string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(attrs, &m) != nil {
		return false
	}
	v, ok := m[key]
	return ok && string(v) != "null"
}
