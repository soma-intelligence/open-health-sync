-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Lab results (docs/labs.md) are stored in ohs_data.observations, one row per result, with
-- canonical_code 'lab:loinc:<code>' or 'lab:local:<code>'. Three clear columns are added:
--
--   external_id  the source's id of the result. A result sent again with the same id
--                replaces the stored one (an amended or corrected result), also when its
--                collection time moved: the writer finds the stored copy by this id.
--   report_id    the source's id of the report (the panel) the result belongs to, so the
--                results of one report are read together. NULL for a result sent alone.
--   captured_at  when the raw object a row came from was captured, the order of two copies
--                of one result (the latest wins), as on the replaceable tables (0045).
--
-- Ids and a capture time carry no value: the values, units of the reference range, flags,
-- names and every other detail are inside the sealed column (internal/vault/schema.go).
-- The columns are nullable without a default, the form a compressed hypertable accepts.
ALTER TABLE ohs_data.observations
    ADD COLUMN IF NOT EXISTS external_id text,
    ADD COLUMN IF NOT EXISTS report_id   text,
    ADD COLUMN IF NOT EXISTS captured_at timestamptz;

CREATE INDEX IF NOT EXISTS observations_user_external
    ON ohs_data.observations (tenant_id, user_id, provider, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observations_user_report
    ON ohs_data.observations (tenant_id, user_id, report_id) WHERE report_id IS NOT NULL;
