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

// cols numbers the sealed columns of a table in the order written. Tags start at
// 1 (0 ends a row) and are never reused: a column added later takes the next
// free tag, a dropped column leaves its tag unused.
func cols(spec ...any) []Column {
	out := make([]Column, 0, len(spec)/2)
	for i := 0; i < len(spec); i += 2 {
		out = append(out, Column{Name: spec[i].(string), Tag: uint8(i/2 + 1), Kind: spec[i+1].(Kind)})
	}
	return out
}

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
		Sealed: cols(
			"value", KindFloat64,
			"source_name", KindText,
			"source_bundle_id", KindText,
			"device_id", KindText,
			"device_model", KindText,
			"product_type", KindText,
			"was_user_entered", KindBool,
			"metadata", KindJSON,
		),
		Identity: []string{"content_hash", "start_date"},
		MAC:      true,
	},
	// quantity_hourly_sealed is the hourly statistics table that replaces the
	// continuous aggregate. It is not in the migrations yet, so the coverage test
	// does not check it. Its group keys (including the keyed source_key) stay in
	// clear; the statistics are sealed.
	"quantity_hourly_sealed": {
		Name: "quantity_hourly_sealed",
		Sealed: cols(
			"n", KindInt64,
			"sum", KindFloat64,
			"min", KindFloat64,
			"max", KindFloat64,
		),
		Identity: []string{"quantity_type", "provider", "source_key", "slug", "worn", "unit", "bucket"},
		MAC:      true,
	},
	"category_samples": {
		Name: "category_samples",
		Sealed: cols(
			"value_int", KindInt64,
			"value_label", KindText,
			"source_name", KindText,
			"device_id", KindText,
			"metadata", KindJSON,
		),
		Identity: []string{"content_hash", "start_date"},
	},
	"heartbeat_samples": {
		Name: "heartbeat_samples",
		Sealed: cols(
			"rr_ms", KindFloat64,
		),
		Identity: []string{"content_hash", "timestamp"},
	},
	"ecg_recordings": {
		Name: "ecg_recordings",
		Sealed: cols(
			"classification", KindInt64,
			"symptoms_status", KindInt64,
			"average_heart_rate", KindFloat64,
			"sampling_frequency", KindFloat64,
			"number_of_measurements", KindInt64,
			"voltage_path", KindText,
			"device_id", KindText,
			"metadata", KindJSON,
		),
		Identity: []string{"content_hash", "start_date"},
	},
	"ecg_samples": {
		Name: "ecg_samples",
		Sealed: cols(
			"time_since_sample_start", KindFloat64,
			"voltage_uv", KindFloat64,
		),
		Identity: []string{"content_hash", "ts"},
	},
	"sleep_sessions": {
		Name: "sleep_sessions",
		Sealed: cols(
			"total_sleep_seconds", KindInt64,
			"deep_sleep_seconds", KindInt64,
			"rem_sleep_seconds", KindInt64,
			"light_sleep_seconds", KindInt64,
			"awake_seconds", KindInt64,
			"sleep_score", KindFloat64,
			"hrv_avg_ms", KindFloat64,
			"resting_hr", KindFloat64,
			"respiratory_rate", KindFloat64,
			"spo2_avg", KindFloat64,
			"device_id", KindText,
			"metadata", KindJSON,
		),
		Identity: []string{"content_hash", "bedtime_start"},
		MAC:      true,
	},
	"sleep_phases": {
		Name: "sleep_phases",
		Sealed: cols(
			"phase", KindText,
			"duration_s", KindInt64,
		),
		Identity: []string{"content_hash"},
	},
	"workouts": {
		Name: "workouts",
		Sealed: cols(
			"duration_s", KindInt64,
			"active_energy_kcal", KindFloat64,
			"distance_m", KindFloat64,
			"avg_heart_rate", KindFloat64,
			"max_heart_rate", KindFloat64,
			"avg_speed_mps", KindFloat64,
			"elevation_gain_m", KindFloat64,
			"vo2_max", KindFloat64,
			"device_id", KindText,
			"metadata", KindJSON,
		),
		Identity: []string{"content_hash", "start_date"},
		MAC:      true,
	},
	"workout_tracks": {
		Name: "workout_tracks",
		Sealed: cols(
			"length_m", KindFloat64,
			"point_count", KindInt64,
		),
		Identity: []string{"workout_id"},
	},
	"route_points": {
		Name: "route_points",
		Sealed: cols(
			"latitude", KindFloat64,
			"longitude", KindFloat64,
			"altitude_m", KindFloat64,
			"speed_mps", KindFloat64,
			"course_deg", KindFloat64,
		),
		Identity: []string{"content_hash", "timestamp"},
	},
	"nutrition_logs": {
		Name: "nutrition_logs",
		Sealed: cols(
			"meal_type", KindText,
			"energy_kcal", KindFloat64,
			"protein_g", KindFloat64,
			"carbs_g", KindFloat64,
			"fat_g", KindFloat64,
			"fiber_g", KindFloat64,
			"sugar_g", KindFloat64,
			"sodium_mg", KindFloat64,
			"nutrients_json", KindJSON,
			"device_id", KindText,
			"metadata", KindJSON,
		),
		Identity: []string{"content_hash", "start_date"},
		MAC:      true,
	},
	"observations": {
		Name: "observations",
		Sealed: cols(
			"value_num", KindFloat64,
			"value_code", KindText,
			"value_text", KindText,
			"value_canonical", KindFloat64,
			"type_concept", KindText,
			"source_value", KindText,
			"raw_ref", KindText,
			"source_name", KindText,
			"device_id", KindText,
			"metadata", KindJSON,
		),
		Identity: []string{"content_hash", "ts"},
	},
	"daily_summaries": {
		Name: "daily_summaries",
		Sealed: cols(
			"readiness_score", KindFloat64,
			"activity_score", KindFloat64,
			"sleep_score", KindFloat64,
			"recovery_score", KindFloat64,
			"strain_score", KindFloat64,
			"step_count", KindInt64,
			"active_kcal", KindFloat64,
			"total_kcal", KindFloat64,
			"distance_m", KindFloat64,
			"floors_climbed", KindInt64,
			"resting_hr", KindFloat64,
			"hrv_avg_ms", KindFloat64,
			"metadata", KindJSON,
		),
		Identity: []string{"content_hash", "summary_date"},
		MAC:      true,
	},
	// time_tags: kind names an illness, an injury or a medication, so it is
	// sealed with the label and the note. The list API reads a user's tags by
	// start_at, which stays in clear.
	"time_tags": {
		Name: "time_tags",
		Sealed: cols(
			"kind", KindText,
			"label", KindText,
			"timezone", KindText,
			"note", KindText,
			"source", KindText,
		),
		Identity: []string{"id"},
	},
	// failed_records: raw_key is an object key and reason_code, status and
	// attempts steer the retry queue, so they stay in clear. The payload and the
	// error text can carry values.
	"failed_records": {
		Name: "failed_records",
		Sealed: cols(
			"raw_payload", KindJSON,
			"error_message", KindText,
		),
		Identity: []string{"id"},
	},
}
