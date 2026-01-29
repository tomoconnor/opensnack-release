-- This Source Code Form is subject to the terms of the Mozilla Public
-- License, v. 2.0. If a copy of the MPL was not distributed with this
-- file, You can obtain one at https://mozilla.org/MPL/2.0/.

-- public.resources definition

-- Drop table

-- DROP TABLE public.resources;

CREATE TABLE public.resources (
	id text NOT NULL,
	"namespace" text NOT NULL,
	service text NOT NULL,
	"type" text NOT NULL,
	"attributes" jsonb NOT NULL,
	created_at timestamp NOT NULL,
	resource_id uuid DEFAULT gen_random_uuid() NOT NULL,
	CONSTRAINT resources_pkey PRIMARY KEY (resource_id)
);
CREATE UNIQUE INDEX uniq_resource ON public.resources USING btree (id, namespace);