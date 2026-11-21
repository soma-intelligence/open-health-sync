-- SPDX-License-Identifier: AGPL-3.0-only
--
-- One row per running process, refreshed by that process on a timer, so the
-- console can show which processes run, which settings revision each has
-- loaded and which need a restart.
--
-- process_id is chosen by the process at start. host and roles say where it
-- runs and what it serves; version is the build. started_at is the process
-- start. start_revision is the settings revision it started with and
-- live_revision the one it applies now. skipped lists the stored settings it
-- ignored because they failed validation, and reload_error the last live
-- reload failure, NULL when the last reload worked. seen_at is the last beat;
-- the console drops a row that has not been seen for a while.
CREATE TABLE ohs_core.process_heartbeats (
    process_id     uuid        PRIMARY KEY,
    host           text        NOT NULL,
    roles          text[]      NOT NULL,
    version        text        NOT NULL,
    started_at     timestamptz NOT NULL,
    start_revision bigint      NOT NULL,
    live_revision  bigint      NOT NULL,
    skipped        text[]      NOT NULL DEFAULT '{}',
    reload_error   text,
    seen_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX process_heartbeats_seen_at ON ohs_core.process_heartbeats (seen_at);
