// SPDX-License-Identifier: AGPL-3.0-only

// Invented values in the shapes of api/openapi.yaml.

export const USER_ID = "3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11";

const provenance = {
  provider: "apple_health",
  source: "Apple Watch",
  device: "Watch7,1",
  ingested_at: "2026-01-10T15:00:00.123456789Z",
};

export const sample = {
  ...provenance,
  id: 1234,
  type: "heart_rate",
  value: 72.5,
  unit: "bpm",
  start_date: "2026-01-10T14:30:00.123456789Z",
  end_date: "2026-01-10T14:30:00Z",
  source_name: "Apple Watch",
  source_bundle_id: "com.example.health",
  device_model: "Watch",
  product_type: "Watch7,1",
  external_id: "A1B2",
  is_primary_source: true,
  was_user_entered: false,
  metadata: { timezone: "Europe/Paris", HKTimeZone: "Europe/Paris", nested_key: { a_b: 1 } },
  some_future_field: "ignored",
};

export const categorySample = {
  ...provenance,
  id: 55,
  type: "mindful_session",
  value_int: 0,
  value_label: null,
  start_date: "2026-01-10T08:00:00Z",
  end_date: "2026-01-10T08:10:00Z",
  source_name: "iPhone",
  external_id: null,
  metadata: {},
};

export const sleepSession = {
  ...provenance,
  source: null,
  id: 77,
  bedtime_start: "2026-01-09T22:00:00Z",
  bedtime_end: "2026-01-10T06:00:00Z",
  total_sleep_seconds: 26100,
  deep_sleep_seconds: 5400,
  rem_sleep_seconds: 6300,
  light_sleep_seconds: 14400,
  awake_seconds: 900,
  sleep_score: 84,
  hrv_avg_ms: 48.2,
  resting_hr: 52,
  respiratory_rate: 14.1,
  spo2_avg: 96.5,
  external_id: "s-77",
  metadata: { timezone: "Europe/Paris" },
};

export const sleepPhase = {
  ...provenance,
  id: 9001,
  session_id: 77,
  phase: "deep",
  start_date: "2026-01-09T22:15:00Z",
  end_date: "2026-01-09T23:45:00Z",
  duration_s: 5400,
};

export const workout = {
  ...provenance,
  id: 4242,
  activity_type: "running",
  start_date: "2026-01-10T07:00:00Z",
  end_date: "2026-01-10T07:45:00Z",
  duration_s: 2700,
  active_energy_kcal: 480,
  distance_m: 7200.5,
  avg_heart_rate: 132,
  max_heart_rate: 165,
  avg_speed_mps: 2.67,
  elevation_gain_m: 35,
  vo2_max: null,
  external_id: "w-1",
  metadata: { indoor: false },
};

export const routePoint = {
  ...provenance,
  id: 1,
  workout_id: 4242,
  timestamp: "2026-01-10T07:00:01Z",
  latitude: 48.8566,
  longitude: 2.3522,
  altitude_m: 35.2,
  speed_mps: 2.7,
  course_deg: 180,
};

export const nutritionLog = {
  ...provenance,
  id: 31,
  start_date: "2026-01-10T12:00:00Z",
  end_date: null,
  meal_type: "lunch",
  energy_kcal: 650,
  protein_g: 32,
  carbs_g: 70,
  fat_g: 22,
  fiber_g: 9,
  sugar_g: 12,
  sodium_mg: 800,
  nutrients: { vitamin_c_mg: 45 },
  external_id: null,
  metadata: {},
};

export const dailySummary = {
  ...provenance,
  provider: "oura",
  id: 12,
  date: "2026-01-10",
  readiness_score: 81,
  activity_score: 77,
  sleep_score: 84,
  recovery_score: null,
  strain_score: null,
  step_count: 9832,
  active_kcal: 520.5,
  total_kcal: 2400,
  distance_m: 7400,
  floors_climbed: 6,
  resting_hr: 52,
  hrv_avg_ms: 48,
  metadata: {},
};

export const coverage = {
  pending_jobs: 0,
  parked_jobs: 1,
  rejected_records: 2,
  truncated: false,
  days: [
    {
      provider: "apple_health",
      date: "2026-01-10",
      records: 3,
      metrics: { heart_rate: 2, step_count: 1 },
      last_synced_at: "2026-01-10T15:00:00.5Z",
    },
  ],
};

export const connection = {
  provider: "oura",
  status: "active",
  scopes: ["daily", "heartrate"],
  last_sync_at: "2026-01-10T15:00:00Z",
  initial_backfill_at: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-10T15:00:00Z",
};

export const freshness = {
  as_of: "2026-01-10T15:05:00Z",
  data: [
    {
      provider: "oura",
      kind: "oauth",
      status: "active",
      state: "fresh",
      synced_through: "2026-01-10T15:00:00Z",
      lag_seconds: 300,
      last_sync_at: "2026-01-10T15:00:00Z",
      last_event_at: null,
      push_healthy: true,
      pull_every_seconds: 21600,
      next_pull_at: "2026-01-10T21:00:00Z",
      last_attempt_at: "2026-01-10T15:00:00Z",
      last_error: null,
      consecutive_failures: 0,
    },
  ],
};

export const user = {
  id: USER_ID,
  external_ref: "customer-42",
  status: "active",
  created_at: "2026-01-01T00:00:00Z",
};

export const deletionJob = {
  job_id: "0b6c7a43-1d11-4b55-a0a9-8a58f0f7d001",
  user_id: USER_ID,
  status: "done",
  created_at: "2026-01-11T00:00:00Z",
  finished_at: "2026-01-11T00:00:05Z",
};

export const userToken = {
  token: "ohs_ut_FAKEFAKEFAKE",
  expires_at: "2026-01-11T01:00:00Z",
  scopes: ["upload", "read"],
};

export const fhirBundle = {
  resourceType: "Bundle",
  type: "searchset",
  link: [
    { relation: "self", url: "https://ohs.example.org/v1/users/u/fhir/Observation?limit=1" },
    {
      relation: "next",
      url: "https://ohs.example.org/v1/users/u/fhir/Observation?limit=1&cursor=eyJ0IjoiMjAyNi0wMS0xMCJ9%3D%3D",
    },
  ],
  entry: [
    {
      fullUrl: "https://ohs.example.org/v1/users/u/fhir/Observation/qs-1234",
      resource: {
        resourceType: "Observation",
        id: "qs-1234",
        status: "final",
        code: { coding: [{ system: "http://loinc.org", code: "8867-4" }] },
        subject: { reference: `Patient/${USER_ID}` },
        valueQuantity: { value: 72.5, unit: "beats/minute", system: "http://unitsofmeasure.org", code: "/min" },
      },
      search: { mode: "match" },
    },
  ],
};
