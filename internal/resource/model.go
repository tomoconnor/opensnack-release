// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package resource

import "time"

type Resource struct {
	ID         string `gorm:"primaryKey"`
	Namespace  string `gorm:"primaryKey"`
	Service    string `gorm:"index; not null"`
	Type       string `gorm:"index; not null"`
	Attributes []byte `gorm:"type:jsonb; not null"`
	CreatedAt  time.Time
}
