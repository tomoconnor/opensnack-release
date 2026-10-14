-- This Source Code Form is subject to the terms of the Mozilla Public
-- License, v. 2.0. If a copy of the MPL was not distributed with this
-- file, You can obtain one at https://mozilla.org/MPL/2.0/.

-- public.resources definition. opensnack also applies internal/db/migrate.go
-- on every start, which creates or upgrades this table, so this file only
-- matters for tools that read the schema directly.

CREATE TABLE public.resources (
	id text NOT NULL,
	"namespace" text NOT NULL,
	region text NOT NULL DEFAULT '',
	service text NOT NULL,
	"type" text NOT NULL,
	"attributes" jsonb NOT NULL,
	created_at timestamp NOT NULL,
	updated_at timestamp,
	resource_id uuid DEFAULT gen_random_uuid() NOT NULL,
	CONSTRAINT resources_pkey PRIMARY KEY (resource_id)
);
CREATE UNIQUE INDEX uniq_resource_scoped ON public.resources USING btree (namespace, region, service, "type", id);
