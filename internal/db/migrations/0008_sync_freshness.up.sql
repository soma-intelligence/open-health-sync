-- SPDX-License-Identifier: AGPL-3.0-only
-- Freshness of a connection, so the API can say how far behind its data is
-- and the scheduler can pull more often when notifications stop arriving.
--   synced_through        end of the most recent window pulled successfully;
--                         it only moves forward (a backfill of an older
--                         window does not make the data look fresher)
--   last_event_at         last provider notification for this connection
--   last_attempt_at       last pull that ran to an outcome (success or error)
--   last_error            the error of the last failed pull, cleared on success
--   consecutive_failures  failed pulls since the last success
ALTER TABLE ohs_sync.connections
    ADD COLUMN synced_through       timestamptz,
    ADD COLUMN last_event_at        timestamptz,
    ADD COLUMN last_attempt_at      timestamptz,
    ADD COLUMN last_error           text,
    ADD COLUMN consecutive_failures integer NOT NULL DEFAULT 0;

-- Existing connections: the last successful sync is the best estimate.
UPDATE ohs_sync.connections SET synced_through = last_sync_at WHERE last_sync_at IS NOT NULL;
