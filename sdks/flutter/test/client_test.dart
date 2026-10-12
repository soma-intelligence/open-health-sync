// SPDX-License-Identifier: MIT

import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:open_health_sync/open_health_sync.dart';
import 'package:test/test.dart';

import 'fixtures.dart';

http.Response jsonResponse(Object body,
        {int status = 200, Map<String, String>? headers}) =>
    http.Response(
      jsonEncode(body),
      status,
      headers: {'content-type': 'application/json', ...?headers},
    );

class Recorder {
  final List<http.Request> requests = [];
}

OhsClient clientWith(
  Future<http.Response> Function(http.Request) handler, {
  Recorder? recorder,
  List<Duration>? sleeps,
  String base = 'https://ohs.example.org',
}) =>
    OhsClient(
      baseUrl: base,
      token: 'ohs_sk_test',
      httpClient: MockClient((r) {
        recorder?.requests.add(r);
        return handler(r);
      }),
      sleep: (d) async => sleeps?.add(d),
    );

void main() {
  group('construction', () {
    test('needs exactly one of token and tokenProvider', () {
      expect(() => OhsClient(baseUrl: 'https://x.org'), throwsArgumentError);
      expect(
        () => OhsClient(
          baseUrl: 'https://x.org',
          token: 'a',
          tokenProvider: () async => 'b',
        ),
        throwsArgumentError,
      );
    });

    test('rejects a relative base URL', () {
      expect(() => OhsClient(baseUrl: '/v1', token: 't'), throwsArgumentError);
    });

    test('accepts a Uri base with a path prefix and a trailing slash',
        () async {
      final rec = Recorder();
      final c = OhsClient(
        baseUrl: Uri.parse('https://ohs.example.org/api/'),
        token: 't',
        httpClient: MockClient((r) async {
          rec.requests.add(r);
          return jsonResponse({'data': []});
        }),
      );
      await c.listProviders();
      expect(
        rec.requests.single.url.toString(),
        'https://ohs.example.org/api/v1/providers',
      );
    });
  });

  group('headers', () {
    test('auth, accept and user agent', () async {
      final rec = Recorder();
      final c =
          clientWith((_) async => jsonResponse({'data': []}), recorder: rec);
      await c.listProviders();
      final r = rec.requests.single;
      expect(r.method, 'GET');
      expect(r.headers['Authorization'], 'Bearer ohs_sk_test');
      expect(r.headers['Accept'], 'application/json');
      expect(r.headers['User-Agent'], 'open-health-sync-flutter/0.1.0');
    });

    test('userAgent null sends none', () async {
      final rec = Recorder();
      final c = OhsClient(
        baseUrl: 'https://x.org',
        token: 't',
        userAgent: null,
        httpClient: MockClient((r) async {
          rec.requests.add(r);
          return jsonResponse({'data': []});
        }),
      );
      await c.listProviders();
      expect(rec.requests.single.headers.containsKey('User-Agent'), isFalse);
    });

    test('token provider is called before each request', () async {
      var calls = 0;
      final seen = <String?>[];
      final c = OhsClient(
        baseUrl: 'https://x.org',
        tokenProvider: () async => 'ohs_ut_${++calls}',
        httpClient: MockClient((r) async {
          seen.add(r.headers['Authorization']);
          return jsonResponse({'data': []});
        }),
      );
      await c.listProviders();
      await c.listProviders();
      expect(calls, 2);
      expect(seen, ['Bearer ohs_ut_1', 'Bearer ohs_ut_2']);
    });
  });

  group('users and tokens', () {
    test('createUser sends body and idempotency key', () async {
      final rec = Recorder();
      final c = clientWith(
        (_) async => jsonResponse({
          'id': userId,
          'external_ref': 'u-42',
          'status': 'active',
          'created_at': '2026-01-10T14:30:00.123456789Z',
        }, status: 201),
        recorder: rec,
      );
      final u = await c.createUser(externalRef: 'u-42', idempotencyKey: 'k-1');
      final r = rec.requests.single;
      expect(r.method, 'POST');
      expect(r.url.path, '/v1/users');
      expect(r.headers['Idempotency-Key'], 'k-1');
      expect(r.headers['Content-Type'], startsWith('application/json'));
      expect(jsonDecode(r.body), {'external_ref': 'u-42'});
      expect(u.id, userId);
      expect(u.externalRef, 'u-42');
      expect(u.status, 'active');
      expect(u.createdAt, DateTime.utc(2026, 1, 10, 14, 30, 0, 123, 456));
    });

    test('createUser without arguments sends an empty object', () async {
      final rec = Recorder();
      final c = clientWith(
        (_) async => jsonResponse({
          'id': userId,
          'external_ref': null,
          'status': 'active',
          'created_at': '2026-01-10T14:30:00Z',
        }, status: 201),
        recorder: rec,
      );
      final u = await c.createUser();
      expect(jsonDecode(rec.requests.single.body), <String, Object?>{});
      expect(
          rec.requests.single.headers.containsKey('Idempotency-Key'), isFalse);
      expect(u.externalRef, isNull);
    });

    test('listUsers sends only the set query parameters', () async {
      final rec = Recorder();
      final c =
          clientWith((_) async => jsonResponse(page([], null)), recorder: rec);
      await c.listUsers();
      expect(rec.requests.last.url.query, '');
      await c.listUsers(cursor: 'a b/c', limit: 50);
      expect(rec.requests.last.url.queryParameters,
          {'cursor': 'a b/c', 'limit': '50'});
      expect(rec.requests.last.url.query, contains('cursor=a+b%2Fc'));
    });

    test('getUser encodes the path segment', () async {
      final rec = Recorder();
      final c = clientWith(
        (_) async => jsonResponse({
          'id': 'a/b',
          'external_ref': null,
          'status': 'deleting',
          'created_at': '2026-01-10T14:30:00Z',
        }),
        recorder: rec,
      );
      await c.getUser('a/b');
      expect(rec.requests.single.url.toString(),
          'https://ohs.example.org/v1/users/a%2Fb');
    });

    test('deleteUser and getDeletion', () async {
      final rec = Recorder();
      final c = clientWith((r) async {
        if (r.method == 'DELETE') {
          return jsonResponse({'job_id': 'job-1'}, status: 202);
        }
        return jsonResponse({
          'job_id': 'job-1',
          'user_id': userId,
          'status': 'done',
          'created_at': '2026-01-10T14:30:00Z',
          'finished_at': '2026-01-10T14:30:05.5Z',
        });
      }, recorder: rec);
      expect(await c.deleteUser(userId), 'job-1');
      final job = await c.getDeletion('job-1');
      expect(rec.requests[0].method, 'DELETE');
      expect(rec.requests[0].url.path, '/v1/users/$userId');
      expect(rec.requests[1].url.path, '/v1/deletions/job-1');
      expect(job.status, 'done');
      expect(job.finishedAt, DateTime.utc(2026, 1, 10, 14, 30, 5, 500));
    });

    test('mintUserToken', () async {
      final rec = Recorder();
      final c = clientWith(
        (_) async => jsonResponse({
          'token': 'ohs_ut_secret',
          'expires_at': '2026-01-10T15:30:00Z',
          'scopes': ['upload', 'read'],
        }, status: 201),
        recorder: rec,
      );
      final t =
          await c.mintUserToken(userId, ['upload', 'read'], ttlSeconds: 600);
      expect(rec.requests.single.url.path, '/v1/users/$userId/tokens');
      expect(jsonDecode(rec.requests.single.body), {
        'scopes': ['upload', 'read'],
        'ttl_seconds': 600,
      });
      expect(t.token, 'ohs_ut_secret');
      expect(t.scopes, ['upload', 'read']);
      expect(t.toString(), isNot(contains('ohs_ut_secret')));
    });
  });

  group('providers and connections', () {
    test('listProviders', () async {
      final c = clientWith((_) async => jsonResponse({
            'data': [
              {
                'slug': 'apple_health',
                'kinds': ['device']
              },
              {
                'slug': 'demo',
                'kinds': ['oauth', 'push']
              },
            ],
          }));
      final p = await c.listProviders();
      expect(p.map((e) => e.slug), ['apple_health', 'demo']);
      expect(p[1].kinds, ['oauth', 'push']);
    });

    test('listConnections and getFreshness', () async {
      final c = clientWith((r) async {
        if (r.url.path.endsWith('/connections')) {
          return jsonResponse({
            'data': [
              {
                'provider': 'demo',
                'status': 'active',
                'scopes': ['daily', 'heartrate'],
                'last_sync_at': '2026-01-10T14:00:00Z',
                'initial_backfill_at': null,
                'created_at': '2026-01-01T00:00:00Z',
                'updated_at': '2026-01-10T14:00:00Z',
              },
            ],
          });
        }
        return jsonResponse({
          'as_of': '2026-01-10T15:00:00Z',
          'data': [
            {
              'provider': 'demo',
              'kind': 'oauth',
              'status': 'active',
              'state': 'fresh',
              'synced_through': '2026-01-10T14:00:00Z',
              'lag_seconds': 3600,
              'last_sync_at': '2026-01-10T14:00:00Z',
              'last_event_at': null,
              'push_healthy': true,
              'pull_every_seconds': 21600,
              'next_pull_at': '2026-01-10T20:00:00Z',
              'last_attempt_at': '2026-01-10T14:00:00Z',
              'last_error': null,
              'consecutive_failures': 0,
            },
          ],
        });
      });
      final conns = await c.listConnections(userId);
      expect(conns.single.provider, 'demo');
      expect(conns.single.initialBackfillAt, isNull);
      expect(conns.single.scopes, ['daily', 'heartrate']);
      final f = await c.getFreshness(userId);
      expect(f.asOf, DateTime.utc(2026, 1, 10, 15));
      expect(f.data.single.state, 'fresh');
      expect(f.data.single.lagSeconds, 3600);
      expect(f.data.single.pushHealthy, isTrue);
      expect(f.data.single.pullEverySeconds, 21600);
    });

    test('startConnection and deleteConnection', () async {
      final rec = Recorder();
      final c = clientWith((r) async {
        if (r.method == 'DELETE') return http.Response('', 204);
        return jsonResponse({
          'authorize_url':
              'https://provider.example.org/oauth/authorize?state=x'
        });
      }, recorder: rec);
      final url =
          await c.startConnection(userId, 'demo', redirectUri: 'myapp://done');
      expect(url.host, 'provider.example.org');
      expect(rec.requests[0].url.path, '/v1/users/$userId/connections/demo');
      expect(
          jsonDecode(rec.requests[0].body), {'redirect_uri': 'myapp://done'});
      await c.deleteConnection(userId, 'demo');
      expect(rec.requests[1].method, 'DELETE');
      expect(rec.requests[1].url.path, '/v1/users/$userId/connections/demo');
    });
  });

  group('read routes', () {
    test('listSamples path, query and decoding', () async {
      final rec = Recorder();
      final c = clientWith(
        (_) async => jsonResponse(page([sampleJson()], 'next-1')),
        recorder: rec,
      );
      final p = await c.listSamples(
        userId,
        type: 'heart_rate',
        start: '2026-01-10',
        end: formatTimestamp(DateTime.utc(2026, 1, 11)),
        limit: 10,
      );
      final r = rec.requests.single;
      expect(r.url.path, '/v1/users/$userId/samples');
      expect(r.url.queryParameters, {
        'type': 'heart_rate',
        'start': '2026-01-10',
        'end': '2026-01-11T00:00:00.000Z',
        'limit': '10',
      });
      expect(p.nextCursor, 'next-1');
      expect(p.data.single.value, 72.5);
    });

    test('every list route hits its path', () async {
      final rec = Recorder();
      final c = clientWith((r) async {
        final fixtures = {
          '/category-samples': categorySampleJson,
          '/phases': sleepPhaseJson,
          '/sleep': sleepSessionJson,
          '/route': routePointJson,
          '/workouts': workoutJson,
          '/nutrition': nutritionJson,
          '/daily': dailyJson,
        };
        for (final MapEntry(:key, :value) in fixtures.entries) {
          if (r.url.path.endsWith(key)) return jsonResponse(page([value()]));
        }
        return http.Response('not found', 404);
      }, recorder: rec);
      expect(
          (await c.listCategorySamples(userId, type: 'mindful_session')).data,
          hasLength(1));
      expect((await c.listSleep(userId)).data, hasLength(1));
      expect((await c.listSleepPhases(userId, 77)).data, hasLength(1));
      expect(
          (await c.listWorkouts(userId, type: 'running')).data, hasLength(1));
      expect((await c.getWorkoutRoute(userId, 9001, limit: 1000)).data,
          hasLength(1));
      expect((await c.listNutrition(userId)).data, hasLength(1));
      expect(
          (await c.listDaily(userId, start: '2026-01-01')).data, hasLength(1));
      expect(rec.requests.map((r) => r.url.toString()), [
        'https://ohs.example.org/v1/users/$userId/category-samples?type=mindful_session',
        'https://ohs.example.org/v1/users/$userId/sleep',
        'https://ohs.example.org/v1/users/$userId/sleep/77/phases',
        'https://ohs.example.org/v1/users/$userId/workouts?type=running',
        'https://ohs.example.org/v1/users/$userId/workouts/9001/route?limit=1000',
        'https://ohs.example.org/v1/users/$userId/nutrition',
        'https://ohs.example.org/v1/users/$userId/daily?start=2026-01-01',
      ]);
    });

    test('getCoverage', () async {
      final rec = Recorder();
      final c = clientWith(
        (_) async => jsonResponse({
          'pending_jobs': 2,
          'parked_jobs': 0,
          'rejected_records': 1,
          'truncated': false,
          'days': [
            {
              'provider': 'apple_health',
              'date': '2026-01-10',
              'records': 3,
              'metrics': {'heart_rate': 2, 'step_count': 1},
              'last_synced_at': '2026-01-10T14:31:02.1Z',
            },
          ],
        }),
        recorder: rec,
      );
      final cov = await c.getCoverage(userId, provider: 'apple_health');
      expect(rec.requests.single.url.queryParameters,
          {'provider': 'apple_health'});
      expect(cov.pendingJobs, 2);
      expect(cov.rejectedRecords, 1);
      expect(cov.days.single.date, '2026-01-10');
      expect(cov.days.single.metrics, {'heart_rate': 2, 'step_count': 1});
    });
  });

  group('pagination', () {
    test('paginate walks every page', () async {
      final cursors = <String?>[];
      final c = clientWith((r) async {
        final cursor = r.url.queryParameters['cursor'];
        cursors.add(cursor);
        return switch (cursor) {
          null =>
            jsonResponse(page([sampleJson(id: 1), sampleJson(id: 2)], 'c2')),
          'c2' => jsonResponse(page([sampleJson(id: 3)], 'c3')),
          _ => jsonResponse(page([sampleJson(id: 4)], null)),
        };
      });
      final ids = await paginate(
        (cursor) => c.listSamples(userId, type: 'heart_rate', cursor: cursor),
      ).map((s) => s.id).toList();
      expect(ids, [1, 2, 3, 4]);
      expect(cursors, [null, 'c2', 'c3']);
    });

    test('paginate on an empty first page', () async {
      final c = clientWith((_) async => jsonResponse(page([], null)));
      expect(await paginate((cur) => c.listSleep(userId, cursor: cur)).toList(),
          isEmpty);
    });
  });

  group('FHIR', () {
    Map<String, Object?> bundle({String? next}) => {
          'resourceType': 'Bundle',
          'type': 'searchset',
          'link': [
            {
              'relation': 'self',
              'url': 'https://ohs.example.org/v1/users/$userId/fhir/Observation'
            },
            if (next != null)
              {
                'relation': 'next',
                'url':
                    'https://ohs.example.org/v1/users/$userId/fhir/Observation?code=http%3A%2F%2Floinc.org%7C8867-4&cursor=$next',
              },
          ],
          'entry': [
            {
              'fullUrl':
                  'https://ohs.example.org/v1/users/$userId/fhir/Observation/qs-1234',
              'resource': {
                'resourceType': 'Observation',
                'id': 'qs-1234',
                'status': 'final',
                'code': {
                  'coding': [
                    {'system': 'http://loinc.org', 'code': '8867-4'},
                  ],
                },
                'subject': {'reference': 'Patient/$userId'},
                'valueQuantity': {
                  'value': 72.5,
                  'unit': 'beats/minute',
                  'system': 'http://unitsofmeasure.org',
                  'code': '/min'
                },
              },
              'search': {'mode': 'match'},
            },
          ],
        };

    test('search sends filters and parses the next cursor', () async {
      final rec = Recorder();
      final c = clientWith(
        (_) async => http.Response(jsonEncode(bundle(next: 'abc%3D')), 200,
            headers: {'content-type': 'application/fhir+json'}),
        recorder: rec,
      );
      final b = await c.searchFhirObservations(
        userId,
        code: 'http://loinc.org|8867-4',
        category: 'vital-signs',
      );
      final r = rec.requests.single;
      expect(r.url.path, '/v1/users/$userId/fhir/Observation');
      expect(r.url.queryParameters,
          {'code': 'http://loinc.org|8867-4', 'category': 'vital-signs'});
      expect(r.headers['Accept'], startsWith('application/fhir+json'));
      expect(b.nextCursor, 'abc=');
      expect(b.observations.single['id'], 'qs-1234');
      expect(b.raw['resourceType'], 'Bundle');
      expect(b.page.data, hasLength(1));
    });

    test('last page has no next cursor and an empty page has no entry',
        () async {
      final c = clientWith((_) async => jsonResponse({
            'resourceType': 'Bundle',
            'type': 'searchset',
            'link': [
              {'relation': 'self', 'url': 'https://ohs.example.org/x'},
            ],
          }));
      final b = await c.searchFhirObservations(userId);
      expect(b.nextCursor, isNull);
      expect(b.observations, isEmpty);
    });

    test('readFhirObservation returns the raw resource', () async {
      final rec = Recorder();
      final c = clientWith(
        (_) async =>
            jsonResponse({'resourceType': 'Observation', 'id': 'bp-1235'}),
        recorder: rec,
      );
      final o = await c.readFhirObservation(userId, 'bp-1235');
      expect(rec.requests.single.url.path,
          '/v1/users/$userId/fhir/Observation/bp-1235');
      expect(o['id'], 'bp-1235');
    });
  });

  group('errors', () {
    test('problem+json with request_id', () async {
      final c = clientWith((_) async => http.Response(
            jsonEncode({
              'type': 'about:blank',
              'title': 'Not Found',
              'status': 404,
              'detail': 'no such user',
              'request_id': 'req-body',
            }),
            404,
            headers: {
              'content-type': 'application/problem+json',
              'x-request-id': 'req-header',
            },
          ));
      try {
        await c.getUser(userId);
        fail('expected an exception');
      } on OhsApiException catch (e) {
        expect(e.status, 404);
        expect(e.title, 'Not Found');
        expect(e.detail, 'no such user');
        expect(e.type, 'about:blank');
        expect(e.requestId, 'req-body');
        expect(e.toString(), contains('req-body'));
        expect(e.toString(), isNot(contains('ohs_sk_test')));
      }
    });

    test('non-JSON body still gives the status and header request id',
        () async {
      final c = clientWith((_) async => http.Response(
            '<html>bad gateway</html>',
            502,
            headers: {'x-request-id': 'req-h', 'retry-after': '7'},
          ));
      await expectLater(
        c.listProviders(),
        throwsA(isA<OhsApiException>()
            .having((e) => e.status, 'status', 502)
            .having((e) => e.title, 'title', isNull)
            .having((e) => e.requestId, 'requestId', 'req-h')
            .having((e) => e.retryAfterSeconds, 'retryAfter', 7)),
      );
    });

    test('read routes are not retried', () async {
      var calls = 0;
      final c = clientWith((_) async {
        calls++;
        return http.Response('', 503);
      });
      await expectLater(c.listProviders(), throwsA(isA<OhsApiException>()));
      expect(calls, 1);
    });
  });
}
