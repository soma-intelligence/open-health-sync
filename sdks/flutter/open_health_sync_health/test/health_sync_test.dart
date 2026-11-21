// SPDX-License-Identifier: AGPL-3.0-only

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:open_health_sync_health/open_health_sync_health.dart';
import 'package:open_health_sync_health/src/channel.dart';

const _token = 'ohs_ut_secret';

/// Matches a future that fails with a [HealthSyncException] of [code].
Matcher _throwsCode(String code) {
  bool hasCode(Object? e) => e is HealthSyncException && e.code == code;
  return throwsA(predicate(hasCode, 'a HealthSyncException $code'));
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  const methods = MethodChannel(methodChannelName);
  const events = EventChannel(eventChannelName);
  final messenger =
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
  final calls = <MethodCall>[];
  final health = OpenHealthSyncHealth.instance;

  /// Answers every call with what [answer] returns, and records the call.
  void answerWith(Object? Function(MethodCall call) answer) {
    messenger.setMockMethodCallHandler(methods, (call) async {
      calls.add(call);
      return answer(call);
    });
  }

  setUp(() {
    calls.clear();
    debugDefaultTargetPlatformOverride = TargetPlatform.android;
    answerWith((call) => null);
  });

  tearDown(() {
    messenger.setMockMethodCallHandler(methods, null);
    messenger.setMockStreamHandler(events, null);
    debugDefaultTargetPlatformOverride = null;
  });

  group('platform', () {
    test('is Health Connect on Android and HealthKit on iOS', () {
      expect(health.platform, HealthPlatform.healthConnect);
      debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
      expect(health.platform, HealthPlatform.healthKit);
    });

    test('a desktop has no store and never reaches the channel', () async {
      debugDefaultTargetPlatformOverride = TargetPlatform.macOS;
      expect(health.platform, isNull);
      expect(await health.isAvailable(), isFalse);
      expect(await health.pendingParts(), 0);
      await health.disableBackgroundSync();
      expect(health.backgroundSyncResults, emitsDone);
      await expectLater(health.sync(), _throwsCode('unsupported_platform'));
      expect(calls, isEmpty);
    });
  });

  group('availability', () {
    test('reads the three states', () async {
      answerWith((call) => 'available');
      expect(await health.availability(), HealthStoreAvailability.available);
      expect(await health.isAvailable(), isTrue);

      answerWith((call) => 'updateRequired');
      expect(
        await health.availability(),
        HealthStoreAvailability.updateRequired,
      );
      expect(await health.isAvailable(), isFalse);

      answerWith((call) => 'something new');
      expect(await health.availability(), HealthStoreAvailability.unavailable);
    });
  });

  group('credentials', () {
    test('are sent to the native side', () async {
      await health.setCredentials(
        baseUrl: 'https://ohs.example.org',
        userToken: _token,
      );
      expect(calls.single.method, 'setCredentials');
      expect(calls.single.arguments, {
        'baseUrl': 'https://ohs.example.org',
        'userToken': _token,
      });

      await health.clearCredentials();
      expect(calls.last.method, 'clearCredentials');
    });

    test('a bad URL or an empty token is refused before the channel', () async {
      const url = 'https://ohs.example.org';
      await expectLater(
        health.setCredentials(baseUrl: 'ohs.example.org', userToken: _token),
        throwsArgumentError,
      );
      await expectLater(
        health.setCredentials(baseUrl: url, userToken: ''),
        throwsArgumentError,
      );
      expect(calls, isEmpty);
    });

    test('an error never carries the token', () async {
      Object? error;
      try {
        await health.setCredentials(baseUrl: 'ftp://x', userToken: _token);
      } on ArgumentError catch (e) {
        error = e;
      }
      expect(error, isNotNull);
      expect(error.toString(), isNot(contains(_token)));
    });
  });

  group('requestAuthorization', () {
    test('sends the Health Connect record types and reads the grant', () async {
      answerWith(
        (call) => {
          'granted': ['Steps', 'BloodPressure'],
          'notDeclared': ['SleepSession'],
          'backgroundGranted': true,
          'historyGranted': null,
        },
      );
      final result = await health.requestAuthorization(
        {
          HealthDataType.steps,
          HealthDataType.bloodPressure,
          HealthDataType.heartRate,
          HealthDataType.sleep,
          HealthDataType.electrocardiogram,
        },
        background: true,
      );

      expect(calls.single.method, 'requestAuthorization');
      expect(calls.single.arguments, {
        'types': ['Steps', 'BloodPressure', 'HeartRate', 'SleepSession'],
        'background': true,
        'history': false,
      });
      expect(result.requested, {
        HealthDataType.steps,
        HealthDataType.bloodPressure,
        HealthDataType.heartRate,
        HealthDataType.sleep,
      });
      expect(result.granted, {
        HealthDataType.steps,
        HealthDataType.bloodPressure,
      });
      expect(result.unsupported, {HealthDataType.electrocardiogram});
      expect(result.notDeclared, {HealthDataType.sleep});
      expect(result.backgroundGranted, isTrue);
      expect(result.historyGranted, isNull);
    });

    test('sends HealthKit identifiers and has no grant on iOS', () async {
      debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
      answerWith((call) => {'granted': null, 'notDeclared': <String>[]});
      final result = await health.requestAuthorization({
        HealthDataType.bloodPressure,
        HealthDataType.nutrition,
        HealthDataType.totalEnergy,
      });

      expect(calls.single.arguments, {
        'types': [
          'HKQuantityTypeIdentifierBloodPressureSystolic',
          'HKQuantityTypeIdentifierBloodPressureDiastolic',
          '@nutrition',
        ],
        'background': false,
        'history': false,
      });
      expect(result.granted, isNull);
      expect(result.unsupported, {HealthDataType.totalEnergy});
      expect(result.notDeclared, isEmpty);
    });

    test('asks nothing when the platform has none of the types', () async {
      final result = await health.requestAuthorization({
        HealthDataType.audiogram,
      });
      expect(result.requested, isEmpty);
      expect(result.unsupported, {HealthDataType.audiogram});
      expect(calls, isEmpty);
    });
  });

  group('sync', () {
    test('without types asks the native side for all of them', () async {
      answerWith((call) => <String, Object?>{});
      final report = await health.sync();
      expect(calls.single.method, 'sync');
      expect(calls.single.arguments, {'types': null});
      expect(report.partsUploaded, 0);
      expect(report.lines, 0);
      expect(report.uploadError, isNull);
    });

    test('reads an Android report', () async {
      answerWith(
        (call) => {
          'batchIds': ['android-1', 'android-2'],
          'partsUploaded': 3,
          'duplicateParts': 1,
          'partsPending': 2,
          'partsDropped': 1,
          'linesByType': {'Steps': 40, 'HeartRate': 2},
          'deletionsByType': {'Steps': 1},
          'deletionsPendingByType': <String, int>{},
          'skippedRecords': 4,
          'typesWithoutPermission': ['SleepSession'],
          'expiredTokenTypes': ['HeartRate'],
          'uploadError': {
            'code': 'api_error',
            'message': 'HTTP 503',
            'status': 503,
            'requestId': 'req-1',
          },
        },
      );
      final report = await health.sync(
        types: {
          HealthDataType.steps,
          HealthDataType.heartRate,
          HealthDataType.sleep,
          HealthDataType.stateOfMind,
        },
      );

      expect(calls.single.arguments, {
        'types': ['Steps', 'HeartRate', 'SleepSession'],
      });
      expect(report.batchIds, ['android-1', 'android-2']);
      expect(report.partsUploaded, 3);
      expect(report.duplicateParts, 1);
      expect(report.partsPending, 2);
      expect(report.partsDropped, 1);
      expect(report.linesByType, {'Steps': 40, 'HeartRate': 2});
      expect(report.lines, 42);
      expect(report.deletions, 1);
      expect(report.skippedRecords, 4);
      expect(report.typesWithoutPermission, {HealthDataType.sleep});
      expect(report.expiredTokenTypes, {HealthDataType.heartRate});
      expect(report.unsupportedTypes, {HealthDataType.stateOfMind});
      expect(report.uploadError?.code, 'api_error');
      expect(report.uploadError?.status, 503);
      expect(report.uploadError?.requestId, 'req-1');
    });

    test('reads an iOS report', () async {
      debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
      answerWith(
        (call) => {
          'batchIds': ['ios-1'],
          'partsUploaded': 1,
          'linesByType': {'HKQuantityTypeIdentifierStepCount': 12},
          'deletionsPendingByType': {'HKQuantityTypeIdentifierStepCount': 2},
          'skippedRecords': 1,
        },
      );
      final report = await health.sync(types: {HealthDataType.steps});

      expect(calls.single.arguments, {
        'types': ['HKQuantityTypeIdentifierStepCount'],
      });
      expect(report.lines, 12);
      expect(report.partsPending, 0);
      expect(
        report.deletionsPendingByType,
        {'HKQuantityTypeIdentifierStepCount': 2},
      );
      expect(report.typesWithoutPermission, isEmpty);
    });

    test('with only types the platform lacks does not call it', () async {
      final report = await health.sync(types: {HealthDataType.audiogram});
      expect(report.unsupportedTypes, {HealthDataType.audiogram});
      expect(calls, isEmpty);
    });

    test('turns a channel error into a HealthSyncException', () async {
      answerWith(
        (call) => throw PlatformException(
          code: 'api_error',
          message: 'HTTP 401 Unauthorized',
          details: {'status': 401, 'requestId': 'req-9'},
        ),
      );
      HealthSyncException? error;
      try {
        await health.sync();
      } on HealthSyncException catch (e) {
        error = e;
      }
      expect(error?.code, 'api_error');
      expect(error?.status, 401);
      expect(error?.requestId, 'req-9');
      expect(error?.message, 'HTTP 401 Unauthorized');
      expect(error.toString(), contains('req-9'));
    });

    test('keeps the code of an error without details', () async {
      answerWith((call) => throw PlatformException(code: 'not_configured'));
      await expectLater(health.sync(), _throwsCode('not_configured'));
    });
  });

  group('queue', () {
    test('flush passes force and reads the report', () async {
      answerWith((call) => {'partsUploaded': 2, 'partsPending': 0});
      final report = await health.flush(force: true);
      expect(calls.single.method, 'flush');
      expect(calls.single.arguments, {'force': true});
      expect(report.partsUploaded, 2);
    });

    test('pendingParts reads the count', () async {
      answerWith((call) => 7);
      expect(await health.pendingParts(), 7);
      expect(calls.single.method, 'pendingParts');
    });
  });

  group('background sync', () {
    test('enable sends the types, the interval and the network rule', () async {
      await health.enableBackgroundSync(
        types: {HealthDataType.steps, HealthDataType.workouts},
        interval: const Duration(minutes: 30),
        requireUnmeteredNetwork: true,
      );
      expect(calls.single.method, 'enableBackgroundSync');
      expect(calls.single.arguments, {
        'types': ['Steps', 'ExerciseSession'],
        'intervalSeconds': 1800,
        'requireUnmeteredNetwork': true,
      });
    });

    test('enable without types means all, every hour', () async {
      await health.enableBackgroundSync();
      expect(calls.single.arguments, {
        'types': null,
        'intervalSeconds': 3600,
        'requireUnmeteredNetwork': false,
      });
    });

    test('disable reaches the native side', () async {
      await health.disableBackgroundSync();
      expect(calls.single.method, 'disableBackgroundSync');
    });

    test('results arrive as reports and errors', () async {
      messenger.setMockStreamHandler(
        events,
        MockStreamHandler.inline(
          onListen: (arguments, sink) {
            sink.success({
              'at': 1767225600000,
              'report': {
                'partsUploaded': 1,
                'linesByType': {'Steps': 5},
              },
            });
            sink.success({
              'at': 1767229200000,
              'error': {'code': 'permission_denied', 'message': 'no access'},
            });
            sink.endOfStream();
          },
        ),
      );
      final plugin = OpenHealthSyncHealth(methods: methods, events: events);
      final results = await plugin.backgroundSyncResults.toList();

      expect(results, hasLength(2));
      expect(results[0].isSuccess, isTrue);
      expect(results[0].at, DateTime.utc(2026, 1, 1));
      expect(results[0].report?.lines, 5);
      expect(results[1].isSuccess, isFalse);
      expect(results[1].report, isNull);
      expect(results[1].error?.code, 'permission_denied');
    });
  });
}
