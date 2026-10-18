-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A provider turned off, or back on, for one tenant by an instance admin
-- (PATCH /v1/admin/tenants/{tenant_id}/providers/{provider}, docs/admin-api.md).
-- No row means on: a tenant uses every provider the instance runs until a
-- row says otherwise. While a provider is off for a tenant, its users cannot
-- connect it, sync it or upload to it, and no pull runs for it; the data
-- already stored stays readable and the connections are kept, so they resume
-- when it is turned back on. updated_by is the id of the admin key that saved
-- the row. The rows go with their tenant.
CREATE TABLE ohs_core.tenant_providers (
    tenant_id  uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    provider   text        NOT NULL,
    enabled    boolean     NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text,
    PRIMARY KEY (tenant_id, provider),
    CONSTRAINT tenant_providers_provider_check CHECK (provider ~ '^[a-z][a-z0-9_]{0,63}$')
);

