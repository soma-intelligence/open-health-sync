# Get Samsung Health data through an API

Samsung Health has no cloud API to pull from: its data lives on the phone. The Samsung Health
app writes to Android's Health Connect, so an Android app reads it there and uploads it to an
Open Health Sync server you run, which serves it over a REST API, in SQL and as exports. No
Samsung partnership is needed, and the records keep the source slug `samsung_health`.

## The routes to Samsung Health data

| Route | What it needs | Status in Open Health Sync |
|---|---|---|
| Health Connect | an Android app with the user's Health Connect permission | supported: `POST /v1/ingest/health-connect` |
| Samsung Health Data SDK | on-device, partner approval to distribute | not used |
| Samsung Health SDK for Android | deprecated on 2025-07-31 | not used |

The Samsung Health data that reaches Health Connect covers activity, sleep, heart rate and skin
temperature, per the [connector list](../connectors/README.md).

## 1. Turn on Samsung Health's Health Connect sync

On the phone, let Samsung Health write to Health Connect, then let your own app read the record
types you want from Health Connect. By default your app reads back 30 days before its first
permission grant; older data needs the history permission
(`PERMISSION_READ_HEALTH_DATA_HISTORY`).

## 2. Upload from your Android app

Your app reads the records and sends them as gzip NDJSON parts, with a user token that has the
`upload` scope. Each line keeps the record's `metadata.dataOrigin`, the package that wrote it:

```json
{"recordType":"HeartRate","metadata":{"id":"hr-1","dataOrigin":"com.sec.android.app.shealth","recordingMethod":"automatic"},"startTime":"2026-03-02T08:00:00Z","endTime":"2026-03-02T08:02:00Z","samples":[{"time":"2026-03-02T08:00:00Z","beatsPerMinute":61},{"time":"2026-03-02T08:01:00Z","beatsPerMinute":63}]}
```

The full walkthrough (token, permissions, headers, background sync, deletions) is in
[Send Android Health Connect data to your own server](health-connect-to-server.md). The Kotlin
SDK that will read Health Connect is still being built, so for now your app implements the
[upload contract](../connectors/health-connect.md#upload-contract) itself.

## 3. Read it back

Records from the Samsung Health app (`com.sec.android.app.shealth`) are stored with provider
`health_connect` and source slug `samsung_health`. Another app that writes to Health Connect gets
its own slug (`oura`, `dexcom`, `google_fit`...), or its package name when it is not on the list,
so the sources stay apart.

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/samples?type=heart_rate&start=2026-03-02&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

With several devices measuring the same thing, the default merged view keeps one source per
figure; `view=all` keeps every source, and source priorities rank them
([merged view](../api.md#several-sources-of-one-metric-the-merged-view)). CSV and Parquet
exports carry the same records ([exports](../exports.md)).

## Related

- [Send Android Health Connect data to your own server](health-connect-to-server.md)
- [Record a Bluetooth heart rate strap, blood pressure cuff or scale without a phone](bluetooth-devices.md)
- [Sync HealthKit data from your iOS app to your own backend](healthkit-ios-app-sync.md)
- [Health Connect connector guide](../connectors/health-connect.md)
- [README](../../README.md)
