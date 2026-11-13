# Client SDKs

Four client libraries call the [REST API](../api.md) from an app: Kotlin, Swift, React Native and
Flutter. They are thin, typed and written by hand against
[api/openapi.yaml](../../api/openapi.yaml). They cover the part of the API an app and its backend
need most ([the list](#what-each-sdk-covers)), not every route, and they share one shape, so what
you learn in one carries over to the others.

| SDK | Directory | Runs on | Guide |
|---|---|---|---|
| Kotlin | [`sdks/kotlin`](../../sdks/kotlin) | Android (API 26+), any JVM 11+ | [Kotlin](kotlin.md) |
| Swift | [`sdks/swift`](../../sdks/swift) | iOS 15+, macOS 12+, Linux (client only) | [Swift](swift.md) |
| React Native | [`sdks/react-native`](../../sdks/react-native) | Client: React Native 0.72+, Expo, Node 18+, browsers. Health module: React Native 0.80+ with the New Architecture, iOS 15.1+, Android API 26+ | [React Native](react-native.md) |
| Flutter | [`sdks/flutter`](../../sdks/flutter) | Client: Flutter 3.22+ and Dart 3.4+ (iOS, Android, desktop, web). Health plugin: Flutter 3.41+, iOS 15+, Android API 26+ | [Flutter](flutter.md) |

**Status: 0.1.0, not on any package registry yet.** Install them from this repository (each guide
says how). Package names and coordinates are set when the first version is published.

| What | Version | Published |
|---|---|---|
| These four SDKs | 0.1.0 | No: not on Maven Central, Swift Package Index, npm or pub.dev. The Swift package has no version of its own, since Swift packages take theirs from a git tag |
| [Server SDKs](../server-sdks.md) (TypeScript, Python), generated from the OpenAPI file | 1.0.0 | No: not on npm or PyPI |
| The server | 0.3.0 | Yes: binaries on the [releases page](https://github.com/soma-intelligence/open-health-sync/releases) and a container image |

**Licence: AGPL-3.0-only**, like the server ([LICENSE](../../LICENSE)).

Example apps for iOS, Android and React Native, built on these SDKs, are in
[examples/](../../examples/README.md).

## What every SDK does

- **The core routes.** Providers, users, tokens, connections, freshness, the read routes
  (samples, category samples, sleep and its phases, workouts and their routes, nutrition, daily
  summaries), coverage, FHIR R4 Observations and the Apple Health upload: 23 to 36 of the 170
  operations of the OpenAPI file ([the list](#what-each-sdk-covers)).
- **Typed records.** One type per schema those routes return, with the same field names in the
  language's own case. Fields the server adds later are ignored, not an error.
- **Paging.** Each list call returns one page (`data` and `nextCursor`); each SDK also gives a
  helper that walks every page for you.
- **Errors.** A non-2xx answer becomes one error type carrying the HTTP status, the RFC 9457
  `title` and `detail`, the `request_id` to quote in a bug report, and `Retry-After` when sent.
- **Upload parts.** A builder turns HealthKit-shaped lines into upload parts the way
  `ohs import apple-health` does: one data type and local day per part, at most 8 MiB before
  compression, gzip compressed, numbered within a batch. Uploads are retried on 429, 503, other
  5xx answers and network errors, honouring `Retry-After`, up to 5 attempts; a 4xx stops at once.
- **No secrets in logs.** Tokens and health values never reach a log line or an error message.

Each SDK also reads the phone's health store and uploads what changed since the last run, in the
foreground and in the background:

| SDK | Reads | How |
|---|---|---|
| Swift | HealthKit | `HealthKitSync` in `OpenHealthSyncHealthKit` ([HealthKit sync](swift.md#healthkit-sync)) |
| Kotlin | Health Connect | `HealthConnectSync` in the `open-health-sync-health-connect` module ([Health Connect sync](kotlin.md#health-connect-sync)) |
| React Native | HealthKit and Health Connect | The native module `open-health-sync-react-native/health`, over the Swift and Kotlin SDKs. Needs a native build, so it does not load in Expo Go ([Health store sync](react-native.md#health-store-sync)) |
| Flutter | HealthKit and Health Connect | The plugin `open_health_sync_health`, over the Swift and Kotlin SDKs ([Health store sync](flutter.md#health-store-sync)) |

The React Native and Flutter readers run the Swift and Kotlin code, so the mapping of records to
upload lines is the same everywhere. The Swift reader has an [iOS example](../../examples/ios/README.md)
app. The React Native module has run on an iOS simulator (it loads and stores its credentials;
no real sync), and the Kotlin reader and the Flutter plugin only in unit tests and builds, on no
device or emulator. Each guide says what was verified.

The Swift and Kotlin SDKs also upload what standard Bluetooth health devices send (straps, cuffs,
scales, glucose meters, thermometers, pulse oximeters): see
[Swift](swift.md#bluetooth-devices) and [Kotlin](kotlin.md#bluetooth-devices). On a computer or
a Raspberry Pi, the [desktop bridge](../bridge.md) does the same without an app.

## What each SDK covers

Read from the public methods of each client (`OhsClient.kt`, `OHSClient.swift`, `client.ts`,
`client.dart`). The method names are the same in the four SDKs, apart from the case of `FHIR`
in Swift.

| Routes | Methods | Kotlin | Swift | React Native | Flutter |
|---|---|---|---|---|---|
| `GET /v1/providers` | `listProviders` | Yes | Yes | Yes | Yes |
| Users: create, list, get, delete, and `GET /v1/deletions/{job_id}` | `createUser`, `listUsers`, `getUser`, `deleteUser`, `getDeletion` | Yes | Yes | Yes | Yes |
| `POST /v1/users/{id}/tokens` | `mintUserToken` | Yes | Yes | Yes | Yes |
| Connections: list, start, delete, and freshness | `listConnections`, `startConnection`, `deleteConnection`, `getFreshness` | Yes | Yes | Yes | Yes |
| Reads: samples, category samples, sleep and phases, workouts and route, nutrition, daily | `listSamples`, `listCategorySamples`, `listSleep`, `listSleepPhases`, `listWorkouts`, `getWorkoutRoute`, `listNutrition`, `listDaily` | Yes | Yes | Yes | Yes |
| `GET /v1/users/{id}/coverage` | `getCoverage` | Yes | Yes | Yes | Yes |
| FHIR R4 Observation: search and read | `searchFhirObservations`, `readFhirObservation` | Yes | Yes | Yes | Yes |
| `POST /v1/ingest/apple-health` | `uploadAppleHealthPart`, `uploadParts` | Yes | Yes | Yes | Yes |
| `POST /v1/ingest/bluetooth` | `uploadBluetoothPart` | Yes | Yes | No | No |
| `POST /v1/ingest/health-connect` | `uploadHealthConnectPart` | Yes | No | No | No |
| Hosted connection page, `POST /v1/users/{id}/connect-sessions` | `createConnectSession` | No | Yes | No | No |
| Sample stats, RR intervals, HRV, ECG list and detail | `sampleStats`, `listRRIntervals`, `listHRV`, `listECGRecordings`, `getECGRecording` | No | Yes | No | No |
| Time tags: create, list, get, update, delete | `createTimeTag`, `listTimeTags`, `getTimeTag`, `updateTimeTag`, `deleteTimeTag` | No | Yes | No | No |
| `POST /v1/users/{id}/self-reports` | `createSelfReports` | No | Yes | No | No |
| Reads HealthKit on the device | `HealthKitSync`; the React Native and Flutter modules | No | Yes | Yes, native module | Yes, plugin |
| Reads Health Connect on the device | `HealthConnectSync`; the React Native and Flutter modules | Yes, `health-connect` module | No | Yes, native module | Yes, plugin |
| Standard Bluetooth health devices | `BluetoothRecorder` decodes the GATT values; `BluetoothConnector` (Swift only) also scans and connects | Decodes values your app's Bluetooth code receives | Yes | No | No |
| Operations of the API covered | | 25 of 170 | 36 of 170 | 23 of 170 | 23 of 170 |

Not covered by any of the four: consent, webhook endpoints and the event stream, exports and the
user archive, lab results, the other FHIR resources, Open mHealth, share links, menstrual cycles,
sync runs, API keys, the audit log and the admin routes. Call those from your backend with a [server SDK](../server-sdks.md), which has one
method per operation, or over plain HTTP.

## Which credential goes where

An app on a phone must never hold a tenant API key (`ohs_sk_`): anyone can pull it out of the
app and read every user of the tenant. The API splits the work this way:

| Who | Credential | Calls |
|---|---|---|
| Your backend | tenant key | everything except the upload: users, tokens, connections and `startConnection`, freshness, every read route, coverage, FHIR |
| The app | user token with the `upload` scope, minted by your backend | the uploads (`uploadAppleHealthPart`, `uploadParts`, `uploadBluetoothPart`, `uploadHealthConnectPart`), the health store readers, and in Swift `createSelfReports` for its own user |

A user token gets 403 on every other route the SDKs call: they are tenant routes. (A token with
the `read` scope reads only through the [MCP server](../mcp.md), for a person's own AI agent,
and the user archive.)

1. Your backend creates the user and mints a short-lived upload token with
   `mintUserToken(userId, ["upload"])`.
2. The app asks your backend for that token and builds the client with it. Each SDK takes a token
   provider function, called before every request, so the app can fetch a new token when the
   old one expires without rebuilding the client.
3. To show data or connect a provider, the app calls your own backend, which calls Open Health
   Sync with the tenant key (with the same SDK when it is written in Kotlin, Swift or
   TypeScript) and returns the records or the consent URL.

## Upload parts from an app

The Apple Health upload route takes one gzip NDJSON part per call
([contract](../connectors/apple-health.md#upload-contract)). Each line is one HealthKit record in
the [line format](../connectors/apple-health.md#line-formats) the server reads. With the part
builder you add lines and get parts back:

| Rule | Why |
|---|---|
| One HealthKit type per part | `X-OHS-Type` names the type of every line |
| One local day per part | `X-OHS-Day` is part of the raw key |
| At most 8 MiB of NDJSON per part | small enough to retry cheaply on a phone network |
| Part numbers unique within a batch, across types | `(day, batch, part)` is the raw key; a clash is a 409 |
| A fresh batch id per run, of letters, digits, `_` and `-` | new data needs a new key |

Re-sending a part with the same bytes is safe: the server answers `duplicate: true`. After the
upload, poll `getCoverage` until `pendingJobs` is 0 to know every part has been processed.

## Versioning

The four SDKs are at 0.1.0 and the server at 0.3.0: the numbers are separate and do not follow
each other. No SDK release has been tagged or published yet, so the SDK you install is the one in
the commit you check out; CI runs each SDK's tests and quickstart against a server built from the
same commit. While the SDKs are 0.x, a minor release may change a signature.
