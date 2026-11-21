# Send Android Health Connect data to your own server

Health Connect has no server API, so the data has to leave from the phone: your Android app
reads the records it was granted and uploads them as gzip NDJSON parts to
`POST /v1/ingest/health-connect` on an Open Health Sync server you run. The server maps them into
the same tables as every other source, so you read them over the API, in SQL or as exports. The
Kotlin SDK that will read Health Connect and upload in this format is still being built, so
today your app implements the upload contract itself.

## 1. Start an instance

Follow the [quickstart](../../README.md#quickstart) to *Start the stack*, then create a user and
keep its details in a file:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
```

That gives `OHS_URL`, `OHS_KEY` (the tenant key) and `OHS_USER`. The Health Connect route needs
no provider keys: the connector is always on.

## 2. Give the app an upload token

The tenant key stays on your backend, never in the app. Your backend mints a user token with
the `upload` scope (one hour by default, at most 24 hours) and hands it to the app:

```sh
curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" -H "Authorization: Bearer $OHS_KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload"],"ttl_seconds":3600}'
# {"token":"ohs_ut_...","expires_at":"...","scopes":["upload"]}
```

A backend on the JVM can use the [Kotlin SDK](../sdks/kotlin.md) for this
(`ohs.mintUserToken(user.id, listOf("upload"))`). The token can upload and nothing else; every
read route answers it 403 ([which credential goes where](../sdks/README.md#which-credential-goes-where)).

## 3. Ask for the right permissions

- An app reads only the record types the user granted.
- By default it reads back 30 days before its first permission grant. Older history needs
  `PERMISSION_READ_HEALTH_DATA_HISTORY`.
- Reading in the background needs `READ_HEALTH_DATA_IN_BACKGROUND` (Android 14+ or a recent
  Health Connect). Without it, sync runs only while the app is in the foreground.

## 4. Write one record per line

Each line is one Health Connect record as JSON, with field names from the androidx
`androidx.health.connect.client.records` classes and the unit in the name:

```json
{"recordType":"Steps","metadata":{"id":"st-1","dataOrigin":"com.sec.android.app.shealth","recordingMethod":"automatic"},"startTime":"2026-03-02T08:00:00Z","endTime":"2026-03-02T09:00:00Z","startZoneOffset":"+01:00","endZoneOffset":"+01:00","count":1200}
{"recordType":"Deletion","recordId":"st-0"}
```

- `recordType` is the class name without `Record` (`BloodGlucoseRecord` is `BloodGlucose`).
- `metadata.id` is required; `metadata.dataOrigin` (the package that wrote it) sets the source
  slug, so Samsung Health, Oura or a CGM app stay apart.
- Instant records carry `time` and `zoneOffset`, interval records `startTime`, `endTime`,
  `startZoneOffset` and `endZoneOffset`. The offset sets the local day of daily summaries.
- Enums are lowercase strings without their prefix (`STAGE_TYPE_DEEP` is `deep`).

Steps, heart rate, resting heart rate, HRV, SpO2, respiratory rate, body temperature, blood
pressure, blood glucose, weight, height, body fat, energy, distance, sleep sessions with stages,
exercise sessions, menstruation, nutrition and hydration are mapped. Other record types are
stored and skipped. The full field table is in the
[Health Connect connector guide](../connectors/health-connect.md#line-format).

## 5. Upload the parts

The route works like the Apple Health upload: a gzip NDJSON body of at most 64 MiB, the user
token, and four headers.

```sh
gzip -c steps.ndjson | curl -s -X POST "$OHS_URL/v1/ingest/health-connect" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H 'X-OHS-Type: Steps' -H 'X-OHS-Day: 2026-03-02' \
  -H 'X-OHS-Batch: 7f3c' -H 'X-OHS-Part: 0' \
  --data-binary @-
```

| Header | Value |
|---|---|
| `X-OHS-Type` | the `recordType` of the lines in the part |
| `X-OHS-Day` | the day the part belongs to, `YYYY-MM-DD` |
| `X-OHS-Batch` | an id for the upload run: letters, digits, `_` and `-` |
| `X-OHS-Part` | the part number; `(batch, part)` must be unique across the types of one day |

Sending the same bytes again is safe and answers `"duplicate": true`. Other bytes under a key
already taken answer 409, so use a fresh batch id for new data. Status codes and limits are in
the [upload contract](../connectors/apple-health.md#upload-contract).

## 6. Keep it in sync

Health Connect has no push, so the app pulls changes on a schedule
([on-phone stores](../background-sync.md#on-phone-stores-apple-health-health-connect)):

- A `PeriodicWorkRequest` (WorkManager, 15 minutes minimum, with a network constraint) reads
  `getChanges(token)`, uploads the changes, and stores the next changes token only after the
  server acknowledged them.
- Deleted records come back as `DeletionChange`: send their ids as `Deletion` lines. Edited
  records replace the stored value when re-sent.
- A changes token expires after 30 days unused. On `changesTokenExpired`, read the window again
  from the last uploaded time and take a new token.
- Sync on app open too. `GET /v1/users/{id}/freshness` reports the source as `stale` after 26
  hours without an upload, so your backend can nudge the user.

## 7. Check that it landed

Processing is asynchronous. Poll until `pending_jobs` is 0, then read:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage?provider=health_connect" \
  -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
curl -s "$OHS_URL/v1/users/$OHS_USER/samples?type=step_count&start=2026-03-02&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

Records have provider `health_connect`. A non-zero `rejected_records` after that means some lines
were not stored ([why](../api.md#knowing-when-an-upload-is-processed)).

## Where the SDKs stand

- **Kotlin**: version 0.1.0, not on any package registry; you include it from this repository.
  It uploads HealthKit-shaped lines and Bluetooth values. Reading Health Connect is planned and
  not there yet, and the [Android example](../../examples/android/README.md#health-connect) uses
  manual entry until it lands.
- **React Native and Flutter**: a community module reads Health Connect, but their upload helpers
  send HealthKit-shaped lines to the Apple Health route, stored under `apple_health`
  ([React Native](../sdks/react-native.md), [Flutter](../sdks/flutter.md)).

## Related

- [Get Samsung Health data through an API](samsung-health-data.md)
- [Sync HealthKit data from your iOS app to your own backend](healthkit-ios-app-sync.md)
- [Record a Bluetooth heart rate strap, blood pressure cuff or scale without a phone](bluetooth-devices.md)
- [Health Connect connector guide](../connectors/health-connect.md)
- [README](../../README.md)
