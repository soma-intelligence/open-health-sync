-- SPDX-License-Identifier: AGPL-3.0-only
--
-- API keys have a name: what the key is for ("Production backend", "CI"), so a list of
-- keys reads by purpose and not only by prefix. Nullable: the keys that exist have none,
-- and the API and `ohs admin key` still create a key without one. When set it is 1 to
-- 100 characters, trimmed by the API.
ALTER TABLE ohs_core.api_keys
    ADD COLUMN name text,
    ADD CONSTRAINT api_keys_name_check CHECK (name IS NULL OR char_length(name) BETWEEN 1 AND 100);
