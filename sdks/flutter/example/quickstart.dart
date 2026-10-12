// SPDX-License-Identifier: MIT

// Uploads one heart rate reading and reads it back.
//
//   OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... dart run example/quickstart.dart
//
// Exits 0 when exactly one heart rate sample is read back, 1 otherwise. It
// prints no token and no health value, only counts.

import 'dart:io';

import 'package:open_health_sync/open_health_sync.dart';

Future<void> main() async {
  final url = Platform.environment['OHS_URL'];
  final key = Platform.environment['OHS_KEY'];
  if (url == null || url.isEmpty || key == null || key.isEmpty) {
    stderr.writeln('set OHS_URL and OHS_KEY (a tenant API key)');
    exit(2);
  }

  // Server side: the tenant key creates the user and mints a user token.
  final tenant = OhsClient(baseUrl: url, token: key);
  // App side: the user token uploads.
  OhsClient? app;
  try {
    final batch = newBatchId('quickstart');
    final user = await tenant.createUser(externalRef: batch);
    print('created user ${user.id}');
    final token = await tenant.mintUserToken(user.id, ['upload']);
    app = OhsClient(baseUrl: url, token: token.token);

    final now = DateTime.now().toUtc();
    final builder = AppleHealthPartBuilder(batch)
      ..add(
        'HKQuantityTypeIdentifierHeartRate',
        localDay(now, utcOffset: Duration.zero),
        {
          'quantityType': 'HKQuantityTypeIdentifierHeartRate',
          'startDate': formatTimestamp(now),
          'endDate': formatTimestamp(now),
          'value': 72,
          'unit': 'count/min',
          'metadata': {'HKTimeZone': 'UTC'},
        },
      );
    final results = await app.uploadParts(builder.build());
    print('uploaded ${results.length} part(s)');

    final deadline = DateTime.now().add(const Duration(seconds: 120));
    var coverage = await tenant.getCoverage(user.id);
    while (coverage.pendingJobs > 0) {
      if (DateTime.now().isAfter(deadline)) {
        stderr
            .writeln('timed out: ${coverage.pendingJobs} job(s) still pending');
        exit(1);
      }
      await Future<void>.delayed(const Duration(seconds: 1));
      coverage = await tenant.getCoverage(user.id);
    }
    print('processed, ${coverage.rejectedRecords} rejected record(s)');

    final count = await paginate(
      (cursor) =>
          tenant.listSamples(user.id, type: 'heart_rate', cursor: cursor),
    ).length;
    print('heart_rate samples: $count');
    if (count != 1) exit(1);
  } on OhsApiException catch (e) {
    stderr.writeln('API error: $e');
    exit(1);
  } finally {
    app?.close();
    tenant.close();
  }
}
