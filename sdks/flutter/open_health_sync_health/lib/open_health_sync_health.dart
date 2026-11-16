// SPDX-License-Identifier: AGPL-3.0-only

/// Reads HealthKit on iOS and Health Connect on Android and uploads what
/// changed to an Open Health Sync server.
///
/// The reading, the record mapping and the uploads are done by the Swift and
/// Kotlin SDKs of the repository; this plugin is the channel to them.
/// See docs/sdks/flutter.md in the repository.
library;

export 'src/health_sync.dart';
export 'src/report.dart';
export 'src/types.dart';
