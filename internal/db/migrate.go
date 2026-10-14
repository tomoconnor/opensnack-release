// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"fmt"

	"gorm.io/gorm"
)

// migrations bring any earlier schema (including an empty database) up to
// date. Every statement is idempotent, so they all run on every start.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS public.resources (
		id text NOT NULL,
		"namespace" text NOT NULL,
		service text NOT NULL,
		"type" text NOT NULL,
		"attributes" jsonb NOT NULL,
		created_at timestamp NOT NULL,
		resource_id uuid DEFAULT gen_random_uuid() NOT NULL,
		CONSTRAINT resources_pkey PRIMARY KEY (resource_id)
	)`,

	// Regions. Global services (IAM, Route 53, S3, STS, CloudFront) keep "".
	`ALTER TABLE public.resources ADD COLUMN IF NOT EXISTS region text NOT NULL DEFAULT ''`,
	// Rows written before regions existed were all us-east-1.
	`UPDATE public.resources SET region = 'us-east-1'
	 WHERE region = '' AND service NOT IN ('iam', 'route53', 's3', 's3control', 'sts', 'cloudfront')`,

	// updated_at, so the inspection API can show when a resource changed.
	`ALTER TABLE public.resources ADD COLUMN IF NOT EXISTS updated_at timestamp`,
	`UPDATE public.resources SET updated_at = created_at WHERE updated_at IS NULL`,

	// The old (id, namespace) key let a queue and a topic with the same name
	// collide. A resource is unique within its namespace, region, service
	// and type.
	`DROP INDEX IF EXISTS public.uniq_resource`,
	`CREATE UNIQUE INDEX IF NOT EXISTS uniq_resource_scoped
	 ON public.resources USING btree (namespace, region, service, "type", id)`,
}

func Migrate(g *gorm.DB) error {
	for i, stmt := range migrations {
		if err := g.Exec(stmt).Error; err != nil {
			return fmt.Errorf("migration %d: %w", i, err)
		}
	}
	return nil
}
