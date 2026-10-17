# React Native example

A small Expo app that uploads one heart rate reading to an Open Health Sync server with the
[React Native SDK](../../docs/sdks/react-native.md). It shows the credential model of a real app:
the app holds only an upload-scope user token, keeps it in Expo SecureStore, and never reads data
back (every read route needs the tenant key, which belongs on your backend).

What it does:

- Settings: server URL and user token. The token is saved in SecureStore only, and the client
  reads it from there through `tokenProvider` before every request.
- Upload: builds one `HKQuantityTypeIdentifierHeartRate` line with `AppleHealthPartBuilder`
  (day from `localDay` in the device time zone), uploads it with `uploadParts`, and shows each
  part's number, type, day and whether it was stored or a duplicate.
- "Send again" re-sends the same parts, so you can see the server answer `duplicate`.
- Errors show the HTTP status, title and request id of `OhsApiError`.

The app is a single file, [`App.tsx`](App.tsx).

## Requirements

- Node 18 or later (CI uses Node 22).
- A running Open Health Sync server, for example from the [repository quickstart](../../README.md#quickstart).
- Expo Go on a phone, or an Android emulator or iOS simulator.

Expo SDK 52, React Native 0.76. The SDK is installed from this repository
(`"open-health-sync-react-native": "file:../../sdks/react-native"`); `metro.config.js` adds that
folder to Metro's watch folders, and `tsconfig.json` points TypeScript at the SDK source.

## Run

```sh
cd examples/react-native
npm install
npx expo start
```

Then press `a` (Android emulator), `i` (iOS simulator), or scan the QR code with Expo Go.

Server URL to enter in the app:

| Where the app runs | URL |
|---|---|
| iOS simulator | `http://localhost:8080` (the default) |
| Android emulator | `http://10.0.2.2:8080` |
| A phone on your network | `http://<your computer's LAN address>:8080` |

Plain `http` is for local development only; use `https` for anything else.

## Get a user token

With the server from the quickstart running and the tenant key it printed:

```sh
export OHS=http://localhost:8080
export KEY=ohs_sk_...    # tenant key

USER_ID=$(curl -s -X POST $OHS/v1/users -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"demo"}' | jq -r .id)
TOKEN=$(curl -s -X POST $OHS/v1/users/$USER_ID/tokens -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload"]}' | jq -r .token)
echo $TOKEN
```

Paste the `ohs_ut_...` token into the app and tap Save. In a real app your backend mints this
token and the app fetches it from your backend; it never sees the tenant key.

## Check the data landed

Processing is asynchronous. Wait until `pending_jobs` is 0, then read the sample back with the
tenant key:

```sh
curl -s "$OHS/v1/users/$USER_ID/coverage" -H "Authorization: Bearer $KEY"
curl -s "$OHS/v1/users/$USER_ID/samples?type=heart_rate" -H "Authorization: Bearer $KEY"
```

## Real HealthKit or Health Connect data

This example types a value by hand so it runs in Expo Go. To upload real readings, read them
with a community module and turn each one into a line as `App.tsx` does:
`react-native-health` for HealthKit on iOS, `react-native-health-connect` for Health Connect on
Android. Both are native modules, so they need a
[development build](https://docs.expo.dev/develop/development-builds/introduction/)
(`npx expo run:ios` or `npx expo run:android`), not Expo Go. The SDK guide has a full sketch:
[Reading HealthKit or Health Connect in React Native](../../docs/sdks/react-native.md#reading-healthkit-or-health-connect-in-react-native).
The upload route takes HealthKit-shaped lines only, so a Health Connect reading is written with
the matching HealthKit identifier and unit.

## Checks

What CI runs:

```sh
cd examples/react-native
npm install
npx tsc --noEmit
npx expo export --platform android --output-dir dist-check
```

## End-to-end test

[`e2e/upload.yaml`](e2e/upload.yaml) is a [Maestro](https://maestro.mobile.dev) flow: on a fresh
install it saves the URL and a token, uploads one heart rate, then sends the same part again and
expects "Duplicate (already stored)". [`test/e2e/apps.sh react-native`](../../test/e2e/apps.sh)
makes a debug build (`npx expo prebuild`, then Gradle), starts Metro, runs the flow on the running
Android emulator against a server built from this checkout (Docker), and reads the sample back
with the tenant key:

```sh
test/e2e/apps.sh react-native    # from the repository root, with an emulator running
```

It needs Node, the Android SDK, JDK 17 and the `maestro` CLI. To use a server you already run, set
`OHS_URL` and `OHS_KEY` (its tenant key). The app ids are `org.openhealthsync.example.reactnative`,
so this app and the native examples can sit on the same device.

## Limits

- One reading per upload, typed by hand. No background sync, no history.
- The token is pasted by hand and has no refresh; when it expires, uploads fail with 401 and
  you paste a new one.
- The app cannot read data back: that needs the tenant key, which must stay on a server.

## Licence

AGPL-3.0-only, like the rest of the repository ([LICENSE](../../LICENSE)).
