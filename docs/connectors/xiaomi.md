# Xiaomi connector

Slug `xiaomi`. Xiaomi bands and watches (Xiaomi Smart Band, Redmi Watch, Xiaomi Watch) sync to
the **Mi Fitness** app (formerly Xiaomi Wear), and older Mi Bands to **Zepp Life** (formerly Mi
Fit). Xiaomi publishes no health API for third parties, so Open Health Sync reads Xiaomi data in
two ways, both through official, user-controlled paths:

| Path | What it brings | How |
|---|---|---|
| Mi Fitness data copy | The full history kept in the Xiaomi cloud: heart rate, resting heart rate, SpO2, steps, distance, active energy, weight and body fat, VO2 max, sleep with stages, workouts | The user downloads a copy of their data from the Xiaomi account privacy center, and `ohs import mi-fitness` uploads it to `POST /v1/ingest/xiaomi` |
| Health Connect and Apple Health | Ongoing data, as Mi Fitness or Zepp Life syncs it | Turn on the app's Health Connect (Android) or Apple Health (iPhone) sync; the data reaches the server through the [Health Connect](health-connect.md) or [Apple Health](apple-health.md) connector |

The copy is the backfill: years of history, once or whenever the user asks for a fresh copy.
Health Connect and Apple Health carry what comes after. No reverse-engineered or private Xiaomi
API is used.

## 1. Get the data copy

The user, signed in to their Xiaomi account:

1. opens [account.xiaomi.com](https://account.xiaomi.com), then **Privacy**, **Manage your
   data**;
2. picks **Mi Fitness** and its download button, and binds an email address when asked;
3. receives a download link by email, usually within minutes.

The copy is a set of CSV files named `<date>_<account id>_MiFitness_<table>.csv` and a PDF
guide. Unzip it if it came as a password protected zip; `ohs import` reads a folder or a plain
zip.

## 2. Import it

```sh
export OHS_URL=https://ohs.example.org
export OHS_TOKEN=ohs_ut_...        # a user token with the upload scope
ohs import mi-fitness ~/Downloads/MiFitness
```

`--dry-run` converts and counts without uploading; `--since 2026-01-01` keeps rows from that
UTC day on. Running the same import again uploads nothing twice: the batch id is derived from the
files, and parts already stored are answered as duplicates. The command prints counts and key
names only, never a health value, the token or the account id.

## 3. Auth flow

No OAuth and no operator keys: the connector is always on. The data copy is requested by the user
in their own Xiaomi account, and uploaded with a user token (`ohs_ut_`) that has the `upload`
scope, like an Apple Health export. The server never holds Xiaomi credentials.

## 4. Rate limits

Nothing is pulled from Xiaomi, so there is no provider rate limit to respect. Uploads share the
instance's limit of uploads in flight with the other device routes (`503` with `Retry-After`
above it); `ohs import` retries those, `429` and `5xx` with backoff, and sends at most
`--concurrency` parts at once (default 4).

## 5. Backfill and updates

The copy holds everything the Xiaomi cloud keeps for the account, so one import is the full
backfill. For later data, either request a new copy and import it again (rows already stored are
updated in place, never duplicated), or turn on the app's Health Connect or Apple Health sync.

## 6. Upload contract

`POST /v1/ingest/xiaomi` works like the [Apple Health upload](apple-health.md): a user token
with the `upload` scope, the headers `X-OHS-Type` (the table: `fitness_data` or
`sport_record`), `X-OHS-Day`, `X-OHS-Batch` and `X-OHS-Part`, and a gzip NDJSON body of at most
64 MiB. One line per row of the copy:

- `table`: `fitness_data` (readings) or `sport_record` (workouts). Required.
- `key`: the row's key (`heart_rate`, `sleep`, `outdoor_running`...). Required.
- `sid`: the data source (device) id; `time`: the row's Unix time in seconds; `updateTime`;
  `category`: the sport category of a workout.
- `device`: `model` and `name` of the data source, from the copy's data source table. The alias
  the user gave the device is left out, and so is the account id.
- `value`: the row's JSON value, as Xiaomi wrote it.

```json
{"table":"fitness_data","sid":"2000000001","key":"heart_rate","time":1772928240,"updateTime":1772931331,"device":{"model":"band.example.m1","name":"Xiaomi Smart Band 8"},"value":{"time":1772928240,"bpm":59,"type":0}}
```

A line of another table or key is stored and skipped, so nothing is lost if a later version maps
it. A line that is not a JSON object, or lacks `table` or `key`, refuses the whole part with 400.

## 7. Mapping

Times are Unix seconds. `timezone`, when a value has it, is the offset from UTC in quarters of an
hour (`4` is UTC+1); it is kept as `utc_offset_minutes` and, for a whole hour, as an `Etc/GMT`
zone in `Metadata["timezone"]`, which files the record under its local day.

| Table / key | Value fields | Canonical records |
|---|---|---|
| `fitness_data` / `heart_rate`, `single_heart_rate` | `time`, `bpm` | `heart_rate` (bpm), 20 to 250 |
| `fitness_data` / `resting_heart_rate` | `date_time`, `bpm` | `resting_heart_rate` (bpm) |
| `fitness_data` / `spo2` | `time`, `spo2` | `oxygen_saturation` (percent), 50 to 100 |
| `fitness_data` / `steps` | `time`, `steps`, `distance` (m) | `step_count` and `distance_walking_running` over one minute |
| `fitness_data` / `calories` | `time`, `calories` (kcal) | `active_energy_burned` over one minute |
| `fitness_data` / `weight` | `weight` (kg), `body_fat_rate` (%) | `body_mass` (kg), `body_fat_percentage` |
| `fitness_data` / `vo2_max` | `time`, `vo2_max` | `vo2_max` (mL/kg/min) |
| `fitness_data` / `sleep`, `watch_night_sleep`, `watch_daytime_sleep` | `bedtime`, `wake_up_time`, `items` (`start_time`, `end_time`, `state`), stage minutes, `sleep_score`, `avg_hr`; a daily report holds its sleeps in `segment_details` | one sleep session per sleep, with phases: `state` 2 deep, 3 light, 4 REM, 5 awake. Without `items`, the stage minutes give the totals |
| `sport_record` / any sport key | `start_time`, `end_time`, `duration` (s), `distance` (m), `calories` (kcal), `avg_hrm`, `max_hrm`, `rise_height` (m), `vo2_max` | a workout; `outdoor_running` is `running`, `outdoor_walking` `walking`, `outdoor_riding` `cycling`, `pool_swimming` `swimming`, `dumbbell_training` `traditional_strength_training`; other keys are kept as they are |

Not mapped yet, stored raw: stress, HRV (`avg_hrv` of a sleep: Xiaomi does not say whether it is
RMSSD or SDNN, and the two never share a key), skin and body temperature, blood pressure, the
calories of a steps row (the `calories` key carries them), daily aggregates, GPS tracks (the copy
holds only expiring download links).

Zero figures are placeholders in the copy and give no record. Per-minute rows (`steps`,
`calories`) are deltas, so their sum over a day is the day's total.

## 8. Provenance

Every record of the copy has provider `xiaomi`, source slug `xiaomi`, source name `Mi Fitness`,
the device model (`DeviceModel`, and `DeviceID` as `Xiaomi <model>`) and, in metadata, the
device name, `mi_table`, `mi_key`, `mi_sid` and `mi_update_time`. FHIR output carries the
provider, source and device as [Provenance](../fhir.md) like any other source.

Records that Mi Fitness (`com.xiaomi.wearable`), Mi Health (`com.mi.health`) or Zepp Life
(`com.xiaomi.hm.health`) write to Health Connect keep provider `health_connect` and get the
source slug `xiaomi`, so fusion ranks the Xiaomi device rather than the pipe. Data they write to
Apple Health arrives with the app's name and bundle id as its source.

A reading both imported from the copy and synced through Health Connect is stored under each
provider, both with source slug `xiaomi`. To count it once, read one provider for the period both
cover.

## 9. Testing

`connectors/xiaomi/export/testdata/mi_fitness` is an invented copy laid out like Xiaomi's (one
file wraps every line in an extra pair of quotes, as some copies do).
`connectors/xiaomi/testdata/upload.ndjson` is its conversion, which the mapping tests read;
regenerate it with `go test ./connectors/xiaomi/export -update`. If a real copy reads
differently, open an issue with the table and key names and the shape of the value (never the
values themselves).
