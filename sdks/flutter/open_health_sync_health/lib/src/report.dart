// SPDX-License-Identifier: AGPL-3.0-only

import 'package:flutter/services.dart';

import 'types.dart';

/// An error of the native side: a call that failed, or the reason an upload
/// stopped.
///
/// [code] is one of `not_configured` (no credentials stored), `unavailable`
/// (no health store), `unsupported_platform`, `missing_usage_description`
/// (iOS, `NSHealthShareUsageDescription`), `no_activity` and
/// `authorization_in_progress` (Android), `permission_denied`, `store_locked`
/// (iOS, the device is locked), `api_error` (the server answered, see
/// [status]), `network_error`, `invalid_argument` and `health_store_error`.
///
/// The message never holds a token or a health value.
class HealthSyncException implements Exception {
  const HealthSyncException(
    this.code, {
    this.message,
    this.status,
    this.requestId,
  });

  /// Reads the map the native side sends for an error.
  factory HealthSyncException.fromMap(Map<Object?, Object?> map) {
    return HealthSyncException(
      map['code'] as String? ?? 'error',
      message: map['message'] as String?,
      status: (map['status'] as num?)?.toInt(),
      requestId: map['requestId'] as String?,
    );
  }

  /// Reads the error of a failed channel call.
  factory HealthSyncException.fromPlatform(PlatformException e) {
    final details = e.details;
    if (details is Map<Object?, Object?>) {
      return HealthSyncException.fromMap({
        ...details,
        'code': e.code,
        'message': details['message'] ?? e.message,
      });
    }
    return HealthSyncException(e.code, message: e.message);
  }

  final String code;
  final String? message;

  /// The HTTP status, for `api_error`.
  final int? status;

  /// The server's request id, for `api_error`. Quote it in a bug report.
  final String? requestId;

  @override
  String toString() {
    final b = StringBuffer('HealthSyncException($code');
    if (status != null) b.write(' $status');
    b.write(')');
    if (message != null) b.write(': $message');
    if (requestId != null) b.write(' [request_id $requestId]');
    return b.toString();
  }
}

/// What the user was asked, and what the store said about it.
class AuthorizationResult {
  const AuthorizationResult({
    this.requested = const {},
    this.granted,
    this.unsupported = const {},
    this.notDeclared = const {},
    this.backgroundGranted,
    this.historyGranted,
  });

  /// The types the platform has, which the user was asked for.
  final Set<HealthDataType> requested;

  /// Android: the types of [requested] the user granted. Null on iOS, where
  /// HealthKit never tells an app whether read access was granted: a
  /// declined type just returns no samples.
  final Set<HealthDataType>? granted;

  /// The types left out because the platform does not have them.
  final Set<HealthDataType> unsupported;

  /// Android: the types whose read permission is missing from the app's
  /// manifest. Health Connect cannot be asked for them.
  final Set<HealthDataType> notDeclared;

  /// Android, when asked: whether reading in the background was granted.
  final bool? backgroundGranted;

  /// Android, when asked: whether data older than 30 days was granted.
  final bool? historyGranted;
}

/// What one sync or flush did.
class SyncReport {
  const SyncReport({
    this.batchIds = const [],
    this.partsUploaded = 0,
    this.duplicateParts = 0,
    this.partsPending = 0,
    this.partsDropped = 0,
    this.linesByType = const {},
    this.deletionsByType = const {},
    this.deletionsPendingByType = const {},
    this.skippedRecords = 0,
    this.typesWithoutPermission = const {},
    this.expiredTokenTypes = const {},
    this.unsupportedTypes = const {},
    this.uploadError,
  });

  /// Reads the map the native side sends for a report.
  factory SyncReport.fromMap(
    Map<Object?, Object?> map,
    HealthPlatform platform, {
    Set<HealthDataType> unsupportedTypes = const {},
  }) {
    return SyncReport(
      batchIds: _strings(map['batchIds']),
      partsUploaded: _int(map['partsUploaded']),
      duplicateParts: _int(map['duplicateParts']),
      partsPending: _int(map['partsPending']),
      partsDropped: _int(map['partsDropped']),
      linesByType: _counts(map['linesByType']),
      deletionsByType: _counts(map['deletionsByType']),
      deletionsPendingByType: _counts(map['deletionsPendingByType']),
      skippedRecords: _int(map['skippedRecords']),
      typesWithoutPermission: _types(map['typesWithoutPermission'], platform),
      expiredTokenTypes: _types(map['expiredTokenTypes'], platform),
      unsupportedTypes: unsupportedTypes,
      uploadError: _error(map['uploadError']),
    );
  }

  /// The batch ids used.
  final List<String> batchIds;

  /// Parts the server stored or already had.
  final int partsUploaded;

  /// Of [partsUploaded], the parts the server already had.
  final int duplicateParts;

  /// Android: parts still in the queue on disk, waiting for the network or
  /// the server. Always 0 on iOS, which keeps no queue (see [uploadError]).
  final int partsPending;

  /// Android: parts the server refused for good; they left the queue.
  final int partsDropped;

  /// Lines read, by native type: the HealthKit identifier on iOS (uploaded
  /// lines), the Health Connect record type on Android (queued lines).
  final Map<String, int> linesByType;

  /// Deletions sent (iOS) or queued (Android), by native type.
  final Map<String, int> deletionsByType;

  /// iOS: deletions kept for later because the server has not said yet that
  /// it applies them, by HealthKit identifier.
  final Map<String, int> deletionsPendingByType;

  /// Records left out: a type or a value the server does not store.
  final int skippedRecords;

  /// Android: types not read because their read permission is not granted.
  final Set<HealthDataType> typesWithoutPermission;

  /// Android: types whose changes token had expired and that were read again
  /// over a time window.
  final Set<HealthDataType> expiredTokenTypes;

  /// The types asked for that this platform does not have.
  final Set<HealthDataType> unsupportedTypes;

  /// Android: why the upload stopped with parts still pending. On iOS a
  /// failed upload throws instead, and the next sync reads the same samples
  /// again.
  final HealthSyncException? uploadError;

  /// Lines read, deletions excluded.
  int get lines => linesByType.values.fold(0, (a, b) => a + b);

  /// Deletions sent or queued.
  int get deletions => deletionsByType.values.fold(0, (a, b) => a + b);
}

/// The outcome of one sync that ran in the background.
class BackgroundSyncResult {
  const BackgroundSyncResult({required this.at, this.report, this.error});

  /// Reads the map the native side sends on the event channel.
  factory BackgroundSyncResult.fromMap(
    Map<Object?, Object?> map,
    HealthPlatform platform,
  ) {
    SyncReport? report;
    final raw = map['report'];
    if (raw is Map<Object?, Object?>) {
      report = SyncReport.fromMap(raw, platform);
    }
    return BackgroundSyncResult(
      at: DateTime.fromMillisecondsSinceEpoch(_int(map['at']), isUtc: true),
      report: report,
      error: _error(map['error']),
    );
  }

  /// When the run ended, in UTC.
  final DateTime at;

  /// The report of a run that read the store; null when it failed.
  final SyncReport? report;

  /// Why the run failed; null when it gave a [report].
  final HealthSyncException? error;

  bool get isSuccess => error == null;
}

int _int(Object? value) => value is num ? value.toInt() : 0;

HealthSyncException? _error(Object? value) {
  if (value is! Map<Object?, Object?>) return null;
  return HealthSyncException.fromMap(value);
}

List<String> _strings(Object? value) {
  if (value is! List<Object?>) return const [];
  return value.whereType<String>().toList();
}

Map<String, int> _counts(Object? value) {
  final counts = <String, int>{};
  if (value is! Map<Object?, Object?>) return counts;
  value.forEach((key, count) {
    if (key is String) counts[key] = _int(count);
  });
  return counts;
}

Set<HealthDataType> _types(Object? value, HealthPlatform platform) {
  final types = <HealthDataType>{};
  for (final name in _strings(value)) {
    types.addAll(HealthDataType.fromNative(platform, name));
  }
  return types;
}
