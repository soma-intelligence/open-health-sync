-- SPDX-License-Identifier: AGPL-3.0-only
-- The names of the keys are lost; the keys themselves keep working.
ALTER TABLE ohs_core.api_keys
    DROP CONSTRAINT IF EXISTS api_keys_name_check,
    DROP COLUMN IF EXISTS name;
