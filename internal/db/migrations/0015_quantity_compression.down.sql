-- SPDX-License-Identifier: AGPL-3.0-only
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM timescaledb_information.jobs
               WHERE proc_name = 'policy_compression'
                 AND hypertable_schema = 'ohs_data' AND hypertable_name = 'quantity_samples') THEN
        PERFORM remove_compression_policy('ohs_data.quantity_samples');
    END IF;
    IF EXISTS (SELECT 1 FROM timescaledb_information.hypertables
               WHERE hypertable_schema = 'ohs_data' AND hypertable_name = 'quantity_samples'
                 AND compression_enabled) THEN
        PERFORM decompress_chunk(c, true) FROM show_chunks('ohs_data.quantity_samples') c;
        ALTER TABLE ohs_data.quantity_samples SET (timescaledb.compress = false);
    END IF;
END $$;

-- Without the unique, nothing stopped a duplicate written outside the ETL:
-- keep the oldest copy so the constraint can come back.
DELETE FROM ohs_data.quantity_samples a
USING ohs_data.quantity_samples b
WHERE a.tenant_id = b.tenant_id AND a.user_id = b.user_id
  AND a.content_hash = b.content_hash AND a.start_date = b.start_date
  AND a.id > b.id;

ALTER TABLE ohs_data.quantity_samples
    ADD CONSTRAINT quantity_samples_dedup_key UNIQUE (tenant_id, user_id, content_hash, start_date);
