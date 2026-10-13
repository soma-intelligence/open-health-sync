-- SPDX-License-Identifier: AGPL-3.0-only
-- Sync runs and connection health.
--
-- A pull job is a sync run: the API lists them as the sync history of a
-- connection and can start one on demand ("sync now").
--   trigger         what started the run: backfill, scheduled, notification
--                   or manual (POST .../sync)
--   outcome         how the last attempt ended: completed, failed,
--                   rate_limited, needs_reauth, missing_scope or inactive
--                   (NULL until the first attempt ends)
--   objects_stored  raw objects the last attempt stored
--   started_at      when the last attempt was claimed
--   finished_at     when the run reached done or parked
--
-- The health of a connection is what a developer shows the user ("reconnect
-- your ring"). broken_reason is set when the connection breaks (needs_reauth,
-- missing_scope, failing, rate_limited) and cleared when it syncs again; each
-- change announces a connection.broken or connection.restored webhook.
ALTER TABLE ohs_sync.pull_jobs
    ADD COLUMN trigger        text NOT NULL DEFAULT 'scheduled',
    ADD COLUMN outcome        text,
    ADD COLUMN objects_stored integer,
    ADD COLUMN started_at     timestamptz,
    ADD COLUMN finished_at    timestamptz,
    ADD CONSTRAINT pull_jobs_trigger_check CHECK (trigger IN ('backfill', 'scheduled', 'notification', 'manual'));

-- Existing rows: notification pulls are the priority 0 ones.
UPDATE ohs_sync.pull_jobs SET trigger = 'notification' WHERE priority = 0;
UPDATE ohs_sync.pull_jobs SET finished_at = created_at WHERE status IN ('done', 'parked');

-- The sync history of a connection, newest first.
CREATE INDEX pull_jobs_history ON ohs_sync.pull_jobs (tenant_id, user_id, provider, id DESC);
-- Finished runs are pruned after RunRetention.
CREATE INDEX pull_jobs_finished ON ohs_sync.pull_jobs (created_at) WHERE status IN ('done', 'parked');

ALTER TABLE ohs_sync.connections
    ADD COLUMN broken_reason      text,
    ADD COLUMN broken_since       timestamptz,
    ADD COLUMN rate_limited_until timestamptz,
    ADD CONSTRAINT connections_broken_reason_check
        CHECK (broken_reason IN ('needs_reauth', 'missing_scope', 'failing', 'rate_limited'));

UPDATE ohs_sync.connections SET broken_reason = 'needs_reauth', broken_since = updated_at
WHERE status = 'needs_reauth';
