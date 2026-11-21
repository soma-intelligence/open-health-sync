-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Indexes behind the fair claims and the scheduler's due query.

-- The pull claim reads each provider's due jobs on their own, at most the
-- provider's free slots per priority, so a provider with a backlog past its
-- cap cannot hide the jobs of the others (internal/queue/pull.go).
--
-- The index the claim used before, pull_jobs_claimable, stays for now: during
-- a rolling upgrade the instances of the previous release still claim through
-- it, and without it each of their claims would sort every pending job. A
-- later release drops it.
--
-- The build is a plain CREATE INDEX, as in every migration before this one:
-- the migration runner sends a file as one multi-statement query, which runs
-- in one transaction, and CREATE INDEX CONCURRENTLY cannot run in one. It
-- holds a SHARE lock on pull_jobs while it reads the table, so enqueues and
-- claims wait for it (the index itself only holds the pending jobs).
CREATE INDEX IF NOT EXISTS pull_jobs_claim ON ohs_sync.pull_jobs (provider, priority, not_before, id) WHERE status = 'pending';

-- schedule_after is the earliest time a regular pull of the connection can
-- become due: the scheduler sets it to the end of the connection's current
-- slot (or to when its notifications will count as silent, if sooner) each
-- time it looks at the connection, and only looks at the connections past
-- it. Without it every tick checked every active connection for a slot job.
-- It is a hint, never the reason a pull is queued: a connection past it is
-- still checked against its slot and its jobs. Existing connections start
-- due, which costs one full pass, the cost every tick had before.
ALTER TABLE ohs_sync.connections
    ADD COLUMN schedule_after timestamptz NOT NULL DEFAULT '-infinity';
CREATE INDEX connections_schedule ON ohs_sync.connections (schedule_after, tenant_id, user_id, provider)
    WHERE status = 'active';
