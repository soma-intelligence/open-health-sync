# Android example

A small Jetpack Compose app that uploads a heart rate reading to Open Health Sync with the
[Kotlin SDK](../../docs/sdks/kotlin.md). You type a heart rate, the app builds one
HealthKit-shaped line (`HKQuantityTypeIdentifierHeartRate`, stamped now, in the device's time
zone) with `AppleHealthPartBuilder` and sends it with `OhsClient.uploadParts`. It shows the
result per part (part number, type, day, stored or duplicate) or the error (status, title,
request id).

It also connects to Bluetooth health devices (heart rate straps, blood pressure cuffs, scales,
glucose meters, thermometers, pulse oximeters) and uploads what they send, as described in
[Bluetooth devices](#bluetooth-devices).

The app holds only a user token with the `upload` scope. It cannot read anything back: every
read route needs the tenant key, which belongs on your backend, never in an app. You check the
data with curl instead (below).

## Install without building

Each release from the next one on carries the app, built from the release's own commit:
[ohs-example-android.apk](https://github.com/soma-intelligence/open-health-sync/releases/latest/download/ohs-example-android.apk).
Open that link on the phone, allow the install from your browser, then paste the server URL and an
upload token in Settings (below). Its SHA-256 is in the release's signed `checksums.txt`. The APK is signed with a build key that changes with each release: remove the previous version
before installing a new one.

## Requirements

- Android Studio Ladybug or later, or JDK 17 and the Android SDK (platform 35) for the command line
- An emulator or device on Android 8.0 (API 26) or later
- A running Open Health Sync server: follow the [quickstart](../../README.md#quickstart)

The app uses the SDK from this repository: `settings.gradle.kts` includes `../../sdks/kotlin` as a
Gradle build, so nothing is downloaded for it.

## Get a token

With the server from the quickstart and its tenant key:

```sh
export OHS=http://localhost:8080
export KEY=ohs_sk_...    # the tenant key printed by `ohs admin tenant create`

USER_ID=$(curl -s -X POST $OHS/v1/users -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"android-demo"}' | jq -r .id)
TOKEN=$(curl -s -X POST $OHS/v1/users/$USER_ID/tokens -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload"]}' | jq -r .token)
echo $TOKEN
```

In a real app your backend mints this token and the app fetches it; nobody pastes it.

## Run

Open `examples/android` in Android Studio and run the `app` configuration, or from the command
line with an emulator running:

```sh
cd examples/android
./gradlew installDebug
```

On first launch the app opens Settings:

- **Server URL**: `http://10.0.2.2:8080` on the emulator (10.0.2.2 is your computer seen from the
  emulator). On a physical device, run `adb reverse tcp:8080 tcp:8080` and use
  `http://localhost:8080`. Any other server must use HTTPS: plain HTTP is allowed only for
  10.0.2.2 and localhost (`res/xml/network_security_config.xml`).
- **User token**: the `ohs_ut_...` token from above. To avoid typing it, focus the field and
  run `adb shell input text "$TOKEN"`.

Then enter a heart rate and tap **Upload**. "duplicate" means the server already held exactly
these bytes and stored nothing new.

## Check that the data landed

Wait until `pending_jobs` is 0, then read the sample back with the tenant key:

```sh
curl -s "$OHS/v1/users/$USER_ID/coverage" -H "Authorization: Bearer $KEY"
curl -s "$OHS/v1/users/$USER_ID/samples?type=heart_rate" -H "Authorization: Bearer $KEY"
```

The sample has `"source_name": "Android example"`.

## Bluetooth devices

Below the heart rate field, **Find devices** asks for the Bluetooth permission (on Android 11
and earlier, for location, which a Bluetooth scan needs there, with location turned on) and
connects to every device in range that advertises a standard health service. The live line shows
the last value, for example `Polar H10: 72 bpm`. **Upload recorded values** sends what was
recorded with `OhsClient.uploadBluetoothPart`; when an upload fails, the next tap sends the same
part again and the server stores it once.

[BluetoothSync.kt](app/src/main/java/org/openhealthsync/example/BluetoothSync.kt) is the part to
copy into an app. For each device it reads Device Information, sets the device clock through the
Current Time service when the device allows it, subscribes to every measurement, and asks a
glucose meter for the records after the last one recorded. It feeds the bytes to the SDK's
`BluetoothRecorder` without decoding them: the server decodes them
([Bluetooth connector](../../docs/connectors/bluetooth.md)). The device address stays on the
phone.

Cuffs, scales and glucose meters usually need pairing: Android shows its prompt on the first
subscription. A cuff or meter that keeps readings in memory sends them when it connects, so
press its Bluetooth button after starting **Find devices**. A Bluetooth device does not work on
the emulator: use a phone.

The readings land as samples with `"provider": "bluetooth"` (`view=all` lists them next to the
same readings from other sources):

```sh
curl -s "$OHS/v1/users/$USER_ID/samples?type=heart_rate&view=all" -H "Authorization: Bearer $KEY"
```

## End-to-end test

`app/src/androidTest` holds an instrumented test that goes through the app as a user would: it
fills Settings, enters a heart rate, taps Upload and waits for "stored".
[`test/e2e/apps.sh android`](../../test/e2e/apps.sh) runs it on the running emulator against a
server built from this checkout (Docker), then reads the sample back with the tenant key:

```sh
test/e2e/apps.sh android    # from the repository root, with an emulator running
```

To use a server you already run, set `OHS_URL` (for example `http://127.0.0.1:8080`) and
`OHS_KEY` (its tenant key). Without the script, `./gradlew connectedDebugAndroidTest` skips the
test: it needs the `ohsServerUrl`, `ohsToken` and `ohsBpm` instrumentation arguments.

## Health Connect

Reading Health Connect is planned for the Kotlin SDK. When it lands, this app will use it in
place of the manual entry. Until then the data source is the text field.

## Limits

- **Token storage.** The URL and token sit in app-private SharedPreferences, with backups off.
  That is enough for a demo. A real app should keep the token in EncryptedSharedPreferences or
  encrypt it with a key from the Android Keystore, and fetch short-lived tokens from its backend
  through `OhsClient(baseUrl, tokenProvider = { ... })`.
- One reading per upload from manual entry.
- No background sync, no retry queue beyond the SDK's own retries (429, 5xx and network errors,
  up to 5 attempts). Bluetooth values wait in memory until you tap upload: closing the app drops
  the ones not sent. Connections stop when the app closes; the desktop
  [bridge](../../docs/bridge.md) is the always-on option.
- The app never logs the token or a value.

## Licence

AGPL-3.0-only, like the rest of the repository ([LICENSE](../../LICENSE)).
