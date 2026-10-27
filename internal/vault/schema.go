// SPDX-License-Identifier: AGPL-3.0-only

package vault

// Unsealed lists the tables of ohs_data that hold no value about a person. Each
// column was checked against the migrations:
//
//   - quantity_codes: a static code list, shared by every tenant.
//   - deleted_records: tenant, user, provider, the provider's own record id and
//     the deletion time. The provider id carries no value (the same id stays in
//     clear as external_id on the sealed tables).
//   - record_aliases: tenant, user, provider, source object id, target table,
//     content hash, start time. Ids and keys only; the content hash is a keyed
//     hash once the vault is on.
//   - device_aliases: patterns that resolve a source string to a device key,
//     shared by every tenant.
var Unsealed = []string{"quantity_codes", "deleted_records", "record_aliases", "device_aliases"}

// Tags are explicit literals and never change meaning: a column added later takes
// the next free tag, a dropped column leaves its tag unused, and an entry is never
// reordered or renumbered. testdata/schema_tags.golden pins every (table, column,
// tag, kind) so any change shows as a diff (go test ./internal/vault -update).
//
// Clear lists every other column of the table, so that a column added by a later
// migration must be placed on purpose (sealed or clear) before the coverage test
// passes. The columns the vault migration adds (sealed, content_mac, source_key,
// worn, tz, derived, synthesized) are listed in advance.

// Schema describes the sealed columns of every sealed table, keyed by table name
// without the schema prefix.
//
// A column stays in clear when SQL filters, sorts or joins on it (ids, tenant,
// user, provider, record type, times, unit, source_slug, is_primary_source,
// content_hash, external_id, the parent keys of child tables). The promoted
// columns source_key, worn, tz, derived, synthesized and summary_date, and the
// sealed and content_mac columns, are clear too and are added to the tables by
// the migration that turns the vault on, so they appear in no Sealed list.
// Everything else is sealed, metadata included.
//
// Identity lists the clear columns that identify a row (the dedup key without
// tenant_id and user_id); it ends the AAD of the row. MAC marks the tables the
// ETL writes with an upsert that compares the old and the new row.
var Schema = map[string]Table{
	"quantity_samples": {
		Name: "quantity_samples",
		Sealed: []Column{
			{"value", 1, KindFloat64},
			{"source_name", 2, KindText},
			{"source_bundle_id", 3, KindText},
			{"device_id", 4, KindText},
			{"device_model", 5, KindText},
			{"product_type", 6, KindText},
			{"was_user_entered", 7, KindBool},
			{"metadata", 8, KindJSON},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "source_slug", "external_id", "quantity_type", "unit", "is_primary_source", "start_date", "end_date", "content_hash", "ingested_at", "sealed", "content_mac", "source_key", "worn", "tz", "derived", "synthesized"},
		Identity: []string{"content_hash", "start_date"},
		MAC:      true,
	},
	// quantity_hourly_sealed is the hourly statistics table that replaces the
	// continuous aggregate. It is not in the migrations yet, so the coverage test
	// does not check it. Its group keys (including the keyed source_key) stay in
	// clear; the statistics are sealed.
	"quantity_hourly_sealed": {
		Name: "quantity_hourly_sealed",
		Sealed: []Column{
			{"n", 1, KindInt64},
			{"sum", 2, KindFloat64},
			{"min", 3, KindFloat64},
			{"max", 4, KindFloat64},
		},
		Clear:    []string{"tenant_id", "user_id", "quantity_type", "provider", "source_key", "slug", "worn", "unit", "bucket", "sealed", "content_mac"},
		Identity: []string{"quantity_type", "provider", "source_key", "slug", "worn", "unit", "bucket"},
		MAC:      true,
	},
	"category_samples": {
		Name: "category_samples",
		Sealed: []Column{
			{"value_int", 1, KindInt64},
			{"value_label", 2, KindText},
			{"source_name", 3, KindText},
			{"device_id", 4, KindText},
			{"metadata", 5, KindJSON},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "source_slug", "external_id", "category_type", "start_date", "end_date", "content_hash", "ingested_at", "sealed"},
		Identity: []string{"content_hash", "start_date"},
	},
	"heartbeat_samples": {
		Name: "heartbeat_samples",
		Sealed: []Column{
			{"rr_ms", 1, KindFloat64},
		},
		Clear:    []string{"tenant_id", "user_id", "provider", "series_id", "timestamp", "content_hash", "ingested_at", "sealed"},
		Identity: []string{"content_hash", "timestamp"},
	},
	// ecg_recordings: voltage_path is a raw object key, no value, and the writer
	// finds the parent recording with voltage_path = $n (etl/writer_parents.go), so
	// it stays in clear like failed_records.raw_key.
	"ecg_recordings": {
		Name: "ecg_recordings",
		Sealed: []Column{
			{"classification", 1, KindInt64},
			{"symptoms_status", 2, KindInt64},
			{"average_heart_rate", 3, KindFloat64},
			{"sampling_frequency", 4, KindFloat64},
			{"number_of_measurements", 5, KindInt64},
			{"device_id", 7, KindText},
			{"metadata", 8, KindJSON},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "source_slug", "start_date", "end_date", "voltage_path", "content_hash", "ingested_at", "sealed"},
		Identity: []string{"content_hash", "start_date"},
	},
	"ecg_samples": {
		Name: "ecg_samples",
		Sealed: []Column{
			{"time_since_sample_start", 1, KindFloat64},
			{"voltage_uv", 2, KindFloat64},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "ecg_id", "ts", "content_hash", "ingested_at", "sealed"},
		Identity: []string{"content_hash", "ts"},
	},
	"sleep_sessions": {
		Name: "sleep_sessions",
		Sealed: []Column{
			{"total_sleep_seconds", 1, KindInt64},
			{"deep_sleep_seconds", 2, KindInt64},
			{"rem_sleep_seconds", 3, KindInt64},
			{"light_sleep_seconds", 4, KindInt64},
			{"awake_seconds", 5, KindInt64},
			{"sleep_score", 6, KindFloat64},
			{"hrv_avg_ms", 7, KindFloat64},
			{"resting_hr", 8, KindFloat64},
			{"respiratory_rate", 9, KindFloat64},
			{"spo2_avg", 10, KindFloat64},
			{"device_id", 11, KindText},
			{"metadata", 12, KindJSON},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "source_slug", "external_id", "bedtime_start", "bedtime_end", "content_hash", "ingested_at", "sealed", "content_mac"},
		Identity: []string{"content_hash", "bedtime_start"},
		MAC:      true,
	},
	"sleep_phases": {
		Name: "sleep_phases",
		Sealed: []Column{
			{"phase", 1, KindText},
			{"duration_s", 2, KindInt64},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "session_id", "start_date", "end_date", "content_hash", "ingested_at", "sealed"},
		Identity: []string{"content_hash"},
	},
	"workouts": {
		Name: "workouts",
		Sealed: []Column{
			{"duration_s", 1, KindInt64},
			{"active_energy_kcal", 2, KindFloat64},
			{"distance_m", 3, KindFloat64},
			{"avg_heart_rate", 4, KindFloat64},
			{"max_heart_rate", 5, KindFloat64},
			{"avg_speed_mps", 6, KindFloat64},
			{"elevation_gain_m", 7, KindFloat64},
			{"vo2_max", 8, KindFloat64},
			{"device_id", 9, KindText},
			{"metadata", 10, KindJSON},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "source_slug", "external_id", "activity_type", "start_date", "end_date", "content_hash", "ingested_at", "sealed", "content_mac"},
		Identity: []string{"content_hash", "start_date"},
		MAC:      true,
	},
	"workout_tracks": {
		Name: "workout_tracks",
		Sealed: []Column{
			{"length_m", 1, KindFloat64},
			{"point_count", 2, KindInt64},
		},
		Clear:    []string{"tenant_id", "user_id", "workout_id", "started_at", "ended_at", "built_at", "sealed"},
		Identity: []string{"workout_id"},
	},
	"route_points": {
		Name: "route_points",
		Sealed: []Column{
			{"latitude", 1, KindFloat64},
			{"longitude", 2, KindFloat64},
			{"altitude_m", 3, KindFloat64},
			{"speed_mps", 4, KindFloat64},
			{"course_deg", 5, KindFloat64},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "workout_id", "timestamp", "content_hash", "ingested_at", "sealed"},
		Identity: []string{"content_hash", "timestamp"},
	},
	"nutrition_logs": {
		Name: "nutrition_logs",
		Sealed: []Column{
			{"meal_type", 1, KindText},
			{"energy_kcal", 2, KindFloat64},
			{"protein_g", 3, KindFloat64},
			{"carbs_g", 4, KindFloat64},
			{"fat_g", 5, KindFloat64},
			{"fiber_g", 6, KindFloat64},
			{"sugar_g", 7, KindFloat64},
			{"sodium_mg", 8, KindFloat64},
			{"nutrients_json", 9, KindJSON},
			{"device_id", 10, KindText},
			{"metadata", 11, KindJSON},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "source_slug", "external_id", "start_date", "end_date", "content_hash", "ingested_at", "sealed", "content_mac"},
		Identity: []string{"content_hash", "start_date"},
		MAC:      true,
	},
	"observations": {
		Name: "observations",
		Sealed: []Column{
			{"value_num", 1, KindFloat64},
			{"value_code", 2, KindText},
			{"value_text", 3, KindText},
			{"value_canonical", 4, KindFloat64},
			{"type_concept", 5, KindText},
			{"source_value", 6, KindText},
			{"raw_ref", 7, KindText},
			{"source_name", 8, KindText},
			{"device_id", 9, KindText},
			{"metadata", 10, KindJSON},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "source_slug", "ts", "end_ts", "canonical_code", "unit_ucum", "is_primary_source", "content_hash", "ingested_at", "sealed"},
		Identity: []string{"content_hash", "ts"},
	},
	"daily_summaries": {
		Name: "daily_summaries",
		Sealed: []Column{
			{"readiness_score", 1, KindFloat64},
			{"activity_score", 2, KindFloat64},
			{"sleep_score", 3, KindFloat64},
			{"recovery_score", 4, KindFloat64},
			{"strain_score", 5, KindFloat64},
			{"step_count", 6, KindInt64},
			{"active_kcal", 7, KindFloat64},
			{"total_kcal", 8, KindFloat64},
			{"distance_m", 9, KindFloat64},
			{"floors_climbed", 10, KindInt64},
			{"resting_hr", 11, KindFloat64},
			{"hrv_avg_ms", 12, KindFloat64},
			{"metadata", 13, KindJSON},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "source_slug", "summary_date", "content_hash", "ingested_at", "sealed", "content_mac"},
		Identity: []string{"content_hash", "summary_date"},
		MAC:      true,
	},
	// time_tags: kind names an illness, an injury or a medication, so it is
	// sealed with the label and the note. The list API reads a user's tags by
	// start_at, which stays in clear. The kind = $n filter of timetag/store.go
	// moves to Go, so a paged list must loop until the page is full (a SQL page
	// of N rows can hold fewer than N matches).
	"time_tags": {
		Name: "time_tags",
		Sealed: []Column{
			{"kind", 1, KindText},
			{"label", 2, KindText},
			{"timezone", 3, KindText},
			{"note", 4, KindText},
			{"source", 5, KindText},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "start_at", "end_at", "created_at", "updated_at", "sealed"},
		Identity: []string{"id"},
	},
	// failed_records: the id is GENERATED ALWAYS and the identity of the row, but
	// the multi-row INSERT of the writer gives no ids. The writer must reserve ids
	// first (nextval(pg_get_serial_sequence(...)) and INSERT ... OVERRIDING SYSTEM
	// VALUE) so the identity is known before the row is sealed.
	// raw_key is an object key and reason_code, status and
	// attempts steer the retry queue, so they stay in clear. The payload and the
	// error text can carry values.
	"failed_records": {
		Name: "failed_records",
		Sealed: []Column{
			{"raw_payload", 1, KindJSON},
			{"error_message", 2, KindText},
		},
		Clear:    []string{"id", "tenant_id", "user_id", "provider", "raw_key", "line_number", "reason_code", "status", "attempts", "created_at", "recovered_at", "sealed"},
		Identity: []string{"id"},
	},
}
