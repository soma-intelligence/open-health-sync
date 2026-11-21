-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A replaceable row (a figure its provider revises: a daily total, a night, a
-- workout) is replaced by a newer copy of itself. "Newer" used to mean
-- "processed last", so a retried, replayed or requeued job for an older raw
-- object could overwrite the provider's revision. captured_at is when the raw
-- object a row came from was captured: the writer only replaces a row with a
-- copy captured at or after it.
--
-- For a pull that is when its fetch started (ohs_sync.raw_objects.captured_at,
-- added here): the row of a raw object is registered after the data was
-- fetched, sometimes minutes after, so its created_at would put a slow pull
-- after a quicker one that fetched later. An upload or a notification body
-- has no captured_at and counts from when it was received (created_at).
--
-- NULL means unknown: rows written before this migration, and the derived
-- daily summaries, which come from no one object. An unknown time carries no
-- order, so such a row is replaced as before. The column is nullable without
-- a default, which is the form a compressed hypertable accepts.

ALTER TABLE ohs_data.quantity_samples ADD COLUMN IF NOT EXISTS captured_at timestamptz;
ALTER TABLE ohs_data.nutrition_logs   ADD COLUMN IF NOT EXISTS captured_at timestamptz;
ALTER TABLE ohs_data.daily_summaries  ADD COLUMN IF NOT EXISTS captured_at timestamptz;
ALTER TABLE ohs_data.sleep_sessions   ADD COLUMN IF NOT EXISTS captured_at timestamptz;
ALTER TABLE ohs_data.workouts         ADD COLUMN IF NOT EXISTS captured_at timestamptz;

ALTER TABLE ohs_sync.raw_objects ADD COLUMN IF NOT EXISTS captured_at timestamptz;
