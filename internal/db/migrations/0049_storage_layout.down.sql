-- SPDX-License-Identifier: AGPL-3.0-only
-- New chunks go back to the 7 day default. Chunks created meanwhile keep
-- their 30 days: a chunk never changes its range.
SELECT set_chunk_time_interval('ohs_data.sleep_sessions', INTERVAL '7 days');
SELECT set_chunk_time_interval('ohs_data.workouts', INTERVAL '7 days');
SELECT set_chunk_time_interval('ohs_data.nutrition_logs', INTERVAL '7 days');
SELECT set_chunk_time_interval('ohs_data.ecg_recordings', INTERVAL '7 days');
SELECT set_chunk_time_interval('ohs_data.daily_summaries', INTERVAL '7 days');
