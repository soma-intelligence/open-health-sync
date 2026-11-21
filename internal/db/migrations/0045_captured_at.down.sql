-- SPDX-License-Identifier: AGPL-3.0-only
--
-- quantity_samples has compressed chunks. Dropping a column that is neither a
-- segment nor an order column needs no decompression (checked on TimescaleDB
-- 2.31 with every chunk compressed).
ALTER TABLE ohs_sync.raw_objects      DROP COLUMN IF EXISTS captured_at;
ALTER TABLE ohs_data.workouts         DROP COLUMN IF EXISTS captured_at;
ALTER TABLE ohs_data.sleep_sessions   DROP COLUMN IF EXISTS captured_at;
ALTER TABLE ohs_data.daily_summaries  DROP COLUMN IF EXISTS captured_at;
ALTER TABLE ohs_data.nutrition_logs   DROP COLUMN IF EXISTS captured_at;
ALTER TABLE ohs_data.quantity_samples DROP COLUMN IF EXISTS captured_at;
