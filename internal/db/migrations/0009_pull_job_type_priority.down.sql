-- SPDX-License-Identifier: AGPL-3.0-only
DROP INDEX ohs_sync.pull_jobs_claimable;
CREATE INDEX pull_jobs_claimable ON ohs_sync.pull_jobs (not_before, id) WHERE status = 'pending';
-- Typed jobs would clash under the old key: keep one per window.
DELETE FROM ohs_sync.pull_jobs WHERE data_type IS NOT NULL AND status IN ('pending', 'running');
DROP INDEX ohs_sync.pull_jobs_idempotent;
CREATE UNIQUE INDEX pull_jobs_idempotent ON ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time)
    WHERE status IN ('pending', 'running');
ALTER TABLE ohs_sync.pull_jobs DROP COLUMN data_type, DROP COLUMN priority;
