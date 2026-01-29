// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package resource

import "gorm.io/gorm"

type GormStore struct {
	db *gorm.DB
}

func NewGormStore(db *gorm.DB) *GormStore {
	return &GormStore{db}
}

func (s *GormStore) Create(res *Resource) error {
	return s.db.Create(res).Error
}

func (s *GormStore) Update(res *Resource) error {
	// Use explicit WHERE clause for composite primary key (id, namespace)
	return s.db.Model(&Resource{}).
		Where("id = ? AND namespace = ?", res.ID, res.Namespace).
		Updates(res).Error
}

func (s *GormStore) Get(id, service, typ, namespace string) (*Resource, error) {
	var r Resource
	err := s.db.Where("id = ? AND service = ? AND type = ? AND namespace = ?",
		id, service, typ, namespace).First(&r).Error
	return &r, err
}

func (s *GormStore) List(service, typ, namespace string) ([]Resource, error) {
	var out []Resource
	err := s.db.Where("service = ? AND type = ? AND namespace = ?",
		service, typ, namespace).Find(&out).Error
	return out, err
}

func (s *GormStore) Delete(id, service, typ, namespace string) error {
	return s.db.Where("id = ? AND service = ? AND type = ? AND namespace = ?",
		id, service, typ, namespace).Delete(&Resource{}).Error
}
