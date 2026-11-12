-- SPDX-License-Identifier: AGPL-3.0-only
DROP INDEX IF EXISTS ohs_core.user_tokens_minted_by_key;
ALTER TABLE ohs_core.user_tokens DROP COLUMN IF EXISTS minted_by_key;
ALTER TABLE ohs_core.api_keys DROP COLUMN IF EXISTS expires_at;
