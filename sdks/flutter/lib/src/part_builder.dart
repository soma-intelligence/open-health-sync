// SPDX-License-Identifier: AGPL-3.0-only

import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';

import 'package:archive/archive.dart';

/// Default largest uncompressed size of one part: 8 MiB.
const int defaultMaxPartBytes = 8 * 1024 * 1024;

final RegExp _idPattern = RegExp(r'^[A-Za-z0-9_-]{1,128}$');
final RegExp _dayPattern = RegExp(r'^(\d{4})-(\d{2})-(\d{2})$');

/// One upload part: gzip NDJSON of one HealthKit type and one local day.
class AppleHealthPart {
  const AppleHealthPart({
    required this.type,
    required this.day,
    required this.batch,
    required this.partNumber,
    required this.body,
    required this.lineCount,
    required this.uncompressedBytes,
  });

  /// HealthKit type identifier of every line, sent as `X-OHS-Type`.
  final String type;

  /// `YYYY-MM-DD`, sent as `X-OHS-Day`.
  final String day;

  /// Sent as `X-OHS-Batch`.
  final String batch;

  /// Sent as `X-OHS-Part`; unique within the batch.
  final int partNumber;

  /// The gzip compressed NDJSON.
  final Uint8List body;

  /// Number of lines in the part.
  final int lineCount;

  /// Size of the NDJSON before compression.
  final int uncompressedBytes;

  @override
  String toString() => 'AppleHealthPart($type, $day, $batch, '
      'part $partNumber, $lineCount lines, ${body.length} bytes)';
}

/// Turns lines in the server's line format into upload parts.
///
/// Lines are grouped by (type, day) in the order each group first appears.
/// Each group's NDJSON is split so a part never holds more than
/// [maxPartBytes] uncompressed (a single larger line goes alone in its own
/// part). Part numbers start at 0 and increase across all groups.
class AppleHealthPartBuilder {
  AppleHealthPartBuilder(this.batch,
      {this.maxPartBytes = defaultMaxPartBytes}) {
    if (!_idPattern.hasMatch(batch)) {
      throw ArgumentError.value(
        batch,
        'batch',
        'must be 1 to 128 letters, digits, "_" or "-"',
      );
    }
    if (maxPartBytes < 1) {
      throw ArgumentError.value(maxPartBytes, 'maxPartBytes', 'must be > 0');
    }
  }

  final String batch;
  final int maxPartBytes;

  final Map<(String, String), List<Uint8List>> _groups = {};

  /// Adds one [line] (a JSON object) of HealthKit [type] on local [day].
  void add(String type, String day, Map<String, Object?> line) {
    if (!_idPattern.hasMatch(type)) {
      throw ArgumentError.value(
        type,
        'type',
        'must be 1 to 128 letters, digits, "_" or "-"',
      );
    }
    if (!isValidDay(day)) {
      throw ArgumentError.value(day, 'day', 'must be a YYYY-MM-DD date');
    }
    final bytes = utf8.encode('${jsonEncode(line)}\n');
    _groups.putIfAbsent((type, day), () => []).add(bytes);
  }

  /// True when nothing was added.
  bool get isEmpty => _groups.isEmpty;

  /// Builds the parts. The builder can be built again; it gives the same
  /// parts for the same lines.
  List<AppleHealthPart> build() {
    final parts = <AppleHealthPart>[];
    var number = 0;
    for (final MapEntry(key: (type, day), value: lines) in _groups.entries) {
      var chunk = BytesBuilder(copy: false);
      var count = 0;
      void flush() {
        if (count == 0) return;
        final ndjson = chunk.takeBytes();
        parts.add(AppleHealthPart(
          type: type,
          day: day,
          batch: batch,
          partNumber: number++,
          body: gzipBytes(ndjson),
          lineCount: count,
          uncompressedBytes: ndjson.length,
        ));
        chunk = BytesBuilder(copy: false);
        count = 0;
      }

      for (final line in lines) {
        if (count > 0 && chunk.length + line.length > maxPartBytes) flush();
        chunk.add(line);
        count++;
      }
      flush();
    }
    return parts;
  }
}

/// Gzip compresses [data]. Works on every platform, web included.
Uint8List gzipBytes(List<int> data) => const GZipEncoder().encodeBytes(data);

/// True when [day] is a real `YYYY-MM-DD` calendar date.
bool isValidDay(String day) {
  final m = _dayPattern.firstMatch(day);
  if (m == null) return false;
  final y = int.parse(m.group(1)!);
  final mo = int.parse(m.group(2)!);
  final d = int.parse(m.group(3)!);
  final dt = DateTime.utc(y, mo, d);
  return dt.year == y && dt.month == mo && dt.day == d;
}

/// The calendar day of [instant], `YYYY-MM-DD`.
///
/// With [utcOffset], the day at that offset from UTC (for a sample whose
/// `HKTimeZone` is known); without it, the day in the device's local time
/// zone. Dart has no time zone database of its own: to use an IANA zone, get
/// its offset at [instant] from a package such as `timezone`.
String localDay(DateTime instant, {Duration? utcOffset}) {
  final t =
      utcOffset == null ? instant.toLocal() : instant.toUtc().add(utcOffset);
  String two(int v) => v.toString().padLeft(2, '0');
  return '${t.year.toString().padLeft(4, '0')}-${two(t.month)}-${two(t.day)}';
}

/// A fresh batch id: [prefix], `-`, then 20 random hex characters.
///
/// Use one per upload run, so new data gets new raw keys.
String newBatchId([String prefix = 'app']) {
  if (!RegExp(r'^[A-Za-z0-9_-]{1,107}$').hasMatch(prefix)) {
    throw ArgumentError.value(
      prefix,
      'prefix',
      'must be 1 to 107 letters, digits, "_" or "-"',
    );
  }
  final rnd = Random.secure();
  final b = StringBuffer(prefix)..write('-');
  for (var i = 0; i < 20; i++) {
    b.write(rnd.nextInt(16).toRadixString(16));
  }
  return b.toString();
}
