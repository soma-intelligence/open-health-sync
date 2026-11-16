-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Health data vault, release N schema (docs/specs/vault.md). This migration only
-- adds structure and fills the promoted clear columns from what is stored today.
-- No row is sealed here: the sealed and content_mac columns stay NULL until the
-- seal command (and the ETL writer, once a user has keys) fills them, which is
-- why every sensitive column loses its NOT NULL below.
--
-- Cost on a large instance: the backfill is a plain UPDATE of quantity_samples
-- inside this migration's transaction (the runner applies one file per
-- transaction), so every row is rewritten once. Compressed chunks are
-- decompressed batch by batch by TimescaleDB while it runs. Expect it to take
-- as long as a full rewrite of the table, and to need the free disk space of
-- the uncompressed table; run it in a maintenance window on a big instance and
-- recompress afterwards (the policy does it within a day).
--
-- It applies on top of migrations 0045 to 0049. captured_at (0045), the
-- capture time that orders the copies of a replaceable row, is left as it is:
-- a clear column the writer compares in SQL, on sealed rows too. The hourly
-- table below is a hypertable of its own and takes no part in the chunk
-- intervals of 0049.

-- ===========================================================================
-- Keys
-- ===========================================================================

-- Root key versions the instance has used. The key itself never enters the
-- database; provider names where it lives (local, a KMS, transit).
CREATE TABLE ohs_core.vault_root (
    version    text        PRIMARY KEY,
    provider   text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz
);

-- Wrapped keys. A tenant key (user_id NULL) is wrapped by a root key version, a
-- user key by a tenant key version; parent_version says which one, as text (a
-- root version is a name, a tenant key version is a decimal number).
-- kem_public holds the ML-KEM-1024 and X25519 public keys of a user key, in
-- clear, and is NULL on a tenant key.
CREATE TABLE ohs_core.vault_keys (
    tenant_id      uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    user_id        uuid,
    version        bigint      NOT NULL CHECK (version >= 1),
    wrapped        bytea       NOT NULL,
    parent_version text        NOT NULL,
    kem_public     bytea,
    created_at     timestamptz NOT NULL DEFAULT now(),
    retired_at     timestamptz,
    CONSTRAINT vault_keys_user_fkey FOREIGN KEY (tenant_id, user_id)
        REFERENCES ohs_core.end_users (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT vault_keys_kem_check CHECK ((user_id IS NULL) = (kem_public IS NULL))
);
-- A primary key cannot hold a NULL user_id, so uniqueness is two partial indexes.
CREATE UNIQUE INDEX vault_keys_tenant_version ON ohs_core.vault_keys (tenant_id, version)
    WHERE user_id IS NULL;
CREATE UNIQUE INDEX vault_keys_user_version ON ohs_core.vault_keys (tenant_id, user_id, version)
    WHERE user_id IS NOT NULL;

-- ===========================================================================
-- Columns of the sealed tables (internal/vault/schema.go is the list)
-- ===========================================================================

ALTER TABLE ohs_data.quantity_samples
    ADD COLUMN sealed      bytea,
    ADD COLUMN content_mac bytea,
    ADD COLUMN source_key  bytea,
    ADD COLUMN worn        boolean,
    ADD COLUMN tz          text,
    ADD COLUMN derived     boolean,
    ADD COLUMN synthesized boolean,
    ADD COLUMN summary_date date;
ALTER TABLE ohs_data.category_samples  ADD COLUMN sealed bytea, ADD COLUMN tz text;
ALTER TABLE ohs_data.heartbeat_samples ADD COLUMN sealed bytea;
ALTER TABLE ohs_data.ecg_recordings    ADD COLUMN sealed bytea;
ALTER TABLE ohs_data.ecg_samples       ADD COLUMN sealed bytea;
ALTER TABLE ohs_data.sleep_sessions
    ADD COLUMN sealed bytea, ADD COLUMN content_mac bytea, ADD COLUMN source_key bytea,
    ADD COLUMN worn boolean, ADD COLUMN synthesized boolean;
ALTER TABLE ohs_data.sleep_phases      ADD COLUMN sealed bytea;
ALTER TABLE ohs_data.workouts
    ADD COLUMN sealed bytea, ADD COLUMN content_mac bytea, ADD COLUMN source_key bytea,
    ADD COLUMN worn boolean;
ALTER TABLE ohs_data.workout_tracks    ADD COLUMN sealed bytea;
ALTER TABLE ohs_data.route_points      ADD COLUMN sealed bytea;
ALTER TABLE ohs_data.nutrition_logs    ADD COLUMN sealed bytea, ADD COLUMN content_mac bytea;
ALTER TABLE ohs_data.observations      ADD COLUMN sealed bytea;
ALTER TABLE ohs_data.daily_summaries
    ADD COLUMN sealed bytea, ADD COLUMN content_mac bytea, ADD COLUMN derived boolean;
ALTER TABLE ohs_data.time_tags         ADD COLUMN sealed bytea;
ALTER TABLE ohs_data.failed_records    ADD COLUMN sealed bytea;

-- Sealed values become NULL in their columns once a row is sealed, so the
-- columns that were NOT NULL stop being so. Defaults stay as they are.
ALTER TABLE ohs_data.quantity_samples ALTER COLUMN value DROP NOT NULL, ALTER COLUMN metadata DROP NOT NULL;
ALTER TABLE ohs_data.category_samples ALTER COLUMN metadata DROP NOT NULL;
ALTER TABLE ohs_data.heartbeat_samples ALTER COLUMN rr_ms DROP NOT NULL;
ALTER TABLE ohs_data.ecg_recordings
    ALTER COLUMN classification DROP NOT NULL,
    ALTER COLUMN symptoms_status DROP NOT NULL,
    ALTER COLUMN number_of_measurements DROP NOT NULL,
    ALTER COLUMN sampling_frequency DROP NOT NULL,
    ALTER COLUMN metadata DROP NOT NULL;
ALTER TABLE ohs_data.ecg_samples
    ALTER COLUMN time_since_sample_start DROP NOT NULL,
    ALTER COLUMN voltage_uv DROP NOT NULL;
ALTER TABLE ohs_data.sleep_sessions ALTER COLUMN metadata DROP NOT NULL;
ALTER TABLE ohs_data.sleep_phases
    ALTER COLUMN phase DROP NOT NULL,
    ALTER COLUMN duration_s DROP NOT NULL;
ALTER TABLE ohs_data.workouts ALTER COLUMN metadata DROP NOT NULL;
ALTER TABLE ohs_data.workout_tracks
    ALTER COLUMN length_m DROP NOT NULL,
    ALTER COLUMN point_count DROP NOT NULL;
ALTER TABLE ohs_data.route_points
    ALTER COLUMN latitude DROP NOT NULL,
    ALTER COLUMN longitude DROP NOT NULL;
ALTER TABLE ohs_data.nutrition_logs ALTER COLUMN metadata DROP NOT NULL;
ALTER TABLE ohs_data.observations ALTER COLUMN metadata DROP NOT NULL;
ALTER TABLE ohs_data.daily_summaries ALTER COLUMN metadata DROP NOT NULL;
ALTER TABLE ohs_data.time_tags
    ALTER COLUMN kind DROP NOT NULL,
    ALTER COLUMN source DROP NOT NULL;

-- ===========================================================================
-- Backfill of the promoted columns, from the clear columns, in SQL
-- ===========================================================================

-- worn: true when the record names a watch, the test the merged view and the
-- hourly aggregate apply today (api/read/merge.go quantityFamily.wear, and
-- migration 0035, where the same test gives rank 0). tz, derived and
-- synthesized are the metadata keys the ETL reads today (etl.MetadataTimezone,
-- writer_deletions.go, sleep_sessionize.go).
-- source_key is a keyed HMAC of the source under the user's index key, which SQL
-- cannot compute: it stays NULL here and the seal command fills it.
-- sleep sessions and workouts have no source columns of their own: the merged
-- view reads the app that wrote them from metadata (parentWear, merge.go), so
-- worn is metadata->>'sourceName' ILIKE '%watch%' there. daily_summaries.derived
-- keeps the "IS DISTINCT FROM 'true'" meaning of writer_deletions.go: a row with
-- no flag is not derived. summary_date of a quantity sample is the day its
-- metadata names (model.MetadataSummaryDate); a value that is not a date gives
-- NULL instead of failing the migration.
-- daily_summaries.summary_date is a column of the table already.
CREATE FUNCTION pg_temp.vault_try_date(v text) RETURNS date
LANGUAGE plpgsql IMMUTABLE AS $f$
BEGIN
    IF v !~ '^\d{4}-\d{2}-\d{2}$' THEN
        RETURN NULL;
    END IF;
    RETURN v::date;
EXCEPTION WHEN others THEN
    RETURN NULL;
END $f$;

UPDATE ohs_data.quantity_samples SET
    worn         = concat_ws(' ', product_type, device_model, source_name) ILIKE '%watch%',
    tz           = metadata->>'timezone',
    derived      = coalesce(metadata->>'derived' = 'true', false),
    synthesized  = coalesce(metadata->>'synthesized' = 'true', false),
    summary_date = pg_temp.vault_try_date(metadata->>'summary_date');

UPDATE ohs_data.category_samples SET tz = metadata->>'timezone';

UPDATE ohs_data.sleep_sessions SET
    worn        = coalesce(metadata->>'sourceName' ILIKE '%watch%', false),
    synthesized = coalesce(metadata->>'synthesized' = 'true', false);

UPDATE ohs_data.workouts SET worn = coalesce(metadata->>'sourceName' ILIKE '%watch%', false);

UPDATE ohs_data.daily_summaries SET derived = coalesce(metadata->>'derived' = 'true', false);

DROP FUNCTION pg_temp.vault_try_date(text);

-- ===========================================================================
-- Hourly statistics, sealed
-- ===========================================================================

-- Replaces the continuous aggregate ohs_data.quantity_hourly, which stays in
-- this release for the users who are not sealed yet. The group keys are in
-- clear (source_key is the keyed HMAC of the source); n, sum, min and max are
-- sealed together. unit is NOT NULL because it is part of the key: the ETL
-- writes '' where a sample has no unit.
CREATE TABLE ohs_data.quantity_hourly_sealed (
    tenant_id     uuid        NOT NULL,
    user_id       uuid        NOT NULL,
    quantity_type text        NOT NULL,
    provider      text        NOT NULL,
    source_key    bytea       NOT NULL,
    slug          text        NOT NULL,
    worn          boolean     NOT NULL,
    unit          text        NOT NULL,
    bucket        timestamptz NOT NULL,
    sealed        bytea       NOT NULL,
    content_mac   bytea,
    PRIMARY KEY (tenant_id, user_id, quantity_type, provider, source_key, slug, worn, unit, bucket)
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable('ohs_data.quantity_hourly_sealed', by_range('bucket', INTERVAL '90 days'));
    END IF;
END $$;
