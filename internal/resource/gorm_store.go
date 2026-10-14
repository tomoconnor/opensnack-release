// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package resource

import (
	"time"

	"gorm.io/gorm"
)

type GormStore struct {
	db *gorm.DB
}

func NewGormStore(db *gorm.DB) *GormStore {
	return &GormStore{db}
}

// DB exposes the connection for the internal inspection API.
func (s *GormStore) DB() *gorm.DB {
	return s.db
}

// scopeOf returns the namespace and region columns for res. A resource read
// back from the store carries both, so either form round-trips.
func scopeOf(res *Resource) (string, string) {
	ns, region := SplitScope(res.Namespace)
	if region == "" {
		region = res.Region
	}
	return ns, region
}

// fromRow restores the scope string handlers expect.
func fromRow(r *Resource) {
	r.Namespace = JoinScope(r.Namespace, r.Region)
}

func (s *GormStore) Create(res *Resource) error {
	ns, region := scopeOf(res)
	now := time.Now().UTC()
	row := *res
	row.Namespace, row.Region = ns, region
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	row.UpdatedAt = now
	if err := s.db.Create(&row).Error; err != nil {
		return err
	}
	res.Region, res.CreatedAt, res.UpdatedAt = region, row.CreatedAt, row.UpdatedAt
	return nil
}

// Update replaces a resource's attributes. CreatedAt is left alone.
func (s *GormStore) Update(res *Resource) error {
	ns, region := scopeOf(res)
	now := time.Now().UTC()
	q := s.db.Model(&Resource{}).
		Where("id = ? AND namespace = ? AND region = ?", res.ID, ns, region)
	if res.Service != "" {
		q = q.Where("service = ?", res.Service)
	}
	if res.Type != "" {
		q = q.Where("type = ?", res.Type)
	}
	err := q.Updates(map[string]any{
		"attributes": res.Attributes,
		"updated_at": now,
	}).Error
	if err == nil {
		res.UpdatedAt = now
	}
	return err
}

func (s *GormStore) Get(id, service, typ, namespace string) (*Resource, error) {
	ns, region := SplitScope(namespace)
	var r Resource
	err := s.db.Where("id = ? AND service = ? AND type = ? AND namespace = ? AND region = ?",
		id, service, typ, ns, region).First(&r).Error
	fromRow(&r)
	return &r, err
}

func (s *GormStore) List(service, typ, namespace string) ([]Resource, error) {
	ns, region := SplitScope(namespace)
	var out []Resource
	err := s.db.Where("service = ? AND type = ? AND namespace = ? AND region = ?",
		service, typ, ns, region).Order("id").Find(&out).Error
	for i := range out {
		fromRow(&out[i])
	}
	return out, err
}

func (s *GormStore) Delete(id, service, typ, namespace string) error {
	ns, region := SplitScope(namespace)
	return s.db.Where("id = ? AND service = ? AND type = ? AND namespace = ? AND region = ?",
		id, service, typ, ns, region).Delete(&Resource{}).Error
}
