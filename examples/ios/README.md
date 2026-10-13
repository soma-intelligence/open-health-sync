# iOS example

A small SwiftUI app that reads HealthKit and uploads it to Open Health Sync with the
[Swift SDK](../../docs/sdks/swift.md). It uses `HealthKitSync` from `OpenHealthSyncHealthKit`:

- **Allow Health access** calls `requestAuthorization` for heart rate, steps, active energy,
  sleep analysis and workouts.
- **Sync now** calls `sync` for the types you ticked and shows the `SyncReport`: lines and parts
  uploaded, parts the server already held, samples skipped, lines per type and the batch ids.
  An error shows its HTTP status, title and request id.
- **Background delivery** calls `enableBackgroundDelivery`, so HealthKit wakes the app to upload
  new samples. The app turns it back on at every launch, as HealthKit requires.

The app holds only a user token with the `upload` scope, kept in the Keychain. It cannot read
anything back: every read route needs the tenant key, which belongs on your backend, never in an
app. You check the data with curl instead (below).

## Requirements

- macOS with Xcode 15 or later (iOS 16 simulator or later)
- [XcodeGen](https://github.com/yonaskolb/XcodeGen): `brew install xcodegen`
- A running Open Health Sync server: follow the [quickstart](../../README.md#quickstart)

The app uses the SDK from this repository by path (`../../sdks/swift`), so nothing is downloaded
for it.

## Get a token

With the server from the quickstart and its tenant key:

```sh
export OHS=http://localhost:8080
export KEY=ohs_sk_...    # the tenant key printed by `ohs admin tenant create`

USER_ID=$(curl -s -X POST $OHS/v1/users -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"ios-demo"}' | jq -r .id)
TOKEN=$(curl -s -X POST $OHS/v1/users/$USER_ID/tokens -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload"]}' | jq -r .token)
echo $TOKEN
```

The token lasts one hour. For a longer session add `"ttl_seconds":86400` (24 hours, the most).
In a real app your backend mints this token and the app fetches it; nobody pastes it.

## Run

```sh
cd examples/ios
xcodegen generate
open OHSExample.xcodeproj
```

Pick the `OHSExampleApp` scheme and an iPhone simulator, then run. The project file is generated
from `project.yml` and not committed.

1. Tap the gear and fill in Settings:
   - **Server URL**: `http://localhost:8080` reaches the server on your Mac from the simulator.
     On a physical iPhone use your Mac's address on the local network (`http://192.168.x.y:8080`)
     or an HTTPS URL; plain HTTP is allowed only for local addresses (`NSAllowsLocalNetworking`).
   - **User token**: the `ohs_ut_...` token from above. Copy it on the Mac and paste it in the
     field: the simulator shares the Mac's clipboard.
2. Tap **Allow Health access** and allow the types in the sheet.
3. Tap **Sync now**.

**Sample data on the simulator.** The simulator's Health app starts empty. Open it, pick a type
(for example *Heart > Heart Rate*), tap *Add Data* and enter a few readings by hand, then sync.
Data entered this way comes from the source `Health`.

To run on a device, set your team under *Signing & Capabilities* (the project leaves
`DEVELOPMENT_TEAM` empty); the HealthKit entitlements are already in `Support/OHSExample.entitlements`.

## End-to-end test

`OHSExampleUITests` goes through the app as a user would: Settings, the Health access sheet, a
first sync and a second one that must find nothing new. The simulator's Health store starts
empty, so in debug builds the app writes the heart rates the test passes in
`OHS_UI_TEST_SEED_BPM` when you allow access ([`UITestSeed.swift`](OHSExample/UITestSeed.swift));
release builds never write to Health. [`test/e2e/apps.sh ios`](../../test/e2e/apps.sh) runs it on
the first available iPhone simulator and reads the samples back with the tenant key:

```sh
test/e2e/apps.sh ios    # from the repository root
```

Without `OHS_URL` and `OHS_KEY` it builds and starts the server with Docker; with them it uses
your running server (for example `OHS_URL=http://127.0.0.1:8080`). Pick another simulator with
`OHS_IOS_DESTINATION` (an `xcodebuild -destination` value). In Xcode, the test is skipped unless
the scheme's test action sets `OHS_URL`, `OHS_TOKEN` and `OHS_BPM`.

## Check that the data landed

Wait until `pending_jobs` is 0, then read the data back with the tenant key:

```sh
curl -s "$OHS/v1/users/$USER_ID/coverage" -H "Authorization: Bearer $KEY"
curl -s "$OHS/v1/users/$USER_ID/samples?type=heart_rate" -H "Authorization: Bearer $KEY"
curl -s "$OHS/v1/users/$USER_ID/samples?type=step_count" -H "Authorization: Bearer $KEY"
curl -s "$OHS/v1/users/$USER_ID/sleep" -H "Authorization: Bearer $KEY"
curl -s "$OHS/v1/users/$USER_ID/workouts" -H "Authorization: Bearer $KEY"
```

## Limits

- **Anchors.** Each type keeps a HealthKit anchor in `UserDefaults`, so a sync sends only what is
  new. After pointing the app at another server, tap *Reset sync anchors* in Settings to send the
  whole history again (the server stores each record once).
- **Background delivery** needs a real device to be useful: iOS decides when to wake the app, and
  steps arrive at most hourly. The token must still be valid then, so a pasted one-hour token
  will fail in the background; a real app refreshes it with its backend.
- **Report.** `SyncReport` counts parts; it does not list them one by one. Deleted samples are not
  sent (the upload route has no deletion yet).
- **Logs.** The app logs nothing: no token, no health value.
