-- SPDX-License-Identifier: AGPL-3.0-only
-- Every key is back to full access, the keys created with fewer scopes included: revoke those
-- first if that matters.
ALTER TABLE ohs_core.api_keys
    DROP CONSTRAINT IF EXISTS api_keys_scopes_check,
    DROP COLUMN IF EXISTS scopes;
