-- SPDX-License-Identifier: AGPL-3.0-only
--
-- API keys may expire: expires_at is set when a key is created and left NULL for a key that
-- lives until it is revoked. The lookup refuses a key past it.
ALTER TABLE ohs_core.api_keys ADD COLUMN expires_at timestamptz;

-- A user token remembers the API key that minted it, so revoking the key revokes the tokens it
-- minted. NULL for the tokens minted before this migration: they live until they expire or are
-- revoked one by one.
ALTER TABLE ohs_core.user_tokens
    ADD COLUMN minted_by_key uuid REFERENCES ohs_core.api_keys (id) ON DELETE SET NULL;
CREATE INDEX user_tokens_minted_by_key ON ohs_core.user_tokens (minted_by_key)
    WHERE minted_by_key IS NOT NULL;
