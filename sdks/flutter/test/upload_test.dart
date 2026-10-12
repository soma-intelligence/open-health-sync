// SPDX-License-Identifier: AGPL-3.0-only

import 'dart:convert';

import 'package:archive/archive.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:open_health_sync/open_health_sync.dart';
import 'package:test/test.dart';

Map<String, Object?> hrLine(String start, num value) => {
      'quantityType': 'HKQuantityTypeIdentifierHeartRate',
      'startDate': start,
      'endDate': start,
      'value': value,
      'unit': 'count/min',
      'metadata': {'HKTimeZone': 'UTC'},
    };

List<String> gunzipLines(List<int> body) => const LineSplitter()
    .convert(utf8.decode(const GZipDecoder().decodeBytes(body)));

void main() {
  group('part builder', () {
    test('groups by type and day in first appearance order', () {
      final b = AppleHealthPartBuilder('batch-1')
        ..add('HKQuantityTypeIdentifierHeartRate', '2026-01-10',
            hrLine('2026-01-10T00:00:00Z', 60))
        ..add('HKQuantityTypeIdentifierStepCount', '2026-01-10',
            {'quantityType': 'HKQuantityTypeIdentifierStepCount', 'value': 7})
        ..add('HKQuantityTypeIdentifierHeartRate', '2026-01-11',
            hrLine('2026-01-11T00:00:00Z', 61))
        ..add('HKQuantityTypeIdentifierHeartRate', '2026-01-10',
            hrLine('2026-01-10T00:10:00Z', 62));
      final parts = b.build();
      expect(parts.map((p) => (p.type, p.day, p.partNumber, p.lineCount)), [
        ('HKQuantityTypeIdentifierHeartRate', '2026-01-10', 0, 2),
        ('HKQuantityTypeIdentifierStepCount', '2026-01-10', 1, 1),
        ('HKQuantityTypeIdentifierHeartRate', '2026-01-11', 2, 1),
      ]);
      expect(parts.every((p) => p.batch == 'batch-1'), isTrue);
      final lines = gunzipLines(parts[0].body);
      expect(lines, hasLength(2));
      expect(jsonDecode(lines[0]), hrLine('2026-01-10T00:00:00Z', 60));
      expect(jsonDecode(lines[1]), hrLine('2026-01-10T00:10:00Z', 62));
    });

    test('NDJSON is compact with every line ending in a newline', () {
      final b = AppleHealthPartBuilder('b')
        ..add('T', '2026-01-10', {'a': 1, 'b': 'x y'});
      final p = b.build().single;
      final text = utf8.decode(const GZipDecoder().decodeBytes(p.body));
      expect(text, '{"a":1,"b":"x y"}\n');
      expect(p.uncompressedBytes, text.length);
    });

    test('splits so no part exceeds maxPartBytes', () {
      final line = hrLine('2026-01-10T00:00:00Z', 60);
      final lineBytes = utf8.encode('${jsonEncode(line)}\n').length;
      final b = AppleHealthPartBuilder('b', maxPartBytes: lineBytes * 3);
      for (var i = 0; i < 7; i++) {
        b.add('HKQuantityTypeIdentifierHeartRate', '2026-01-10', line);
      }
      b.add('HKQuantityTypeIdentifierStepCount', '2026-01-10', line);
      final parts = b.build();
      expect(parts.map((p) => p.lineCount), [3, 3, 1, 1]);
      expect(parts.map((p) => p.partNumber), [0, 1, 2, 3]);
      for (final p in parts) {
        expect(p.uncompressedBytes, lessThanOrEqualTo(lineBytes * 3));
        expect(gunzipLines(p.body), hasLength(p.lineCount));
      }
    });

    test('a line larger than the max goes alone in its own part', () {
      final b = AppleHealthPartBuilder('b', maxPartBytes: 40)
        ..add('T', '2026-01-10', {'s': 'x'})
        ..add('T', '2026-01-10', {'s': 'y' * 100})
        ..add('T', '2026-01-10', {'s': 'z'});
      final parts = b.build();
      expect(parts.map((p) => p.lineCount), [1, 1, 1]);
      expect(parts[1].uncompressedBytes, greaterThan(40));
    });

    test('validates batch, type and day', () {
      expect(() => AppleHealthPartBuilder(''), throwsArgumentError);
      expect(() => AppleHealthPartBuilder('a b'), throwsArgumentError);
      expect(() => AppleHealthPartBuilder('x' * 129), throwsArgumentError);
      final b = AppleHealthPartBuilder('ok_-1');
      expect(() => b.add('HK.Type', '2026-01-10', {}), throwsArgumentError);
      expect(() => b.add('T', '2026-1-10', {}), throwsArgumentError);
      expect(() => b.add('T', '2026-02-30', {}), throwsArgumentError);
      expect(() => b.add('T', '2026-01-10T00:00:00Z', {}), throwsArgumentError);
      expect(b.isEmpty, isTrue);
    });

    test('localDay', () {
      final t = DateTime.utc(2026, 1, 10, 23, 30);
      expect(localDay(t, utcOffset: Duration.zero), '2026-01-10');
      expect(localDay(t, utcOffset: const Duration(hours: 1)), '2026-01-11');
      expect(localDay(t, utcOffset: const Duration(hours: -10)), '2026-01-10');
      expect(localDay(DateTime.utc(26, 3, 4), utcOffset: Duration.zero),
          '0026-03-04');
      expect(isValidDay(localDay(DateTime.now())), isTrue);
    });

    test('newBatchId', () {
      final a = newBatchId('ios');
      final b = newBatchId('ios');
      expect(a, matches(RegExp(r'^ios-[0-9a-f]{20}$')));
      expect(a, isNot(b));
      expect(newBatchId(), startsWith('app-'));
      expect(() => newBatchId('bad prefix'), throwsArgumentError);
      expect(newBatchId('x' * 107).length, 128);
    });
  });

  group('upload', () {
    AppleHealthPart onePart() => (AppleHealthPartBuilder('batch-1')
          ..add('HKQuantityTypeIdentifierHeartRate', '2026-01-10',
              hrLine('2026-01-10T00:00:00Z', 72)))
        .build()
        .single;

    test('sends the headers and gzip body', () async {
      final requests = <http.Request>[];
      final c = OhsClient(
        baseUrl: 'https://ohs.example.org',
        token: 'ohs_ut_x',
        httpClient: MockClient((r) async {
          requests.add(r);
          return http.Response(
              '{"key":"raw/t/u/apple_health/2026-01-10/batch-1_0.ndjson.gz","duplicate":false}',
              200);
        }),
      );
      final res = await c.uploadAppleHealthPart(onePart());
      final r = requests.single;
      expect(r.method, 'POST');
      expect(r.url.path, '/v1/ingest/apple-health');
      expect(r.headers['Content-Type'], 'application/gzip');
      expect(r.headers['X-OHS-Type'], 'HKQuantityTypeIdentifierHeartRate');
      expect(r.headers['X-OHS-Day'], '2026-01-10');
      expect(r.headers['X-OHS-Batch'], 'batch-1');
      expect(r.headers['X-OHS-Part'], '0');
      expect(r.headers['Authorization'], 'Bearer ohs_ut_x');
      expect(jsonDecode(gunzipLines(r.bodyBytes).single),
          hrLine('2026-01-10T00:00:00Z', 72));
      expect(res.duplicate, isFalse);
      expect(res.key, endsWith('batch-1_0.ndjson.gz'));
    });

    test('503 with Retry-After then 200', () async {
      final sleeps = <Duration>[];
      var calls = 0;
      final c = OhsClient(
        baseUrl: 'https://ohs.example.org',
        token: 't',
        sleep: (d) async => sleeps.add(d),
        httpClient: MockClient((r) async {
          calls++;
          if (calls == 1) {
            return http.Response(
                '{"title":"Service Unavailable","status":503}', 503,
                headers: {'retry-after': '3'});
          }
          return http.Response('{"key":"k","duplicate":true}', 200);
        }),
      );
      final res = await c.uploadAppleHealthPart(onePart());
      expect(calls, 2);
      expect(sleeps, [const Duration(seconds: 3)]);
      expect(res.duplicate, isTrue);
    });

    test('400 is not retried', () async {
      var calls = 0;
      final c = OhsClient(
        baseUrl: 'https://ohs.example.org',
        token: 't',
        sleep: (_) async {},
        httpClient: MockClient((r) async {
          calls++;
          return http.Response(
              '{"title":"Bad Request","status":400,"detail":"X-OHS-Day is invalid"}',
              400);
        }),
      );
      await expectLater(
        c.uploadAppleHealthPart(onePart()),
        throwsA(isA<OhsApiException>().having((e) => e.status, 'status', 400)),
      );
      expect(calls, 1);
    });

    test('gives up after 5 attempts with exponential backoff', () async {
      final sleeps = <Duration>[];
      var calls = 0;
      final c = OhsClient(
        baseUrl: 'https://ohs.example.org',
        token: 't',
        sleep: (d) async => sleeps.add(d),
        httpClient: MockClient((r) async {
          calls++;
          return http.Response('oops', 500);
        }),
      );
      await expectLater(
        c.uploadAppleHealthPart(onePart()),
        throwsA(isA<OhsApiException>().having((e) => e.status, 'status', 500)),
      );
      expect(calls, 5);
      expect(sleeps.map((d) => d.inSeconds), [1, 2, 4, 8]);
    });

    test('429 and network errors are retried', () async {
      final sleeps = <Duration>[];
      var calls = 0;
      final c = OhsClient(
        baseUrl: 'https://ohs.example.org',
        token: 't',
        sleep: (d) async => sleeps.add(d),
        httpClient: MockClient((r) async {
          calls++;
          if (calls == 1) throw http.ClientException('connection reset');
          if (calls == 2) return http.Response('', 429);
          return http.Response('{"key":"k","duplicate":false}', 200);
        }),
      );
      await c.uploadAppleHealthPart(onePart());
      expect(calls, 3);
      expect(sleeps.map((d) => d.inSeconds), [1, 2]);
    });

    test('network errors give up after 5 attempts', () async {
      var calls = 0;
      final c = OhsClient(
        baseUrl: 'https://ohs.example.org',
        token: 't',
        sleep: (_) async {},
        httpClient: MockClient((r) async {
          calls++;
          throw http.ClientException('offline');
        }),
      );
      await expectLater(c.uploadAppleHealthPart(onePart()),
          throwsA(isA<http.ClientException>()));
      expect(calls, 5);
    });

    test('token provider is called for each attempt', () async {
      var tokens = 0;
      var calls = 0;
      final c = OhsClient(
        baseUrl: 'https://ohs.example.org',
        tokenProvider: () async => 'ohs_ut_${++tokens}',
        sleep: (_) async {},
        httpClient: MockClient((r) async {
          calls++;
          return calls == 1
              ? http.Response('', 503)
              : http.Response('{"key":"k","duplicate":false}', 200);
        }),
      );
      await c.uploadAppleHealthPart(onePart());
      expect(tokens, 2);
    });

    test('uploadParts goes in order', () async {
      final seen = <String>[];
      final c = OhsClient(
        baseUrl: 'https://ohs.example.org',
        token: 't',
        httpClient: MockClient((r) async {
          seen.add(r.headers['X-OHS-Part']!);
          return http.Response(
              '{"key":"k${r.headers['X-OHS-Part']}","duplicate":false}', 200);
        }),
      );
      final b = AppleHealthPartBuilder('b', maxPartBytes: 1);
      for (var i = 0; i < 3; i++) {
        b.add('T', '2026-01-10', {'i': i});
      }
      final res = await c.uploadParts(b.build());
      expect(seen, ['0', '1', '2']);
      expect(res.map((r) => r.key), ['k0', 'k1', 'k2']);
    });
  });
}
