// SPDX-License-Identifier: AGPL-3.0-only

@TestOn('vm')
@Tags(['vm'])
library;

import 'dart:convert';
import 'dart:io';

import 'package:open_health_sync/open_health_sync.dart';
import 'package:test/test.dart';

void main() {
  test('dart:io gzip reads a part body', () {
    final b = AppleHealthPartBuilder('b')
      ..add('HKQuantityTypeIdentifierHeartRate', '2026-01-10', {'value': 72})
      ..add('HKQuantityTypeIdentifierHeartRate', '2026-01-10', {'value': 73});
    final p = b.build().single;
    expect(p.body.sublist(0, 2), [0x1f, 0x8b]);
    expect(utf8.decode(gzip.decode(p.body)), '{"value":72}\n{"value":73}\n');
  });
}
