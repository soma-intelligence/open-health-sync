// SPDX-License-Identifier: MIT

/// Dart client for the Open Health Sync REST API.
///
/// Works in Flutter apps on iOS, Android, desktop and web, and in plain Dart.
/// See docs/sdks/flutter.md in the repository.
library;

export 'src/client.dart' show OhsClient, defaultUserAgent, maxUploadAttempts;
export 'src/errors.dart';
export 'src/json.dart' show formatTimestamp, parseTimestamp;
export 'src/models.dart';
export 'src/pagination.dart';
export 'src/part_builder.dart';
