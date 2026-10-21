-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE ohs_sync.oauth_states DROP COLUMN tenant_keys_id;
DROP INDEX ohs_sync.connections_tenant_keys;
ALTER TABLE ohs_sync.connections DROP COLUMN tenant_keys_id;
DROP TABLE ohs_core.tenant_provider_keys;
