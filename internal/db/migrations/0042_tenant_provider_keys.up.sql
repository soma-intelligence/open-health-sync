-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A tenant's own keys for a provider (PUT
-- /v1/admin/tenants/{tenant_id}/providers/{provider}/keys, docs/admin-api.md).
-- While a row is set, the tenant's users connect through the tenant's own
-- OAuth application instead of the instance's keys; the instance's keys keep
-- serving every other tenant. keys_sealed is one JSON object of the keys by
-- FIELD, sealed with OHS_ENCRYPTION_KEY and bound to the tenant and the
-- provider, like ohs_core.provider_settings. keys_id names the OAuth
-- application: it stays the same when a secret is rotated and changes when
-- the client id does, since grants issued to one application do not refresh
-- with another. created_by and updated_by are ids of the admin keys that
-- saved the row. The rows go with their tenant.
CREATE TABLE ohs_core.tenant_provider_keys (
    tenant_id   uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    provider    text        NOT NULL,
    keys_id     uuid        NOT NULL,
    keys_sealed bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text        NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  text        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, provider),
    CONSTRAINT tenant_provider_keys_keys_id_key UNIQUE (keys_id),
    CONSTRAINT tenant_provider_keys_provider_check CHECK (provider ~ '^[a-z][a-z0-9_]{0,63}$')
);

-- The keys a connection was made with: NULL for the instance's keys, else
-- the keys_id of the tenant's keys. Refreshes, pulls, notifications and
-- revocation use the same keys for the life of the grant. The column is not a
-- foreign key: when the tenant's keys are removed, or replaced with another
-- client id, the connections made with them are set needs_reauth and keep the
-- id, so nothing calls the provider with keys the grant was not issued to.
ALTER TABLE ohs_sync.connections ADD COLUMN tenant_keys_id uuid;

CREATE INDEX connections_tenant_keys ON ohs_sync.connections (tenant_keys_id)
    WHERE tenant_keys_id IS NOT NULL;

-- The keys a consent was started with, so the callback trades the code with
-- the same application.
ALTER TABLE ohs_sync.oauth_states ADD COLUMN tenant_keys_id uuid;
