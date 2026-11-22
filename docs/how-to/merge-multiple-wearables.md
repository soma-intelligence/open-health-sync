# Combine data from several wearables without counting it twice

Open Health Sync stores every source's records as received and, by default, reads them through a
merged view (`view=merged`): a record is listed only when no better ranked source has a matching
one, so a sum or an average counts nothing twice. You choose which source wins per metric with
`PUT /v1/users/{id}/source-priorities`, and `view=all` still gives you every record.

A ring and a watch both record the night, a phone and a watch both count steps, a sample arrives
directly and again through an aggregator. The merged view applies to `samples`,
`category-samples`, `sleep`, `workouts`, `daily`, the FHIR search and the exports.

## 1. Start an instance with overlapping sources

Follow the [quickstart](../../README.md#quickstart). `ohs seed --demo` writes 90 days from six
sources that overlap the way real devices do (an Oura ring and a WHOOP strap both record sleep,
for instance), so you can try everything below before connecting your own
([demo data](../demo.md)). With real accounts, connect each one (`go run ./recipes/connect oura`,
then `whoop`, and so on); see the [connectors](../connectors/README.md).

## 2. Read the merged view and compare it with every record

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/sleep?start=2026-10-01&end=2026-10-08" \
  -H "Authorization: Bearer $OHS_KEY"                  # one session per night
curl -s "$OHS_URL/v1/users/$OHS_USER/sleep?start=2026-10-01&end=2026-10-08&view=all" \
  -H "Authorization: Bearer $OHS_KEY"                  # every source's sessions
```

Every record carries `provider`, `source`, `device` and `ingested_at`, so you always know where a
value came from. Two records of one metric match when they are close in time:

| Kind | Example metrics | Match when |
|---|---|---|
| counts | `step_count`, `active_energy_burned`, distances | the intervals overlap (a day total overlaps the whole day) |
| readings | `heart_rate`, `oxygen_saturation`, `respiratory_rate`, blood pressure | within 5 minutes |
| nightly or daily figures | `resting_heart_rate`, `hrv_rmssd`, `hrv_sdnn`, scores | within 12 hours |
| body measurements | `body_mass`, `body_fat_percentage`, `body_temperature` | within an hour |
| sessions | sleep sessions, workouts | they share at least half of the shorter one (a nap beside the night stays) |
| anything else | | the intervals overlap |

## 3. Choose which source wins

```sh
curl -s -X PUT "$OHS_URL/v1/users/$OHS_USER/source-priorities" -H "Authorization: Bearer $OHS_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"priorities": {"sleep": ["oura", "whoop"], "step_count": ["apple_health"]}}'
```

Keys are a metric (a quantity type, `sleep` or `workouts`), a group (`activity`, `heart`,
`recovery`, `body`, `sleep`, `workouts`) or `*` for every metric. The most specific order wins:
the metric, then its group, then `*`, then the built-in order of the group. A source missing from
an order ranks after every listed one. Within one source, a stream worn on the body (a watch)
ranks before the others (a phone).

`GET .../source-priorities` returns your `priorities` and the built-in `defaults`;
`{"priorities": {}}` goes back to the defaults. Stored records never change: the order only
decides what the merged view shows, so you can change it at any time.

## 4. Read one figure per day

In the merged view `daily` holds one summary per local day. Each figure comes from the best ranked
source that has it, and `sources` names that source per figure:

```json
{"date": "2026-03-02", "step_count": 10000, "readiness_score": 80, "resting_hr": 50,
 "sources": {"step_count": "apple_health", "readiness_score": "oura", "resting_hr": "whoop"}, ...}
```

With `view=all`, `daily` holds one row per provider and day.

## 5. Pick the order from your own data

The `compare-sources` recipe shows how far two devices are apart on the days both measured:

```sh
go run ./recipes/compare-sources                       # steps, last 14 days
go run ./recipes/compare-sources -metric sleep         # also resting_hr, hrv
go run ./recipes/compare-sources -metric resting_hr -days 30
```

It prints one column per provider, the typical gap and which one reads higher
([recipe](../recipes/compare-sources.md)).

## Where nothing is merged

- `samples/stats` returns one series per provider, source and unit. A phone and a watch that both
  count steps stay two series: pick one, or the largest, rather than adding them up.
- Nutrition logs and ECG recordings have no merge: two apps logging one meal are two entries.
- A full backup export should pass `view=all`:

  ```sh
  curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/daily?format=csv&view=all" \
    -H "Authorization: Bearer $OHS_KEY"
  ```

## Related

- [API guide: the merged view](../api.md#several-sources-of-one-metric-the-merged-view)
- [Export WHOOP data to CSV or Parquet](whoop-export.md)
- [Export Withings weight and blood pressure](withings-export.md)
- [Share your health data with your clinician](share-with-clinician.md)
- [Get Oura data as FHIR R4](oura-fhir.md)
- [README](../../README.md)
