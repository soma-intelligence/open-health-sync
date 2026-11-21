-- SPDX-License-Identifier: AGPL-3.0-only
-- Storage layout of the sparse hypertables: chunk interval.
--
-- Every hypertable was left at the 7 day default. The
-- tables below hold a handful of rows per user and day (one night, a few
-- workouts, one summary per provider): ten years of history made about 520
-- nearly empty chunks each, all of them planned and locked by a query with
-- no time bound. 30 days cuts that by four and still keeps a chunk small.
-- Only chunks created from now on are affected: existing ones keep their
-- 7 days. The high volume tables (quantity, category, heartbeat, ECG and
-- route samples, observations) stay at 7 days.
SELECT set_chunk_time_interval('ohs_data.sleep_sessions', INTERVAL '30 days');
SELECT set_chunk_time_interval('ohs_data.workouts', INTERVAL '30 days');
SELECT set_chunk_time_interval('ohs_data.nutrition_logs', INTERVAL '30 days');
SELECT set_chunk_time_interval('ohs_data.ecg_recordings', INTERVAL '30 days');
SELECT set_chunk_time_interval('ohs_data.daily_summaries', INTERVAL '30 days');

-- Compression is left as it is: only quantity_samples is compressed
-- (migration 0015). heartbeat_samples, ecg_samples and route_points keep a
-- dedup unique that the ETL writer relies on (ON CONFLICT DO NOTHING), and a
-- compressed chunk checks a unique by decompressing whole batches: a period
-- delivered again fails past
-- timescaledb.max_tuples_decompressed_per_dml_transaction. They can be
-- compressed once their writes use the window dedup of quantity_samples.
