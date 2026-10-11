-- SPDX-License-Identifier: AGPL-3.0-only
-- A pull job can name one data type (a provider notification names what
-- changed): NULL pulls every type. Notification pulls get priority 0 and are
-- claimed before backstop and backfill pulls (priority 1), so a burst of slot
-- jobs or a rate limit never queues fresh data behind routine work.
ALTER TABLE ohs_sync.pull_jobs
    ADD COLUMN data_type text,
    ADD COLUMN priority  smallint NOT NULL DEFAULT 1;

DROP INDEX ohs_sync.pull_jobs_idempotent;
CREATE UNIQUE INDEX pull_jobs_idempotent ON ohs_sync.pull_jobs
    (tenant_id, user_id, provider, from_time, to_time, (coalesce(data_type, '')))
    WHERE status IN ('pending', 'running');

DROP INDEX ohs_sync.pull_jobs_claimable;
CREATE INDEX pull_jobs_claimable ON ohs_sync.pull_jobs (priority, not_before, id) WHERE status = 'pending';
