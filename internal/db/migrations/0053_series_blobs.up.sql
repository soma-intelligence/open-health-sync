-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Series blobs. heartbeat_samples, ecg_samples and route_points store one row
-- per point: an ECG is about 15,000 rows, a long run thousands, a day of
-- heartbeats a hundred thousand. Their compression was dropped (migration
-- 0049: a compressed chunk breaks their dedup unique), and with the vault each
-- row carries its own seal (45 bytes of nonce and tag) for 8 to 16 bytes of
-- value, so a point cost 575 to 700 bytes on disk with its four indexes.
--
-- A series is now one row: the whole series of one parent (the voltages of an
-- ECG recording, the route of a workout, the beats of a heartbeat series),
-- encoded in columns, compressed with zstd in Go, then sealed under the user's
-- data key like any sealed row (internal/vault/series, vault.Schema). Without
-- keys the compressed blob is stored as it is, in points; with keys points is
-- NULL and sealed holds it.
--
--   kind        1 ECG recording, 2 workout route, 3 heartbeat series
--   provider    the provider of the series: of the heartbeat series, or of
--               the recording or workout (a provider erasure deletes by it);
--               '' for a series converted from rows whose parent was gone
--   parent      the ecg_recordings.id or workouts.id (decimal text), or the
--               heartbeat series id
--   format      the version of the blob layout (series.Format)
--   start_at,   the first and last point time (an ECG sample's time is its
--   end_at      recording's start plus its offset), what the per-point rows
--               showed in clear; reads select series by them and retention
--               drops a series once its last point is past the cutoff
--   point_count the points in the blob, clear as the row count was
--   captured_at the newest capture time of the raw objects merged into it
--
-- The per-point tables stay, read until `ohs vault seal` has moved their rows
-- into blobs (`ohs vault status` counts what is left); nothing writes them any
-- more. A later release drops them.
--
-- A plain table, not a hypertable: a user has one row per recording, workout
-- or series, and a series that grows when a later part of it arrives would
-- have to move between chunks.

CREATE TABLE ohs_data.series_blobs (
    tenant_id   uuid        NOT NULL,
    user_id     uuid        NOT NULL,
    kind        smallint    NOT NULL CHECK (kind IN (1, 2, 3)),
    provider    text        NOT NULL DEFAULT '',
    parent      text        NOT NULL,
    format      smallint    NOT NULL CHECK (format >= 1),
    start_at    timestamptz NOT NULL,
    end_at      timestamptz NOT NULL,
    point_count integer     NOT NULL CHECK (point_count > 0),
    points      bytea,
    sealed      bytea,
    ingested_at timestamptz NOT NULL DEFAULT now(),
    captured_at timestamptz,
    PRIMARY KEY (tenant_id, user_id, kind, parent, provider),
    CONSTRAINT series_blobs_one_form CHECK ((points IS NULL) <> (sealed IS NULL))
);

-- Heartbeat reads list series by their first beat; retention looks for
-- series whose last point is old.
CREATE INDEX series_blobs_user_start ON ohs_data.series_blobs (tenant_id, user_id, kind, start_at);
CREATE INDEX series_blobs_end ON ohs_data.series_blobs (end_at);

-- The blobs are compressed already: TOAST stores them out of line without
-- trying to compress them again.
ALTER TABLE ohs_data.series_blobs ALTER COLUMN points SET STORAGE EXTERNAL;
ALTER TABLE ohs_data.series_blobs ALTER COLUMN sealed SET STORAGE EXTERNAL;
