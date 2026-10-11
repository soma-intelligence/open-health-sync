-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE ohs_sync.connections
    DROP COLUMN synced_through,
    DROP COLUMN last_event_at,
    DROP COLUMN last_attempt_at,
    DROP COLUMN last_error,
    DROP COLUMN consecutive_failures;
