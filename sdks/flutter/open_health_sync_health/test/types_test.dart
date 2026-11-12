// SPDX-License-Identifier: AGPL-3.0-only

import 'package:flutter_test/flutter_test.dart';
import 'package:open_health_sync_health/open_health_sync_health.dart';

/// The record types of the Kotlin SDK's `HealthConnectRecordType`, in its
/// order. A type added there must be named here too.
const _healthConnectRecordTypes = [
  'Steps',
  'HeartRate',
  'RestingHeartRate',
  'HeartRateVariabilityRmssd',
  'OxygenSaturation',
  'RespiratoryRate',
  'BodyTemperature',
  'BloodPressure',
  'BloodGlucose',
  'Weight',
  'Height',
  'BodyFat',
  'ActiveCaloriesBurned',
  'TotalCaloriesBurned',
  'BasalMetabolicRate',
  'Distance',
  'SleepSession',
  'ExerciseSession',
  'MenstruationFlow',
  'IntermenstrualBleeding',
  'Nutrition',
  'Hydration',
];

void main() {
  test('every type is on at least one platform', () {
    for (final type in HealthDataType.values) {
      expect(
        type.healthKit.isNotEmpty || type.healthConnect.isNotEmpty,
        isTrue,
        reason: type.name,
      );
    }
  });

  test('every Health Connect record type is named exactly once', () {
    final named = [
      for (final type in HealthDataType.values) ...type.healthConnect,
    ];
    expect(named.toSet(), _healthConnectRecordTypes.toSet());
    expect(named, hasLength(_healthConnectRecordTypes.length));
  });

  test('HealthKit entries are identifiers or one of the three groups', () {
    const groups = {'@quantities', '@nutrition', '@categories'};
    for (final type in HealthDataType.values) {
      for (final id in type.healthKit) {
        expect(id.startsWith('HK') || groups.contains(id), isTrue, reason: id);
      }
    }
  });

  test('the support map answers per platform', () {
    const ecg = HealthDataType.electrocardiogram;
    expect(ecg.isSupportedOn(HealthPlatform.healthKit), isTrue);
    expect(ecg.isSupportedOn(HealthPlatform.healthConnect), isFalse);
    expect(
      HealthDataType.totalEnergy.isSupportedOn(HealthPlatform.healthKit),
      isFalse,
    );
    expect(
      HealthDataType.supportedOn(HealthPlatform.healthConnect),
      isNot(contains(ecg)),
    );
    expect(
      HealthDataType.supportedOn(HealthPlatform.healthKit),
      contains(HealthDataType.steps),
    );
  });

  test('a native type gives back its Dart type', () {
    expect(
      HealthDataType.fromNative(HealthPlatform.healthConnect, 'SleepSession'),
      {HealthDataType.sleep},
    );
    expect(
      HealthDataType.fromNative(
        HealthPlatform.healthKit,
        'HKQuantityTypeIdentifierBloodPressureSystolic',
      ),
      {HealthDataType.bloodPressure},
    );
    expect(
      HealthDataType.fromNative(HealthPlatform.healthConnect, 'Unknown'),
      isEmpty,
    );
  });
}
