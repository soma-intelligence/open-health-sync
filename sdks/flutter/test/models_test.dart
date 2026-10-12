// SPDX-License-Identifier: MIT

import 'package:open_health_sync/open_health_sync.dart';
import 'package:test/test.dart';

import 'fixtures.dart';

void main() {
  final ingested = DateTime.utc(2026, 1, 10, 14, 31, 2, 123, 456);

  void expectProvenance(Provenance p) {
    expect(p.provider, 'apple_health');
    expect(p.source, 'apple');
    expect(p.device, 'Apple Watch');
    expect(p.ingestedAt, ingested);
    expect(p.ingestedAt.isUtc, isTrue);
  }

  test('Sample', () {
    final s = Sample.fromJson(sampleJson());
    expect(s.id, 1234);
    expect(s.type, 'heart_rate');
    expect(s.value, 72.5);
    expect(s.unit, 'bpm');
    expect(s.startDate, DateTime.utc(2026, 1, 10, 14, 30));
    expect(s.endDate, DateTime.utc(2026, 1, 10, 14, 30, 0, 500));
    expect(s.sourceBundleId, 'com.apple.health.ABC');
    expect(s.productType, 'Watch7,1');
    expect(s.externalId, isNull);
    expect(s.isPrimarySource, isTrue);
    expect(s.wasUserEntered, isFalse);
    expect(s.metadata['timezone'], 'Europe/Paris');
    expectProvenance(s);
  });

  test('Sample with an integer value and nulls', () {
    final json = sampleJson()
      ..['value'] = 120
      ..['unit'] = null
      ..['end_date'] = null
      ..['source'] = null
      ..['device'] = null
      ..remove('metadata');
    final s = Sample.fromJson(json);
    expect(s.value, 120.0);
    expect(s.unit, isNull);
    expect(s.endDate, isNull);
    expect(s.source, isNull);
    expect(s.metadata, isEmpty);
  });

  test('CategorySample', () {
    final c = CategorySample.fromJson(categorySampleJson());
    expect(c.id, 88);
    expect(c.valueInt, 0);
    expect(c.valueLabel, 'not_applicable');
    expect(c.externalId, 'ext-88');
    expectProvenance(c);
  });

  test('SleepSession', () {
    final s = SleepSession.fromJson(sleepSessionJson());
    expect(s.id, 77);
    expect(s.bedtimeEnd, DateTime.utc(2026, 1, 10, 6));
    expect(s.totalSleepSeconds, 26100);
    expect(s.awakeSeconds, 2700);
    expect(s.sleepScore, 84.0);
    expect(s.hrvAvgMs, 48.2);
    expect(s.spo2Avg, 96.5);
    expectProvenance(s);
  });

  test('SleepPhase', () {
    final p = SleepPhase.fromJson(sleepPhaseJson());
    expect(p.sessionId, 77);
    expect(p.phase, 'deep');
    expect(p.durationS, 5400);
    expectProvenance(p);
  });

  test('Workout', () {
    final w = Workout.fromJson(workoutJson());
    expect(w.id, 9001);
    expect(w.activityType, 'running');
    expect(w.durationS, 2700);
    expect(w.activeEnergyKcal, 480.0);
    expect(w.distanceM, 7200.5);
    expect(w.vo2Max, isNull);
    expect(w.externalId, 'w-1');
    expectProvenance(w);
  });

  test('RoutePoint', () {
    final r = RoutePoint.fromJson(routePointJson());
    expect(r.workoutId, 9001);
    expect(r.latitude, 48.8566);
    expect(r.altitudeM, 35.0);
    expect(r.courseDeg, isNull);
    expectProvenance(r);
  });

  test('NutritionLog', () {
    final n = NutritionLog.fromJson(nutritionJson());
    expect(n.mealType, 'lunch');
    expect(n.carbsG, 70.5);
    expect(n.sodiumMg, 900.0);
    expect(n.nutrients, {'vitamin_c_mg': 30});
    expect(n.endDate, isNull);
    expectProvenance(n);
  });

  test('DailySummary keeps date as a string', () {
    final d = DailySummary.fromJson(dailyJson());
    expect(d.date, '2026-01-10');
    expect(d.stepCount, 9500);
    expect(d.activityScore, isNull);
    expect(d.floorsClimbed, 4);
    expect(d.activeKcal, 520.5);
    expectProvenance(d);
  });

  test('ids above 32 bits', () {
    final s = Sample.fromJson(sampleJson(id: 9007199254740991));
    expect(s.id, 9007199254740991);
  });

  test('a missing required field names the field only', () {
    final json = sampleJson()..remove('start_date');
    expect(
      () => Sample.fromJson(json),
      throwsA(isA<FormatException>()
          .having((e) => e.message, 'message', contains('start_date'))),
    );
  });

  group('timestamps', () {
    test('nanoseconds are trimmed to microseconds', () {
      expect(parseTimestamp('2026-01-10T14:30:00.123456789Z'),
          DateTime.utc(2026, 1, 10, 14, 30, 0, 123, 456));
    });

    test('no fraction, short fraction, offsets', () {
      expect(parseTimestamp('2026-01-10T14:30:00Z'),
          DateTime.utc(2026, 1, 10, 14, 30));
      expect(parseTimestamp('2026-01-10T14:30:00.5Z'),
          DateTime.utc(2026, 1, 10, 14, 30, 0, 500));
      expect(parseTimestamp('2026-01-10T15:30:00+01:00'),
          DateTime.utc(2026, 1, 10, 14, 30));
      expect(parseTimestamp('2026-01-10T09:30:00.000000001-05:00'),
          DateTime.utc(2026, 1, 10, 14, 30));
      expect(parseTimestamp('2026-01-10T14:30:00Z').isUtc, isTrue);
    });

    test('rejects other shapes', () {
      expect(() => parseTimestamp('2026-01-10'), throwsFormatException);
      expect(
          () => parseTimestamp('2026-01-10T14:30:00'), throwsFormatException);
      expect(() => parseTimestamp('yesterday'), throwsFormatException);
    });

    test('formatTimestamp gives UTC RFC 3339', () {
      final t = DateTime.utc(2026, 1, 10, 14, 30);
      expect(formatTimestamp(t), '2026-01-10T14:30:00.000Z');
      expect(parseTimestamp(formatTimestamp(t.toLocal())), t);
    });
  });
}
