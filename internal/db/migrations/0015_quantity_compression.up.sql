-- SPDX-License-Identifier: AGPL-3.0-only
-- quantity_samples is the largest table by far. Compress chunks older than
-- 30 days (about 7 times smaller on disk). A compressed chunk checks a unique
-- index by decompressing whole batches, so the dedup unique goes: the ETL
-- writer dedups against the stored rows of each batch's time window, under a
-- per-user advisory lock, using quantity_samples_user_date.
ALTER TABLE ohs_data.quantity_samples DROP CONSTRAINT quantity_samples_dedup_key;

-- Compression is a Timescale License feature: an Apache-only build of
-- TimescaleDB keeps the table uncompressed, which works the same, only larger.
DO $$
BEGIN
    IF current_setting('timescaledb.license', true) = 'timescale' THEN
        ALTER TABLE ohs_data.quantity_samples SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'tenant_id, user_id',
            timescaledb.compress_orderby = 'start_date DESC, id DESC'
        );
        PERFORM add_compression_policy('ohs_data.quantity_samples', INTERVAL '30 days');
    END IF;
END $$;
