-- SPDX-License-Identifier: AGPL-3.0-only
DROP INDEX IF EXISTS ohs_sync.connections_schedule;
ALTER TABLE ohs_sync.connections DROP COLUMN IF EXISTS schedule_after;
DROP INDEX IF EXISTS ohs_sync.pull_jobs_claim;
