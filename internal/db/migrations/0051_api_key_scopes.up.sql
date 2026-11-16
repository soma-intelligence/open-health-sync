-- SPDX-License-Identifier: AGPL-3.0-only
--
-- API keys have scopes: what a key may do in its tenant (users:read, data:write, ...; the list
-- is auth.KeyScopes). '{*}' is every scope. The keys that exist get it, so they keep doing all
-- they did before this migration, and so does a key created without scopes.
ALTER TABLE ohs_core.api_keys
    ADD COLUMN scopes text[] NOT NULL DEFAULT '{*}',
    ADD CONSTRAINT api_keys_scopes_check CHECK (cardinality(scopes) > 0);
