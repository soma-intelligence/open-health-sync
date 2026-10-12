// SPDX-License-Identifier: MIT

// Realistic answers of the API, with invented values.

const String userId = '3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11';

Map<String, Object?> provenance() => {
      'provider': 'apple_health',
      'source': 'apple',
      'device': 'Apple Watch',
      'ingested_at': '2026-01-10T14:31:02.123456789Z',
      'some_future_field': {'ignored': true},
    };

Map<String, Object?> sampleJson({int id = 1234}) => {
      'id': id,
      'type': 'heart_rate',
      'value': 72.5,
      'unit': 'bpm',
      'start_date': '2026-01-10T14:30:00Z',
      'end_date': '2026-01-10T14:30:00.5Z',
      'source_name': 'Apple Watch',
      'source_bundle_id': 'com.apple.health.ABC',
      'device_model': 'Watch',
      'product_type': 'Watch7,1',
      'external_id': null,
      'is_primary_source': true,
      'was_user_entered': false,
      'metadata': {
        'timezone': 'Europe/Paris',
        'HKMetadataKeyHeartRateMotionContext': 1
      },
      ...provenance(),
    };

Map<String, Object?> categorySampleJson() => {
      'id': 88,
      'type': 'mindful_session',
      'value_int': 0,
      'value_label': 'not_applicable',
      'start_date': '2026-01-10T08:00:00Z',
      'end_date': '2026-01-10T08:10:00Z',
      'source_name': 'iPhone',
      'external_id': 'ext-88',
      'metadata': <String, Object?>{},
      ...provenance(),
    };

Map<String, Object?> sleepSessionJson() => {
      'id': 77,
      'bedtime_start': '2026-01-09T22:00:00Z',
      'bedtime_end': '2026-01-10T06:00:00Z',
      'total_sleep_seconds': 26100,
      'deep_sleep_seconds': 5400,
      'rem_sleep_seconds': 6300,
      'light_sleep_seconds': 14400,
      'awake_seconds': 2700,
      'sleep_score': 84,
      'hrv_avg_ms': 48.2,
      'resting_hr': 52,
      'respiratory_rate': 14.5,
      'spo2_avg': 96.5,
      'external_id': null,
      'metadata': {'timezone': 'Europe/Paris'},
      ...provenance(),
    };

Map<String, Object?> sleepPhaseJson() => {
      'id': 701,
      'session_id': 77,
      'phase': 'deep',
      'start_date': '2026-01-09T22:15:00Z',
      'end_date': '2026-01-09T23:45:00Z',
      'duration_s': 5400,
      ...provenance(),
    };

Map<String, Object?> workoutJson() => {
      'id': 9001,
      'activity_type': 'running',
      'start_date': '2026-01-10T07:00:00Z',
      'end_date': '2026-01-10T07:45:00Z',
      'duration_s': 2700,
      'active_energy_kcal': 480,
      'distance_m': 7200.5,
      'avg_heart_rate': 132,
      'max_heart_rate': 165,
      'avg_speed_mps': 2.67,
      'elevation_gain_m': 35.2,
      'vo2_max': null,
      'external_id': 'w-1',
      'metadata': {'timezone': 'Europe/Paris'},
      ...provenance(),
    };

Map<String, Object?> routePointJson() => {
      'id': 50001,
      'workout_id': 9001,
      'timestamp': '2026-01-10T07:00:05Z',
      'latitude': 48.8566,
      'longitude': 2.3522,
      'altitude_m': 35,
      'speed_mps': 2.7,
      'course_deg': null,
      ...provenance(),
    };

Map<String, Object?> nutritionJson() => {
      'id': 300,
      'start_date': '2026-01-10T12:00:00Z',
      'end_date': null,
      'meal_type': 'lunch',
      'energy_kcal': 650,
      'protein_g': 32,
      'carbs_g': 70.5,
      'fat_g': 22,
      'fiber_g': 8,
      'sugar_g': 12,
      'sodium_mg': 900,
      'nutrients': {'vitamin_c_mg': 30},
      'external_id': null,
      'metadata': <String, Object?>{},
      ...provenance(),
    };

Map<String, Object?> dailyJson() => {
      'id': 12,
      'date': '2026-01-10',
      'readiness_score': 80,
      'activity_score': null,
      'sleep_score': 84,
      'recovery_score': null,
      'strain_score': null,
      'step_count': 9500,
      'active_kcal': 520.5,
      'total_kcal': 2300,
      'distance_m': 7300,
      'floors_climbed': 4,
      'resting_hr': 52,
      'hrv_avg_ms': 48,
      'metadata': <String, Object?>{},
      ...provenance(),
    };

Map<String, Object?> page(List<Map<String, Object?>> data, [String? next]) =>
    {'data': data, 'next_cursor': next};
