# Apple Health connector

Slug `apple_health`. A device connector: an iOS app (yours, or one built on the [Swift SDK](../sdks/swift.md#healthkit-sync)) reads HealthKit
and uploads the data, the server never talks to Apple. It needs no operator keys and is always enabled.
Without an app, `ohs import apple-health` loads the export the Health app writes, history
included ([below](#importing-a-health-app-export)).

## Upload contract

`POST /v1/ingest/apple-health` stores one **part**: a gzip compressed NDJSON file.

| | |
|---|---|
| Auth | `Authorization: Bearer <user token>` with the `upload` scope |
| Body | gzip of NDJSON: one JSON object per line, at least one line, each line at most 32 MiB uncompressed |
| `Content-Type` | not checked; send `application/gzip` |
| Size | at most 64 MiB of gzip bytes per part, 413 above it. A part is never truncated. A part that decompresses to more than 512 MiB is also refused with 413. |

Headers, all required:

| Header | Value |
|---|---|
| `X-OHS-Type` | HealthKit type identifier of every line of the part, for example `HKQuantityTypeIdentifierHeartRate`. Letters, digits, `_` and `-`, at most 128. |
| `X-OHS-Day` | The day the part belongs to, `YYYY-MM-DD`. It is part of the raw key. |
| `X-OHS-Batch` | An id for the upload run, letters, digits, `_` and `-`, at most 128. |
| `X-OHS-Part` | Integer from 0 to 1000000, the part number in the batch. |

A part holds one data type. To upload more, send more parts.

**Keys and duplicates.** The raw object is stored at
`raw/<tenant>/<user>/apple_health/<day>/<batch>_<part>.ndjson.gz`. The type is recorded with the
object but is not part of the key, so **`(batch, part)` must be unique across all the types you
upload for one day**: two types sent as the same batch and part collide. Re-sending the same
bytes is safe and answers 200 with `"duplicate": true`. Different bytes under a key that is
already taken answer 409; a raw file is never rewritten. Use a fresh batch id for new data.

Responses:

| Status | Meaning |
|---|---|
| 200 | `{"key": "raw/...", "duplicate": false}`: stored and queued for processing |
| 400 | A header is missing or invalid (the message names it), the body is empty, not gzip, truncated, or a line is not a JSON object |
| 401, 403 | Missing or expired token, a token without the `upload` scope, or a user being deleted (their tokens stop working at once) |
| 409 | Other bytes are already stored under this day, batch and part, whatever their type: batch and part must be unique across types |
| 410 | The user started being deleted while this upload was being stored (its token was accepted just before); nothing is kept |
| 413 | Over 64 MiB of gzip, or over 512 MiB decompressed |
| 503 | Too many uploads in progress on this instance; `Retry-After` says when to retry |

Processing is asynchronous. Poll `GET /v1/users/{id}/coverage` until `pending_jobs` is 0, then
read the data (see [api.md](../api.md#knowing-when-an-upload-is-processed)).

## Line formats

Each line is routed by the fields it carries, not by `X-OHS-Type`.

Quantity samples:

```json
{"quantityType":"HKQuantityTypeIdentifierHeartRate","startDate":"2026-03-01T14:30:00Z","endDate":"2026-03-01T14:30:00Z","value":72.5,"unit":"count/min","sourceName":"Apple Watch","sourceBundleId":"com.apple.health.ABC","device":"Apple Watch","deviceModel":"Watch","productType":"Watch7,1","wasUserEntered":false,"metadata":{"HKTimeZone":"Europe/Paris"}}
```

`value` and `unit` are what HealthKit reports; the server converts to the storage unit (here
`count/min` becomes `bpm`, a 0.97 oxygen saturation fraction becomes 97 percent). The
`HKTimeZone` metadata key is kept as the sample's `timezone` and decides its local day.
A line of a quantity or category type the server does not know, or a quantity line without
`value`, is skipped without an error: nothing is stored for it and it does not appear in
coverage. The server counts unknown types and logs each one once (`apple_health: unknown type
skipped`, with the HealthKit identifier), so a missing mapping shows in the logs.

Other line kinds are recognized by their discriminator field:

| Field | Becomes |
|---|---|
| `quantityType` (dietary identifiers) | nutrition logs |
| `categoryType` | category samples; `HKCategoryTypeIdentifierSleepAnalysis` makes sleep sessions and phases |
| `workoutActivityType` | workouts |
| `correlationType` | blood pressure pairs, food entries |
| `classification` | ECG recording metadata |
| `timeSinceSampleStart` and `value` | ECG voltage samples |
| `heartbeats` | beat to beat RR intervals (see below) |
| `latitude` and `longitude` | workout route points |

ECG recordings and their voltage samples are served by `GET /v1/users/{id}/ecg` and as FHIR
Observations ([api.md](../api.md#ecg-recordings)); heartbeat intervals by
`GET /v1/users/{id}/rr-intervals` and `/hrv`. Clinical record and FHIR resource lines are
accepted and dropped on purpose.

### Sleep

HealthKit records a night as separate samples: at most one "in bed" sample (value `0`, from the
iPhone's sleep schedule or a sleep app) and the watch's stage samples (`2` awake, `3` core, `4`
deep, `5` REM, `1` asleep with no stage). Uploads file each sample under the local day it
starts on, so a night that crosses midnight arrives in two parts. The server puts each night
back together, whichever part is processed first:

- Samples less than an hour apart belong to the same night. A longer gap starts another one,
  so a nap is a session of its own.
- A night with an in bed sample is that session: `bedtime_start` and `bedtime_end` are the time
  in bed, and each stage goes to the in bed session that contains it, or else to the nearest.
- A night with no in bed sample becomes one session from its first stage to its last, with
  `"synthesized": true` in its metadata.
- The figures are sums of the stages: `deep_sleep_seconds`, `rem_sleep_seconds`,
  `light_sleep_seconds` (core and unstaged sleep) and `awake_seconds`. `total_sleep_seconds` is
  deep, REM and light together, the Health app's "time asleep".

Nights stored by 0.1.0 can be split at midnight, have no figures or hold stages of another
night. To rebuild them, delete the person's Apple Health sleep in `psql`, then re-process their
raw files with the recipe in [self-hosting.md](../self-hosting.md#operating-notes):

```sql
DELETE FROM ohs_data.sleep_phases p USING ohs_data.sleep_sessions s
WHERE p.tenant_id = s.tenant_id AND p.user_id = s.user_id AND p.session_id = s.id
  AND s.provider = 'apple_health' AND s.tenant_id = '<tenant id>' AND s.user_id = '<user id>';
DELETE FROM ohs_data.sleep_sessions
WHERE provider = 'apple_health' AND tenant_id = '<tenant id>' AND user_id = '<user id>';
```

### Heartbeat series

HealthKit gives beat times, not intervals, so a series is one line carrying all its beats:

```json
{"startDate":"2026-03-01T14:30:00Z","uuid":"series-1","heartbeats":[{"timeSinceSeriesStart":0.0,"precededByGap":false},{"timeSinceSeriesStart":0.82,"precededByGap":false},{"timeSinceSeriesStart":1.65,"precededByGap":true}]}
```

Each beat after the first yields one RR interval in milliseconds, stamped at the beat's own
time. A beat with `precededByGap: true` follows missing data, so no interval is produced for
it. A beat that is not after the previous kept one is dropped. A line with a single beat and no
`heartbeats` array is rejected, because one beat gives no interval.

### Example files

Ready to upload, one NDJSON line per record:

- `connectors/applehealth/testdata/e2e/`: a full day (2026-01-10) of heart rate, steps, sleep,
  workouts and SpO2, as the end to end suite uploads it. Each file is one part, named
  `<part>_<type>.ndjson`: gzip it and send it with `X-OHS-Part` and `X-OHS-Type` taken from the
  name, `X-OHS-Day: 2026-01-10` and one `X-OHS-Batch` for the whole set.
- `connectors/applehealth/testdata/ios_upload_lines.ndjson`: the lines an iOS client sends,
  several kinds mixed (mixed lines in one part are accepted).

The other files in `connectors/applehealth/testdata/` are unit test inputs, one record per file
and pretty printed (`mixed_types.json` is a JSON array), so they are not upload ready as they
are; `jq -c 'if type == "array" then .[] else . end' FILE` turns one into NDJSON. Some are
invalid on purpose: `bad_values.json` (a value that is not a number) and `missing_fields.json`
(no `startDate`) are rejected and counted in `rejected_records`, and `unknown_type.json` and
the unknown line of `mixed_types.json` are skipped.

## Importing a Health app export

The Health app on the iPhone exports everything it holds: tap your picture, then *Export All
Health Data*. That gives `export.zip`, with `apple_health_export/export.xml` (every record),
`workout-routes/*.gpx` and `electrocardiograms/*.csv`. `ohs import apple-health` turns it into
the lines above and uploads them through `POST /v1/ingest/apple-health`, like an app would. The
server needs nothing more; the command needs no database and runs on any machine with the
`ohs` binary, a Raspberry Pi included.

```sh
ohs import apple-health export.zip --dry-run                      # convert and count only
OHS_TOKEN=ohs_ut_... ohs import apple-health export.zip --url https://ohs.example.org
```

| Flag | |
|---|---|
| `--url` | Base URL of the instance (default `OHS_URL`). https, unless the host is localhost or `--allow-http` is given |
| `--token` | A user token (`ohs_ut_`) with the `upload` scope (default `OHS_TOKEN`, which keeps it out of the shell history) |
| `--since YYYY-MM-DD` | Keep samples whose local day is on or after this day |
| `--tz ZONE` | IANA zone for samples without `HKTimeZone` (see time zones below) |
| `--batch ID` | Batch id; by default derived from the export (see below) |
| `--dry-run` | Convert and count, upload nothing |
| `--concurrency N` | Parts uploaded at once, default 4 |

It reads `export.xml` token by token straight out of the zip, without extracting it: memory
stays around 150 MB whatever the size of the export (a 470 MB `export.xml` converts in about 35
seconds on a laptop core). An extracted `export.xml` works too; the route and ECG folders are
then read from next to it. It prints counts and HealthKit type names, never a health value or
the token.

**What is converted.**

| In the export | Line |
|---|---|
| `Record` of a quantity type the server stores | quantity line; dietary types become nutrition logs |
| `Record` of a category type the server stores | category line; the `HKCategoryValue...` name becomes HealthKit's integer (sleep stages included) |
| `Record` `HKDataTypeIdentifierHeartbeatSeries` | heartbeat series line |
| `Correlation` blood pressure, food | correlation line with its `objects` |
| `Workout` | workout line: duration in seconds, distance in metres and energy in kilocalories from the attributes or the `WorkoutStatistics`, heart rate average, min and max, `WorkoutEvent`s kept as `workoutEvents` in the raw line |
| `WorkoutRoute` (its GPX file) | route point lines, in the same part as their workout |
| `Audiogram` | audiogram line |
| `electrocardiograms/*.csv` | ECG recording line and its voltage lines (microvolts in the file, volts on the line), one part per ECG. The name and birth date in the file header are never read |

Every `MetadataEntry` goes to the line's `metadata`; `HKWasUserEntered` also sets
`wasUserEntered`; the `device` description gives `device`, `deviceModel`, `productType`,
`deviceManufacturer` and `deviceSoftwareVersion`. Anything else (`ActivitySummary`, clinical
records, types the server does not store) is left out and counted by type in the summary, as is
a record that cannot be read (a date or value that does not parse).

**Time zones.** `export.xml` writes dates as local time with the offset the phone applied when
it wrote the export (`2026-01-10 08:00:00 +0100`); the import sends the exact instant in UTC.
That offset is the export's, not necessarily the zone a sample was recorded in, so it is not
kept. When HealthKit recorded `HKTimeZone`, it stays in the metadata and decides the local day,
as for an upload from an app. Many samples carry none: the server then counts their local day
in UTC. Pass `--tz Europe/Paris` to assume a zone for them; it is written as their `HKTimeZone`.
A part's `X-OHS-Day` is the sample's local day in that zone, or the day as the export wrote it.

**Parts and batches.** Lines are grouped into one part per data type and local day, at most
8 MiB uncompressed. A workout with a route is a part of its own, so the server attaches the
points to it; so is each ECG. The batch id is derived from the export's `ExportDate`, the size
of `export.xml` and the options (`ahx-20260715T070000Z-1a2b3c4d5e6f`), and the conversion is
deterministic: running the same import again sends the same bytes under the same keys, and the
server answers each part `duplicate: true`. So an interrupted import is resumed by running it
again. Other options give another batch; the rows still are not stored twice, since records are
de-duplicated by content. A 409 means other bytes already sit under a key of this batch: pass a
new `--batch`.

429 and 5xx answers and network errors are retried with backoff (honouring `Retry-After`), up
to 6 tries per part. 401 and 403 stop the import at once: check the token and its `upload` scope.

**Limits.**

- Heartbeat beats carry only a time of day in the export, in the phone's 12 or 24 hour format;
  a series whose times do not parse is counted as unreadable.
- ECG files are read with their English header (`Recorded Date`, `Classification`,
  `Sample Rate`). In another language the recording date is still found; the classification
  becomes 100 (unrecognized) and the sampling frequency defaults to 512 Hz.
- The export lists the members of a correlation both inside it and as top-level records, as
  HealthKit queries return them to an app. Blood pressure members are stored once (same content
  hash); a food correlation also makes its own nutrition log, as an app upload would.
- A route longer than one part (about 40,000 points) continues in the next part; those points
  are attached by time once the workout is stored.

## Processing notes

- Content is de-duplicated by hash, so the same reading uploaded twice is stored once.
- A running total HealthKit re-sends with a newer value (a day's step count) replaces the stored
  one.
- Daily step, energy, distance and flight totals are derived per local day from samples. When an
  iPhone and an Apple Watch both report, the larger source total wins instead of being added.
- A value outside its hard plausible range rejects the record; outside the soft range it is
  stored and flagged in its metadata.
- Dietary values, single samples and food entries alike, are converted to the unit of their
  nutrient ([the nutrient table](../api.md#nutrition-logs)): a mass in kilograms, grams,
  milligrams or micrograms, a volume in litres, millilitres or fluid ounces, an energy in
  kilocalories (HealthKit's `Cal`) or kilojoules. A value in a unit that does not convert to its
  nutrient's (a mass for water, the small calorie `cal`) rejects the record.
