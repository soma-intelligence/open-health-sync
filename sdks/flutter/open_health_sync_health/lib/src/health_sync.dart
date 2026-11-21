// SPDX-License-Identifier: AGPL-3.0-only

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import 'channel.dart';
import 'report.dart';
import 'types.dart';

/// Reads the health store of the device and uploads what changed.
///
/// On iOS the work is done by the Swift SDK's `HealthKitSync`, on Android by
/// the Kotlin SDK's `HealthConnectSync`. Use [instance]:
///
/// ```dart
/// final health = OpenHealthSyncHealth.instance;
/// if (await health.isAvailable()) {
///   await health.setCredentials(baseUrl: url, userToken: token);
///   await health.requestAuthorization({HealthDataType.steps});
///   final report = await health.sync(types: {HealthDataType.steps});
/// }
/// ```
class OpenHealthSyncHealth {
  /// A plugin on other channels than the default ones, for tests.
  @visibleForTesting
  OpenHealthSyncHealth({MethodChannel? methods, EventChannel? events})
      : _methods = methods ?? const MethodChannel(methodChannelName),
        _events = events ?? const EventChannel(eventChannelName);

  /// The plugin of this app.
  static final OpenHealthSyncHealth instance = OpenHealthSyncHealth();

  final MethodChannel _methods;
  final EventChannel _events;
  Stream<BackgroundSyncResult>? _results;

  /// The health store of the platform the app runs on; null on the web and
  /// on desktops, which have none.
  HealthPlatform? get platform {
    if (kIsWeb) return null;
    switch (defaultTargetPlatform) {
      case TargetPlatform.iOS:
        return HealthPlatform.healthKit;
      case TargetPlatform.android:
        return HealthPlatform.healthConnect;
      default:
        return null;
    }
  }

  /// Whether the health store can be used, and if not, whether an update of
  /// the Health Connect app would fix it.
  Future<HealthStoreAvailability> availability() async {
    if (platform == null) return HealthStoreAvailability.unavailable;
    final name = await _invoke<String>('availability');
    for (final value in HealthStoreAvailability.values) {
      if (value.name == name) return value;
    }
    return HealthStoreAvailability.unavailable;
  }

  /// Whether the health store can be used. False on the web and on desktops.
  Future<bool> isAvailable() async {
    return await availability() == HealthStoreAvailability.available;
  }

  /// Stores where to upload and with which credential.
  ///
  /// [userToken] is a user token with the `upload` scope that your server
  /// minted, never the tenant key. It is kept by the native side (the
  /// keychain on iOS, a file encrypted with an Android Keystore key on
  /// Android) because a background sync runs when no Dart code does. Call
  /// this again whenever your server gives a new token.
  Future<void> setCredentials({
    required String baseUrl,
    required String userToken,
  }) async {
    if (userToken.isEmpty) {
      throw ArgumentError.value('', 'userToken', 'must not be empty');
    }
    final uri = Uri.tryParse(baseUrl);
    if (uri == null || !(uri.isScheme('https') || uri.isScheme('http'))) {
      throw ArgumentError.value(baseUrl, 'baseUrl', 'must be an http(s) URL');
    }
    await _invoke<void>('setCredentials', {
      'baseUrl': baseUrl,
      'userToken': userToken,
    });
  }

  /// Forgets the credentials, for example when the user signs out. A
  /// background sync then does nothing until [setCredentials] is called.
  Future<void> clearCredentials() => _invoke<void>('clearCredentials');

  /// Shows the system screen that asks the user for read access to [types].
  ///
  /// On Android, [background] also asks for reading while the app is not
  /// open (needed by [enableBackgroundSync]) and [history] for data older
  /// than 30 days before the first grant; both are ignored on iOS.
  Future<AuthorizationResult> requestAuthorization(
    Set<HealthDataType> types, {
    bool background = false,
    bool history = false,
  }) async {
    final target = _requirePlatform();
    final requested = types.where((t) => t.isSupportedOn(target)).toSet();
    final unsupported = types.difference(requested);
    if (requested.isEmpty) {
      return AuthorizationResult(unsupported: unsupported);
    }
    final answer = await _invoke<Map<Object?, Object?>>(
      'requestAuthorization',
      {
        'types': _identifiers(requested, target),
        'background': background,
        'history': history,
      },
    );
    final map = answer ?? const <Object?, Object?>{};
    final granted = _nativeSet(map['granted']);
    final notDeclared = _nativeSet(map['notDeclared']) ?? const <String>{};
    return AuthorizationResult(
      requested: requested,
      granted: granted == null ? null : _holding(requested, target, granted),
      unsupported: unsupported,
      notDeclared: _missing(requested, target, notDeclared),
      backgroundGranted: map['backgroundGranted'] as bool?,
      historyGranted: map['historyGranted'] as bool?,
    );
  }

  /// Reads what changed in the store since the last sync and uploads it.
  ///
  /// [types] defaults to every type the native SDK reads. A type the platform
  /// does not have is left out and listed in [SyncReport.unsupportedTypes].
  ///
  /// Throws [HealthSyncException] when the store cannot be read or, on iOS,
  /// when an upload fails (the next sync then sends the same samples). On
  /// Android a failed upload does not throw: the parts stay in a queue on
  /// disk and the report carries [SyncReport.partsPending] and
  /// [SyncReport.uploadError].
  Future<SyncReport> sync({Set<HealthDataType>? types}) async {
    final target = _requirePlatform();
    final unsupported = _unsupported(types, target);
    final ids = types == null ? null : _identifiers(types, target);
    if (ids != null && ids.isEmpty) {
      return SyncReport(unsupportedTypes: unsupported);
    }
    final map = await _invoke<Map<Object?, Object?>>('sync', {'types': ids});
    return SyncReport.fromMap(
      map ?? const {},
      target,
      unsupportedTypes: unsupported,
    );
  }

  /// Uploads the parts waiting in the Android queue without reading the
  /// store. [force] skips the wait of a part that failed before: use it when
  /// the user asks for a sync. On iOS there is no queue and the report is
  /// empty.
  Future<SyncReport> flush({bool force = false}) async {
    final target = _requirePlatform();
    final map = await _invoke<Map<Object?, Object?>>('flush', {'force': force});
    return SyncReport.fromMap(map ?? const {}, target);
  }

  /// How many parts wait in the Android queue. Always 0 on iOS.
  Future<int> pendingParts() async {
    if (platform == null) return 0;
    return await _invoke<int>('pendingParts') ?? 0;
  }

  /// Keeps syncing [types] when the app is not open.
  ///
  /// On iOS this turns on HealthKit background delivery, and the plugin turns
  /// it on again at every launch; [interval] picks the closest HealthKit
  /// frequency (immediate under an hour, then hourly, daily, weekly). On
  /// Android it schedules a WorkManager job every [interval] (at least 15
  /// minutes) that runs when a network is up, on an unmetered one only with
  /// [requireUnmeteredNetwork].
  ///
  /// The credentials must be stored first ([setCredentials]). Results arrive
  /// on [backgroundSyncResults].
  Future<void> enableBackgroundSync({
    Set<HealthDataType>? types,
    Duration interval = const Duration(hours: 1),
    bool requireUnmeteredNetwork = false,
  }) async {
    final target = _requirePlatform();
    final ids = types == null ? null : _identifiers(types, target);
    if (ids != null && ids.isEmpty) return;
    await _invoke<void>('enableBackgroundSync', {
      'types': ids,
      'intervalSeconds': interval.inSeconds,
      'requireUnmeteredNetwork': requireUnmeteredNetwork,
    });
  }

  /// Stops what [enableBackgroundSync] started. Parts already queued on
  /// Android are kept.
  Future<void> disableBackgroundSync() async {
    if (platform == null) return;
    await _invoke<void>('disableBackgroundSync');
  }

  /// One event per sync that ran in the background, while the Dart side is
  /// alive. The native side keeps the last runs that ended while nobody
  /// listened (at most 20) and sends them to the first listener.
  Stream<BackgroundSyncResult> get backgroundSyncResults {
    if (platform == null) return const Stream.empty();
    return _results ??= _events.receiveBroadcastStream().map(_decodeResult);
  }

  BackgroundSyncResult _decodeResult(Object? event) {
    final target = _requirePlatform();
    if (event is! Map<Object?, Object?>) {
      return BackgroundSyncResult.fromMap(const {}, target);
    }
    return BackgroundSyncResult.fromMap(event, target);
  }

  HealthPlatform _requirePlatform() {
    final target = platform;
    if (target == null) {
      throw const HealthSyncException(
        'unsupported_platform',
        message: 'this platform has no health store',
      );
    }
    return target;
  }

  Future<T?> _invoke<T>(String method, [Map<String, Object?>? args]) async {
    _requirePlatform();
    try {
      return await _methods.invokeMethod<T>(method, args);
    } on PlatformException catch (e) {
      throw HealthSyncException.fromPlatform(e);
    }
  }
}

/// The native types of [types] on [platform], without duplicates.
List<String> _identifiers(Set<HealthDataType> types, HealthPlatform platform) {
  final ids = <String>{};
  for (final type in types) {
    ids.addAll(type.identifiersOn(platform));
  }
  return ids.toList();
}

Set<HealthDataType> _unsupported(
  Set<HealthDataType>? types,
  HealthPlatform platform,
) {
  if (types == null) return const {};
  return types.where((t) => !t.isSupportedOn(platform)).toSet();
}

Set<String>? _nativeSet(Object? value) {
  if (value is! List<Object?>) return null;
  return value.whereType<String>().toSet();
}

/// The types of [types] whose native types are all in [granted].
Set<HealthDataType> _holding(
  Set<HealthDataType> types,
  HealthPlatform platform,
  Set<String> granted,
) {
  return types
      .where((t) => t.identifiersOn(platform).every(granted.contains))
      .toSet();
}

/// The types of [types] with a native type in [missing].
Set<HealthDataType> _missing(
  Set<HealthDataType> types,
  HealthPlatform platform,
  Set<String> missing,
) {
  return types
      .where((t) => t.identifiersOn(platform).any(missing.contains))
      .toSet();
}
