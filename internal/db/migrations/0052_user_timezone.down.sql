-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE ohs_core.end_users
    DROP CONSTRAINT IF EXISTS end_users_timezone_pair_check,
    DROP CONSTRAINT IF EXISTS end_users_timezone_source_check,
    DROP COLUMN IF EXISTS timezone_seen_at,
    DROP COLUMN IF EXISTS timezone_source,
    DROP COLUMN IF EXISTS timezone;
