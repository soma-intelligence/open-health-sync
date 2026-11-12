# open_health_sync_health (Flutter plugin)

Reads HealthKit on iOS and Health Connect on Android and uploads what changed since the last
run to an Open Health Sync server. The reading, the record mapping, the upload parts and the
retries are done by the two native SDKs of this repository: the Swift SDK's `HealthKitSync`
and the Kotlin SDK's `HealthConnectSync`. The plugin is the channel to them, with one Dart
API for both. Version 0.1.0, not published. AGPL-3.0-only, like the server
([LICENSE](../../../LICENSE)).

Full guide, with the data types and the error codes:
[docs/sdks/flutter.md](../../../docs/sdks/flutter.md#health-store-sync).

```dart
import 'package:open_health_sync_health/open_health_sync_health.dart';

final health = OpenHealthSyncHealth.instance;

if (await health.isAvailable()) {
  // A user token with the upload scope, minted by your server. Never the tenant key.
  await health.setCredentials(baseUrl: 'https://ohs.example.org', userToken: token);

  const types = {HealthDataType.steps, HealthDataType.heartRate, HealthDataType.sleep};
  await health.requestAuthorization(types, background: true);

  final report = await health.sync(types: types);
  await health.enableBackgroundSync(types: types);
  health.backgroundSyncResults.listen((result) => print(result.isSuccess));
}
```

## Requirements

Flutter 3.41 or later, iOS 15, Android API 26 (`minSdk = 26`, `compileSdk = 36`).

## Set up iOS

1. The iOS side is a Swift package that depends on the Swift SDK of this repository. Swift
   Package Manager is on by default from Flutter 3.44; there is no podspec. Until the
   repository has a version tag with the `Package.swift` at its root (`v0.3.0`), build with
   the absolute path of a checkout in `OHS_SWIFT_SDK_PATH`:

   ```sh
   OHS_SWIFT_SDK_PATH=/path/to/open-health-sync flutter run
   ```

2. In Xcode, add the HealthKit capability to the Runner target (the
   `com.apple.developer.healthkit` entitlement) and, for background sync, tick *Background
   Delivery* under it (`com.apple.developer.healthkit.background-delivery`).
3. Add `NSHealthShareUsageDescription` to `ios/Runner/Info.plist`:

   ```xml
   <key>NSHealthShareUsageDescription</key>
   <string>Your steps, heart rate and sleep are sent to your account.</string>
   ```

4. Set the deployment target to iOS 15 or later.

## Set up Android

1. The Android side depends on the Health Connect module of the Kotlin SDK. Until it is
   published, include the Kotlin SDK of a checkout as a Gradle build, and let it find the
   Android SDK (`ANDROID_HOME`, or `sdk.dir` in `sdks/kotlin/local.properties`):

   ```kotlin
   // android/settings.gradle.kts
   includeBuild("/path/to/open-health-sync/sdks/kotlin")
   ```

   The included build brings its own Android Gradle plugin (8.13.2, with Kotlin 2.0.21),
   which must be the one of your app and does not run on Gradle 9.6 or later. Built here with
   Gradle 8.14.3 and JDK 17.

2. In `android/app/build.gradle.kts`: `minSdk = 26`, `compileSdk = 36`.
3. In `android/app/src/main/AndroidManifest.xml`, declare the read permission of each type you
   sync and the screen that explains your use of the data, which Health Connect requires:

   ```xml
   <uses-permission android:name="android.permission.health.READ_STEPS" />
   <uses-permission android:name="android.permission.health.READ_HEART_RATE" />
   <uses-permission android:name="android.permission.health.READ_SLEEP" />
   <!-- Only for background sync, and for data older than 30 days before the first grant: -->
   <uses-permission android:name="android.permission.health.READ_HEALTH_DATA_IN_BACKGROUND" />
   <uses-permission android:name="android.permission.health.READ_HEALTH_DATA_HISTORY" />

   <application>
       <activity android:name=".MainActivity" android:exported="true">
           <!-- ... the launcher intent filter Flutter wrote ... -->
           <!-- Android 13 and lower -->
           <intent-filter>
               <action android:name="androidx.health.ACTION_SHOW_PERMISSIONS_RATIONALE" />
           </intent-filter>
       </activity>
       <!-- Android 14 and higher -->
       <activity-alias
           android:name="ViewPermissionUsageActivity"
           android:exported="true"
           android:permission="android.permission.START_VIEW_PERMISSION_USAGE"
           android:targetActivity=".MainActivity">
           <intent-filter>
               <action android:name="android.intent.action.VIEW_PERMISSION_USAGE" />
               <category android:name="android.intent.category.HEALTH_PERMISSIONS" />
           </intent-filter>
       </activity-alias>
   </application>
   ```

   The permission of each record type is in the
   [Kotlin guide](../../../docs/sdks/kotlin.md#record-types). The plugin declares none itself.

## What differs between the platforms

| | iOS (HealthKit) | Android (Health Connect) |
|---|---|---|
| `requestAuthorization` | `granted` is null: HealthKit does not say | `granted` lists the types the user granted |
| A failed upload | `sync` throws; the next sync sends the same samples | Parts stay in a queue on disk; the report has `partsPending` and `uploadError` |
| `flush`, `pendingParts` | Empty report, 0 | The queue |
| `enableBackgroundSync` | HealthKit background delivery, turned on again by the plugin at every launch | A WorkManager job, at most every 15 minutes |

The credentials are kept natively (keychain on iOS, a value encrypted with an Android Keystore
key on Android) because a background sync runs when no Dart code does. They are never logged.

## Tests

```sh
flutter pub get
dart format --output=none --set-exit-if-changed .
flutter analyze --fatal-infos
flutter test
```

The tests mock the method channel and the event channel. Not verified on a device yet: see the
end of the [guide's section](../../../docs/sdks/flutter.md#how-it-is-built).
