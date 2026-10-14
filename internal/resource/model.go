// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package resource

import (
	"strings"
	"time"
)

type Resource struct {
	ID string `gorm:"primaryKey"`
	// Namespace is the scope the handler passed in: "ns" for global
	// services, "ns@region" for regional ones. The GormStore stores the two
	// halves in separate columns and joins them again on the way out, so
	// handlers can round-trip a resource without knowing about regions.
	Namespace string `gorm:"primaryKey"`
	// Region is "" for global services.
	Region     string `gorm:"not null;default:''"`
	Service    string `gorm:"index; not null"`
	Type       string `gorm:"index; not null"`
	Attributes []byte `gorm:"type:jsonb; not null"`
	// CreatedAt is set once on create and never changes, so a checker can tell
	// an in-place update from a replacement.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SplitScope separates a store scope into its namespace and region. A scope
// without a region (global services) returns region "".
func SplitScope(scope string) (namespace, region string) {
	if i := strings.LastIndexByte(scope, '@'); i >= 0 {
		return scope[:i], scope[i+1:]
	}
	return scope, ""
}

// JoinScope is the inverse of SplitScope.
func JoinScope(namespace, region string) string {
	if region == "" {
		return namespace
	}
	return namespace + "@" + region
}
