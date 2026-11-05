# Flutter SDK

The Dart client for the [REST API](../api.md), for Flutter apps on iOS, Android, desktop and web,
and for Dart backends. It is a plain Dart package with no Flutter dependency: `http` for requests
and `archive` for gzip, so it runs wherever Dart 3.4+ runs, the browser included. It follows the
shape every SDK shares ([overview](README.md)). AGPL-3.0-only, like the server ([LICENSE](../../LICENSE)).

## Install

Nothing is published yet. Add the package from this repository, by path or
by git:

```yaml
# pubspec.yaml of your app
dependencies:
  open_health_sync:
    path: ../open-health-sync/sdks/flutter
  # or
  open_health_sync:
    git:
      url: https://github.com/soma-intelligence/open-health-sync
      path: sdks/flutter
```

Then `flutter pub get` (or `dart pub get`). Everything is in one import:

```dart
import 'package:open_health_sync/open_health_sync.dart';
```

## Create a client

In an app, the client holds a user token with the `upload` scope that your server minted, never
the tenant key ([why](README.md#which-credential-goes-where)). On the REST API that token can
upload and nothing else: every other route answers 403 to it. Give it a token provider: it is
called before each request, so it can return a cached token and fetch a new one when it is about
to expire.

```dart
final uploads = OhsClient(
  baseUrl: 'https://ohs.example.org',
  tokenProvider: () => myBackend.currentUploadToken(), // your own Future<String> function
);
```

`OhsClient(baseUrl: ..., token: ...)` takes a fixed credential instead, which suits a Dart
backend holding the tenant key (`ohs` in the examples below), and tests. Give exactly one of the two. `baseUrl` is a `String` or a
`Uri` and may carry a path prefix. Other named arguments:

| Argument | |
|---|---|
| `httpClient` | an `http.Client` to send requests through (a `MockClient` in tests, a client with your own timeouts or proxy). `close()` closes only a client the SDK created itself. |
| `userAgent` | default `open-health-sync-flutter/0.1.0`; `null` sends none. Browsers may drop it. |
| `sleep` | the wait between upload retries, `Future.delayed` by default; tests pass one that returns at once. |

## Read data

Reads take the tenant key, so they run on your backend; the app gets the records from it. Every
list call returns one `Page<T>` (`data`, `nextCursor`). `start` and `end` take an RFC 3339
time or a `YYYY-MM-DD` date as a string, passed as is; the window is half open.
`formatTimestamp(DateTime)` turns a `DateTime` into an RFC 3339 UTC string.

```dart
final page = await ohs.listSamples(
  userId,
  type: 'heart_rate',
  start: '2026-01-10',
  end: formatTimestamp(DateTime.utc(2026, 1, 11)),
);
for (final s in page.data) {
  print('${s.startDate} ${s.value} ${s.unit}');
}

// Every page, as a Stream:
await for (final w in paginate((cursor) =>
    ohs.listWorkouts(userId, start: '2026-01-01', cursor: cursor))) {
  print(w.activityType);
}
```

The other read calls are `listCategorySamples`, `listSleep`, `listSleepPhases`, `listWorkouts`,
`getWorkoutRoute`, `listNutrition`, `listDaily`, `getCoverage`, `listConnections` and
`getFreshness`. Records are immutable classes with `fromJson` factories, and every record type
implements `Provenance` (`provider`, `source`, `device`, `ingestedAt`).

Times are UTC `DateTime`s. The server sends up to nine fractional digits and Dart keeps six at
most, so digits after the microsecond are dropped (on the web, `DateTime` keeps milliseconds).
`date` fields (`DailySummary.date`, `CoverageDay.date`) stay `YYYY-MM-DD` strings because they
are the user's local day. `metadata` and `nutrients` are `Map<String, Object?>`. Record ids are
`int`; on the web a Dart `int` is exact up to 2^53, far above the ids a server hands out.

FHIR R4 Observations come back as JSON maps, with the next cursor already parsed:

```dart
final bundle = await ohs.searchFhirObservations(userId, category: 'vital-signs');
for (final o in bundle.observations) {
  print(o['id']);
}
final next = bundle.nextCursor; // null on the last page
// bundle.raw is the whole Bundle; paginate((c) async =>
//   (await ohs.searchFhirObservations(userId, cursor: c)).page) walks every page.
```

## Connect a provider

Your backend asks for the consent URL with the tenant key and returns it to the app, which opens
it in the browser, for example with the `url_launcher` plugin. After consent the provider sends the user to your
`redirect_uri` with `?status=connected|error&provider=<slug>`.

```dart
// Backend (tenant key)
final url = await ohs.startConnection(userId, 'oura', redirectUri: 'myapp://ohs/connected');
// App, with the URL from your backend
await launchUrl(url, mode: LaunchMode.externalApplication);
```

`listConnections` shows the result; `deleteConnection(userId, 'oura')` disconnects.

## Upload

`AppleHealthPartBuilder` turns lines in the server's
[line format](../connectors/apple-health.md#line-formats) into upload parts: one part per type
and local day, at most 8 MiB of NDJSON each, gzip compressed and numbered across the batch.
Uploading needs a token with the `upload` scope.

```dart
final at = DateTime.now().toUtc();
final builder = AppleHealthPartBuilder(newBatchId('ios'))
  ..add(
    'HKQuantityTypeIdentifierHeartRate',
    localDay(at, utcOffset: Duration.zero),
    {
      'quantityType': 'HKQuantityTypeIdentifierHeartRate',
      'startDate': formatTimestamp(at),
      'endDate': formatTimestamp(at),
      'value': 72,
      'unit': 'count/min',
      'metadata': {'HKTimeZone': 'UTC'},
    },
  );
final results = await uploads.uploadParts(builder.build());
```

`newBatchId(prefix)` gives a fresh batch id per run. `localDay(instant)` is the day in the
device's time zone; `localDay(instant, utcOffset: ...)` the day at a given offset. Dart has no
time zone database of its own: for an IANA zone such as a sample's `HKTimeZone`, get its offset
at that instant from a package such as `timezone`. The builder checks the batch, the type and
the day and throws `ArgumentError` when one is invalid. On your backend, poll `getCoverage(userId)`
until `pendingJobs` is 0 to know the server has processed everything.

## Reading HealthKit or Health Connect in Flutter

The SDK does not read device stores itself. A community plugin (for example the `health`
package) reads HealthKit on iOS or Health Connect on Android; you turn each reading into a line
and add it to the builder:

```dart
// A reading as your plugin returns it: start, end and beats per minute.
Map<String, Object?> heartRateLine(DateTime from, DateTime to, num bpm, String zone) => {
      'quantityType': 'HKQuantityTypeIdentifierHeartRate',
      'startDate': formatTimestamp(from),
      'endDate': formatTimestamp(to),
      'value': bpm,
      'unit': 'count/min',
      'metadata': {'HKTimeZone': zone},
    };

builder.add(
  'HKQuantityTypeIdentifierHeartRate',
  localDay(reading.from),
  heartRateLine(reading.from, reading.to, reading.bpm, deviceZoneName),
);
```

The upload route currently takes HealthKit-shaped lines only
([line formats](../connectors/apple-health.md#line-formats)), whatever the device: a Health
Connect reading must be written with the matching HealthKit identifier and unit, and it is stored
under the `apple_health` provider.

## Errors and retries

A non-2xx answer throws `OhsApiException` with `status`, `title`, `detail`, `type`, `requestId`
(quote it in a bug report) and `retryAfterSeconds`. A body that is not JSON still gives the
status and the `X-Request-Id` header. A network failure throws `http.ClientException`, and a
response that does not decode throws `FormatException`.

Uploads are retried on 429, 503, other 5xx answers and network errors, waiting `Retry-After`
when the server sends it and 1, 2, 4, 8 seconds otherwise (at most 30), up to 5 attempts
(`maxUploadAttempts`). The token provider is called again before each attempt. Any other 4xx
throws at once, and other calls are not retried. Neither the token nor a health value appears
in an exception message.

## Run the tests

```sh
cd sdks/flutter
dart pub get
dart format --set-exit-if-changed .
dart analyze --fatal-infos
dart test
```

The tests use `package:http/testing` `MockClient`, no server. They need the Dart SDK only, not
Flutter. One test, tagged `vm`, also checks the gzip bodies with `dart:io`.

## Run the example

[`example/quickstart.dart`](../../sdks/flutter/example/quickstart.dart) runs the README
quickstart: it creates a user, mints a token, uploads one heart rate reading, waits for
processing and reads it back. Point it at a test instance, never at real users' data:

```sh
cd sdks/flutter
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... dart run example/quickstart.dart
```

It exits 1 when it does not read exactly one sample back. It prints counts only, never the token
or a health value.
