-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE ohs_sync.connections
    DROP CONSTRAINT connections_broken_reason_check,
    DROP COLUMN rate_limited_until,
    DROP COLUMN broken_since,
    DROP COLUMN broken_reason;

DROP INDEX ohs_sync.pull_jobs_finished;
DROP INDEX ohs_sync.pull_jobs_history;
ALTER TABLE ohs_sync.pull_jobs
    DROP CONSTRAINT pull_jobs_trigger_check,
    DROP COLUMN finished_at,
    DROP COLUMN started_at,
    DROP COLUMN objects_stored,
    DROP COLUMN outcome,
    DROP COLUMN trigger;
