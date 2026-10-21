# Client SDKs

Four client libraries call the [REST API](../api.md) from an app: Kotlin, Swift, React Native and
Flutter. They are thin and typed, built from [api/openapi.yaml](../../api/openapi.yaml), and they
share one shape, so what you learn in one carries over to the others.

| SDK | Directory | Runs on | Guide |
|---|---|---|---|
| Kotlin | [`sdks/kotlin`](../../sdks/kotlin) | Android (API 26+), any JVM 11+ | [Kotlin](kotlin.md) |
| Swift | [`sdks/swift`](../../sdks/swift) | iOS 15+, macOS 12+, Linux (client only) | [Swift](swift.md) |
| React Native | [`sdks/react-native`](../../sdks/react-native) | React Native 0.72+, Expo, Node 18+, browsers | [React Native](react-native.md) |
| Flutter | [`sdks/flutter`](../../sdks/flutter) | Flutter 3.22+ and Dart 3.4+ (iOS, Android, desktop, web) | [Flutter](flutter.md) |

**Status: 0.1.0, not on any package registry yet.** Install them from this repository (each guide
says how). Package names and coordinates are set when the first version is published.

**Licence: AGPL-3.0-only**, like the server ([LICENSE](../../LICENSE)).

Example apps for iOS, Android and React Native, built on these SDKs, are in
[examples/](../../examples/README.md).

## What every SDK does

- **Every API route.** Users, tokens, connections, freshness, the read routes (samples, category
  samples, sleep and its phases, workouts and their routes, nutrition, daily summaries),
  coverage, FHIR R4 Observations and the Apple Health upload.
- **Typed records.** One type per schema of the OpenAPI file, with the same field names in the
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

The Swift SDK also reads HealthKit itself and uploads what changed since the last run
([HealthKit sync](swift.md#healthkit-sync)). Reading Health Connect on Android is planned for the
Kotlin SDK.

The Swift and Kotlin SDKs also upload what standard Bluetooth health devices send (straps, cuffs,
scales, glucose meters, thermometers, pulse oximeters): see
[Swift](swift.md#bluetooth-devices) and [Kotlin](kotlin.md#bluetooth-devices). On a computer or
a Raspberry Pi, the [desktop bridge](../bridge.md) does the same without an app.

## Which credential goes where

An app on a phone must never hold a tenant API key (`ohs_sk_`): anyone can pull it out of the
app and read every user of the tenant. The API splits the work this way:

| Who | Credential | Calls |
|---|---|---|
| Your backend | tenant key | everything except the upload: users, tokens, connections and `startConnection`, freshness, every read route, coverage, FHIR |
| The app | user token with the `upload` scope, minted by your backend | `uploadAppleHealthPart`, `uploadParts` and `uploadBluetoothPart` only |

A user token gets 403 on every other route. (A token with the `read` scope reads only through the
[MCP server](../mcp.md), for a person's own AI agent.)

1. Your backend creates the user and mints a short-lived upload token with
   `mintUserToken(userId, ["upload"])`.
2. The app asks your backend for that token and builds the client with it. Each SDK takes a token
   provider function, called before every request, so the app can fetch a new token when the
   old one expires without rebuilding the client.
3. To show data or connect a provider, the app calls your own backend, which calls Open Health
   Sync with the tenant key (with the same SDK when it is written in Kotlin, Swift or
   TypeScript) and returns the records or the consent URL.

## Upload parts from an app

The upload route takes one gzip NDJSON part per call
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

The SDKs follow the API: an SDK release names the server versions it was tested against in its
changelog. While the server is 0.x, a minor SDK release may change a signature.
