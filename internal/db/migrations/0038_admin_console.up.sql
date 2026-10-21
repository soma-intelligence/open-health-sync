-- SPDX-License-Identifier: AGPL-3.0-only
--
-- The admin console (docs/console.md). tenants.admin marks the instance
-- admin tenant: its API keys may change instance-wide settings through the
-- /v1/admin routes. The only tenant of an instance that has exactly one is
-- promoted, so a self-hosted instance keeps a working console on upgrade; an
-- instance with several tenants gets none until the operator names one
-- (ohs admin tenant admin).
--
-- provider_settings holds what the console saved for a provider: its keys as
-- one JSON object sealed with OHS_ENCRYPTION_KEY (bound to the slug, NULL
-- when none was saved) and whether it is on. A provider's
-- OHS_<PROVIDER>_* variables, when any is set, replace the stored keys;
-- enabled applies either way. Instance-wide, so in ohs_core with no tenant.
ALTER TABLE ohs_core.tenants ADD COLUMN admin boolean NOT NULL DEFAULT false;

UPDATE ohs_core.tenants SET admin = true
WHERE (SELECT count(*) FROM ohs_core.tenants) = 1;

CREATE TABLE ohs_core.provider_settings (
    slug        text        PRIMARY KEY,
    keys_sealed bytea,
    enabled     boolean     NOT NULL DEFAULT true,
    updated_by  text        NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_settings_slug_check CHECK (slug ~ '^[a-z][a-z0-9_]{0,63}$')
);

-- The console lists a tenant's failed sync runs and parked processing jobs,
-- newest first. Parked rows are few, so the indexes stay small.
CREATE INDEX pull_jobs_tenant_parked ON ohs_sync.pull_jobs (tenant_id, id DESC) WHERE status = 'parked';
CREATE INDEX etl_jobs_tenant_parked ON ohs_sync.etl_jobs (tenant_id, id DESC) WHERE status = 'parked';
