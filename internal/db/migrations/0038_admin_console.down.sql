-- SPDX-License-Identifier: AGPL-3.0-only
DROP INDEX ohs_sync.etl_jobs_tenant_parked;
DROP INDEX ohs_sync.pull_jobs_tenant_parked;
DROP TABLE ohs_core.provider_settings;
ALTER TABLE ohs_core.tenants DROP COLUMN admin;
