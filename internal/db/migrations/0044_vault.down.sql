-- SPDX-License-Identifier: AGPL-3.0-only
-- Back to the schema before the vault. Dropping the sealed columns discards
-- sealed rows' content. Restoring NOT NULL fails if a sealed row exists (its
-- values are NULL), which is the safe outcome: open and unseal every row
-- first, or accept the loss and delete those rows.

DROP TABLE IF EXISTS ohs_data.quantity_hourly_sealed;

ALTER TABLE ohs_data.time_tags
    ALTER COLUMN kind SET NOT NULL,
    ALTER COLUMN source SET NOT NULL;
ALTER TABLE ohs_data.daily_summaries ALTER COLUMN metadata SET NOT NULL;
ALTER TABLE ohs_data.observations ALTER COLUMN metadata SET NOT NULL;
ALTER TABLE ohs_data.nutrition_logs ALTER COLUMN metadata SET NOT NULL;
ALTER TABLE ohs_data.route_points
    ALTER COLUMN latitude SET NOT NULL,
    ALTER COLUMN longitude SET NOT NULL;
ALTER TABLE ohs_data.workout_tracks
    ALTER COLUMN length_m SET NOT NULL,
    ALTER COLUMN point_count SET NOT NULL;
ALTER TABLE ohs_data.workouts ALTER COLUMN metadata SET NOT NULL;
ALTER TABLE ohs_data.sleep_phases
    ALTER COLUMN phase SET NOT NULL,
    ALTER COLUMN duration_s SET NOT NULL;
ALTER TABLE ohs_data.sleep_sessions ALTER COLUMN metadata SET NOT NULL;
ALTER TABLE ohs_data.ecg_samples
    ALTER COLUMN time_since_sample_start SET NOT NULL,
    ALTER COLUMN voltage_uv SET NOT NULL;
ALTER TABLE ohs_data.ecg_recordings
    ALTER COLUMN classification SET NOT NULL,
    ALTER COLUMN symptoms_status SET NOT NULL,
    ALTER COLUMN number_of_measurements SET NOT NULL,
    ALTER COLUMN sampling_frequency SET NOT NULL,
    ALTER COLUMN metadata SET NOT NULL;
ALTER TABLE ohs_data.heartbeat_samples ALTER COLUMN rr_ms SET NOT NULL;
ALTER TABLE ohs_data.category_samples ALTER COLUMN metadata SET NOT NULL;
ALTER TABLE ohs_data.quantity_samples ALTER COLUMN value SET NOT NULL, ALTER COLUMN metadata SET NOT NULL;

ALTER TABLE ohs_data.failed_records    DROP COLUMN sealed;
ALTER TABLE ohs_data.time_tags         DROP COLUMN sealed;
ALTER TABLE ohs_data.daily_summaries   DROP COLUMN sealed, DROP COLUMN content_mac, DROP COLUMN derived;
ALTER TABLE ohs_data.observations      DROP COLUMN sealed;
ALTER TABLE ohs_data.nutrition_logs    DROP COLUMN sealed, DROP COLUMN content_mac;
ALTER TABLE ohs_data.route_points      DROP COLUMN sealed;
ALTER TABLE ohs_data.workout_tracks    DROP COLUMN sealed;
ALTER TABLE ohs_data.workouts
    DROP COLUMN sealed, DROP COLUMN content_mac, DROP COLUMN source_key, DROP COLUMN worn;
ALTER TABLE ohs_data.sleep_phases      DROP COLUMN sealed;
ALTER TABLE ohs_data.sleep_sessions
    DROP COLUMN sealed, DROP COLUMN content_mac, DROP COLUMN source_key, DROP COLUMN worn,
    DROP COLUMN synthesized;
ALTER TABLE ohs_data.ecg_samples       DROP COLUMN sealed;
ALTER TABLE ohs_data.ecg_recordings    DROP COLUMN sealed;
ALTER TABLE ohs_data.heartbeat_samples DROP COLUMN sealed;
ALTER TABLE ohs_data.category_samples  DROP COLUMN sealed, DROP COLUMN tz;
ALTER TABLE ohs_data.quantity_samples
    DROP COLUMN sealed,
    DROP COLUMN content_mac,
    DROP COLUMN source_key,
    DROP COLUMN worn,
    DROP COLUMN tz,
    DROP COLUMN derived,
    DROP COLUMN synthesized,
    DROP COLUMN summary_date;

DROP TABLE IF EXISTS ohs_core.vault_keys;
DROP TABLE IF EXISTS ohs_core.vault_root;
