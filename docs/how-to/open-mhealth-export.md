# Export wearable data as Open mHealth JSON

To get wearable data as Open mHealth JSON, call `GET /v1/users/{id}/omh/data-points` on Open
Health Sync: it returns a user's records from every connected source as Open mHealth data points,
each a `header` and a `body` that follows one schema of the Open mHealth catalog. Every mapping is
checked against the official schemas in CI. Open Health Sync is open source and self-hosted.

A research pipeline, a mobile health study or an analysis notebook built on the Open mHealth
schemas can then read Oura, WHOOP, Withings, Apple Health and the other sources without learning
each vendor's JSON.

## 1. Start an instance with data

Follow the [quickstart](../../README.md#quickstart), then fill it with 90 days of synthetic
data from six sources and note the key and user id it prints ([demo data](../demo.md)):

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs seed --demo
export OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... OHS_USER=...   # from its output
```

For real data, connect a [provider](../connectors/README.md) or import a file first.

## 2. Read data points

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/omh/data-points?schema=omh:heart-rate&start=2026-03-01&limit=1" \
  -H "Authorization: Bearer $OHS_KEY"
```

```json
{
  "data": [
    {
      "header": {
        "id": "qs-101",
        "creation_date_time": "2026-03-01T09:15:30.25Z",
        "schema_id": { "namespace": "omh", "name": "heart-rate", "version": "2.0" },
        "acquisition_provenance": { "source_name": "apple_health", "source_data_point_id": "hk-uuid-1", "modality": "sensed" },
        "user_id": "3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11"
      },
      "body": {
        "effective_time_frame": { "date_time": "2026-03-01T08:00:00Z" },
        "heart_rate": { "value": 62, "unit": "beats/min" }
      }
    }
  ],
  "next_cursor": "MjAyNi0wMy0wMVQwODowMDowMFp8MXwxMDE"
}
```

| Parameter | Meaning |
|---|---|
| `schema` | one schema: `heart-rate`, `omh:heart-rate` or `omh:heart-rate:2.0`; a schema the mapping does not emit is a 400 |
| `start`, `end` | RFC 3339 or `YYYY-MM-DD`; `start` <= the record's time < `end` |
| `limit`, `cursor` | page size (default 100, at most 1000) and the previous page's `next_cursor` |
| `view` | `merged` (default: one source per figure when several measured the same thing) or `all` ([why](../api.md#several-sources-of-one-metric-the-merged-view)) |

`acquisition_provenance.source_name` names the provider each data point came from.

## 3. Save every page to a file

A page can hold fewer than `limit` data points and still have a `next_cursor`, so follow it until
it is null. The cursor is URL-safe:

```sh
cursor=""
: > data-points.ndjson
while :; do
  page=$(curl -s "$OHS_URL/v1/users/$OHS_USER/omh/data-points?view=all&limit=1000${cursor:+&cursor=$cursor}" \
    -H "Authorization: Bearer $OHS_KEY")
  echo "$page" | jq -c '.data[]' >> data-points.ndjson
  cursor=$(echo "$page" | jq -r '.next_cursor // empty')
  [ -z "$cursor" ] && break
done
```

Or take everything at once: the user archive, `GET /v1/users/{id}/archive`, is a zip that holds
`omh/data-points.ndjson` next to the FHIR bundle and the canonical JSON lists
([user archive](../exports.md#complete-user-archive-data-portability)).

## What maps to which schema

| Records | Schema |
|---|---|
| `heart_rate`, `resting_heart_rate` | `omh:heart-rate:2.0` |
| `step_count` | `omh:step-count:3.0` |
| `active_energy_burned` | `omh:calories-burned:2.0` |
| `body_mass`, `height`, `body_mass_index`, `body_fat_percentage` | `omh:body-weight:2.0`, `omh:body-height:1.0`, `omh:body-mass-index:2.0`, `omh:body-fat-percentage:1.0` |
| `body_temperature`, `oxygen_saturation`, `respiratory_rate` | `omh:body-temperature:3.0`, `omh:oxygen-saturation:2.0`, `omh:respiratory-rate:2.0` |
| `blood_glucose` | `omh:blood-glucose:3.0` |
| a systolic and diastolic pair | `omh:blood-pressure:3.1` |
| a sleep session | `omh:sleep-episode:1.1` |
| a workout | `omh:physical-activity:1.2` |

Not in this version: daily summaries (Open mHealth has no daily summary schema), heart rate
variability, VO2 max and the scores, category samples, nutrition, sleep phases, routes, IEEE
1752.1 data points, and a single data point by id. The [Open mHealth reference](../open-mhealth.md)
has the header and body mapping and the schema versions.

## Need FHIR instead?

The same records, in the same order and with the same ids, are served as FHIR R4 Observations
under `/v1/users/{id}/fhir`, which also covers daily summaries, HRV, ECG and nutrition. See
[FHIR R4 output](../fhir.md) and [Get Oura data as FHIR R4](oura-fhir.md).

## Related

- [Get Oura data as FHIR R4](oura-fhir.md)
- [Export WHOOP data to CSV or Parquet](whoop-export.md)
- [Combine data from several wearables without counting it twice](merge-multiple-wearables.md)
- [Get a webhook when new wearable data arrives](webhooks-new-data.md)
- [Open mHealth reference](../open-mhealth.md)
- [README](../../README.md)
