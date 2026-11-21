# open_health_sync (Dart and Flutter)

Two packages for Flutter and Dart. Version 0.1.0, not published (`publish_to: none`): add them
to an app by path or git. AGPL-3.0-only, like the server ([LICENSE](../../LICENSE)).

| Package | Directory | What it is |
|---|---|---|
| `open_health_sync` | this directory | Dart client for the Open Health Sync REST API, for Flutter apps (iOS, Android, desktop, web) and Dart backends. A plain Dart package, no Flutter dependency |
| `open_health_sync_health` | [`open_health_sync_health`](open_health_sync_health) | Flutter plugin that reads HealthKit on iOS and Health Connect on Android and uploads what changed, by running the Swift and Kotlin SDKs of this repository |

Guide: [docs/sdks/flutter.md](../../docs/sdks/flutter.md). Overview of all SDKs:
[docs/sdks/README.md](../../docs/sdks/README.md).

The client, with the Dart SDK alone:

```sh
dart pub get
dart test
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... dart run example/quickstart.dart
```

The plugin, with Flutter 3.41 or later:

```sh
cd open_health_sync_health
flutter pub get
flutter test
```

The plugin is a package of its own so that the client still resolves, analyses and tests
without Flutter. Its directory is left out of the client's analysis and of its published files
(`analysis_options.yaml`, `.pubignore`).
