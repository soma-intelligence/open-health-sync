# Spec: synthetic data for every provider

Status: built 2026-10-22 on branch `test/synthetic-data`. Code: `internal/synth` (generator and
renderers), `internal/synth/synthtest` (the pipeline harness). Plan:
[plans/synthetic-data.md](../plans/synthetic-data.md).

## Problem

The connectors were tested with hand-written fixtures: a few lines per data type, written by the
person who wrote the mapper, so they share the mapper's assumptions. Nothing checked a connector
against what the vendor documents it may send: every documented field, unit, enum value and time
format, the variants the vendor's own examples show, and the faults a real feed carries
(duplicates, nulls, out of range values, malformed JSON, unknown fields from a newer API
version). Nothing exercised time zones across DST and travel, several devices measuring the same
minutes, or a year of second-level heart rate.

Real data cannot fill the gap: it is health data, it cannot be committed, and one account covers
one person's devices.

## What it is

A deterministic generator in Go, in two steps:

1. **Timeline.** `synth.Build(opts)` lives an invented person for a number of days: the time zone
   of each day, sleep with stages, heart rate, steps, workouts with routes, weigh-ins, blood
   pressure, glucose, ECG strips, meals, drinks, menstrual cycles. Every signal is a function of
   time from seeded noise, so the same seed gives the same bytes on every machine.
2. **Renderers.** One package per provider (`internal/synth/<provider>`) writes the timeline in
   that provider's wire format, exactly as the connector stores it: the API's JSON items, an
   upload line, a file. `Lines(tl)` returns the lines, each tagged with the fault it carries on
   purpose (none for a documented line) and what a scenario did to it.

`synthtest.Suite` runs the lines of every scenario and persona through the steps of the ETL
worker: one NDJSON object per data type read by `etl.Reader`, `Map` (a panic is caught),
`model.CanonicalizeSourceAttribution`, `model.CanonicalizeUnits`, `etl.ValidateMapResultAt`.
Each line ends as `parse_error`, `panic`, `map_error`, `empty`, `unit_error`, `rejected` or
`stored`.

## Rules

- **No real data.** Fixtures are fully synthetic: invented names (`Sam Example`), invented ids,
  round persona figures from public physiology. A real Apple Health export was read once for
  aggregate shapes only (which record types, units and sources occur, how large files get); no
  value, date, name or identifier from it is in the repository.
- **Schemas from the vendor.** A renderer is written from the vendor's own published
  documentation, never from the connector's code: an OpenAPI document, a reference page, a
  schema file, the examples in them. Where the vendor's pages disagree with themselves (Omron
  types systolic as a string and writes it as a number), the renderer emits every variant they
  show. The only things read from this repository are its own contracts: the upload envelopes
  of the device routes (`docs/connectors/*.md`) and the self report API.

## Sources, fetched 2026-10-22

| Provider | Renderer | Source |
|---|---|---|
| Apple Health | `applehealth`: SDK upload lines and `export.zip` (export.xml with its DTD, workout routes as GPX, ECG CSV, clinical JSON) | HealthKit reference (developer.apple.com/documentation/healthkit): quantity, category and correlation identifiers, HKUnit strings, workout activity types, sleep values, metadata keys |
| Oura | `oura` | OpenAPI 1.42 (api.ouraring.com/v2/static/json/openapi-1.42.json) |
| WHOOP | `whoop` | OpenAPI 3.0.1 of developer.whoop.com/api and its user data pages |
| Polar | `polar` | AccessLink v3 swagger.yaml (www.polar.com/accesslink-api) |
| Withings | `withings` | openapi.yaml of developer.withings.com and the all-available-health-data guide |
| Strava | `strava` | swagger.json of developers.strava.com and its model files |
| Fitbit (Google Health API) | `fitbit` | Google Health API v4 discovery document, revision 20261020 |
| Dexcom | `dexcom` | swaggerV3.json of developer.dexcom.com (EGV schema, egvs_v3 example) |
| intervals.icu | `intervalsicu` | OpenAPI 3.0.1 at intervals.icu/api/v1/docs |
| Suunto | `suunto` | API Zone (apizone.suunto.com): operations, examples and FAQ |
| Omron | `omron` | Omron Partner API for Europe and US (omronhealthcare-ohi.atlassian.net, space API) |
| Health Connect (with Xiaomi, Zepp, RingConn, Samsung Health, Oura, Withings, OMRON connect and FreeStyle Libre as writing apps) | `healthconnect` | androidx.health.connect.client reference: record classes, metadata classes, enum constants |
| FIT, GPX, TCX files | `activityfiles` | FIT Protocol and FIT Profile 21.218 (developer.garmin.com/fit, fit-sdk-tools release), GPX 1.1 schema (topografix.com), Training Center Database v2 and TrackPointExtension v2 schemas |
| Bluetooth devices | `bluetooth` | GATT Specification Supplement and its YAML structures (bitbucket.org/bluetooth-SIG/public, gss/) |
| Self reports | `selfreport` | This project's API, [self-reports.md](../self-reports.md) |

Every page was saved with its URL, date and SHA-256, so a renderer can be checked against the
exact text it was written from.

### Not rendered, and why

| Connector | Reason |
|---|---|
| `garmin` | The Health API push schemas (dailies, epochs, sleeps...) are only shown to approved partners. The public Personal API document describes other endpoints |
| `ultrahuman` | The public page lists metric names and units but not the answer's shape; the full reference is behind a login |
| `eightsleep` | Eight Sleep publishes no API documentation |
| `xiaomi` (Mi Fitness export) | Xiaomi documents how to request the export, not its files. Xiaomi bands still reach the tests through Health Connect, as Mi Fitness writes there |

## Scenarios

Timeline scenarios change what the person lives; document scenarios change how the provider
writes it.

| Scenario | What it puts in the data |
|---|---|
| `baseline` | A clean dataset at home, one device per kind |
| `dst` | Both DST changes of the home zone, nights across them, a 02:30 on the skipped hour |
| `travel` | Flights through +05:30, +05:45, +12:45, -10:00 and +14:00, across the date line |
| `multi_device` | Watch, phone, ring and strap measuring the same minutes; the same workout from two services |
| `gaps` | Days without data, a device off for hours, empty answers |
| `extremes` | Valid but rare: HR 35 and 205, a 20 hour ultra, a 13 hour night, glucose 45 and 400 |
| `huge` | One sample a second where devices allow it |
| `duplicates` | Exact copies, re-sent revisions with a corrected value, deletions where the format has them |
| `missing` | Optional fields absent or null, required fields absent |
| `out_of_range` | Negative counts, HR 0 and 10 times the range, SFLOAT NaN, end before start |
| `unit_variants` | Every unit the vendor documents (lb, mmol/L, kPa, degF, in) and unknown ones |
| `malformed` | Truncated JSON or files, BOM, invalid UTF-8, NaN, deep nesting, a wrong CRC, data before its definition |
| `unknown` | Unknown fields, enum values, data types and messages, as a newer API would send |
| `timestamps` | Every documented time format, local times, epochs, 1970, 2099, garbage |
| `all` | Every scenario except `huge` |

The first document of each type gets every mutation; then one document in eight gets the next
three of a per type cycle, so a run stays small and still covers every mutation of every type.

Personas: `active`, `sedentary`, `night_shift`, `diabetic` (wears a CGM), `hypertensive` (blood
pressure twice a day), `cycle` (tracks a menstrual cycle).

## Invariants checked

- No line panics a mapper.
- A documented line (no fault) is never refused: no map error, no unit error, no rejection.
- A malformed line is never stored.
- Every record that would be stored has finite numbers, no time more than 48 hours after the
  generation's "now", and no end before its start (samples, sleep, workouts, observations).
- The same seed gives the same bytes; another seed gives other bytes.
- The baseline of every provider stores something.

## Running it

- `go test ./...` (the CI run) runs the light version: five days, the timeline scenarios one by
  one, the document scenarios through `all`, the six personas over four days. About 50 seconds
  for every renderer on a 4 core machine.
- `OHS_SYNTH_FULL=1 go test ./internal/synth/...` runs fourteen days, every scenario on its own
  and `huge`. The `synthetic-data` workflow runs it nightly.

## Through the whole server

`test/synthflow` sends the same data to a running server on a fresh TimescaleDB: the device
providers through their upload routes, the cloud providers the way a pull stores them (raw
object, `raw_objects` row, ETL job). Every line with a fault is a part of its own, so the
server's own logs (`sync_log`, `failed_records`) say what it did with that line, which is
compared with the harness's prediction. Then it checks every stored row in the database (no
NaN, nothing more than 48 hours ahead, no end before its start, no job left parked), reads
each quantity type back through the REST API and calls the sleep, workout, FHIR, Open mHealth
and export routes. About twenty minutes: `OHS_SYNTH_E2E=1 go test -tags integration
./test/synthflow/`, run nightly by the `synthetic-data` workflow, with
`OHS_SYNTH_REPORT=path` for the anomaly report.

The first run (seed 11, three days, scenario `all`, about 264 000 lines) agreed with the
harness on every line once bug 7 was fixed, and found what the harness cannot see:

- Bug 7 below.
- The sample export of the merged view (the default) was about a thousand times slower than
  `view=all` on second-level data: for the activity files account (three days of FIT files,
  249 355 samples) `view=all` streamed everything in 2 seconds, the merged export had written
  99 990 rows after 15 minutes (about 110 rows a second), as each record ran its own lookup
  over the streams around it. The export and the archive now decide the merged view in one
  pass (`read.EachSample`): the same account's 115 297 merged samples in 2.2 seconds instead
  of 19 minutes, and the Apple Health account's 57 575 in 0.8 seconds instead of 10 minutes 42,
  identical record for record.
- Epoch dates reach the database on six providers (Apple Health, Bluetooth, Polar,
  Withings, Dexcom, Omron), as the harness predicted.
- Fitbit: a data point re-sent without its optional `dataSource` is stored a second time, as
  the hash keys on the source. Google Health API points mostly have no `name` to dedupe on.

## Bugs it found

Fixed on the branch, each with a unit test:

1. Sleep phases, ECG recordings, daily summaries and observations were never validated.
2. An end before the start, or far in the future, was stored for quantity, category, nutrition
   and workout records.
3. HealthKit glucose in mmol/L (`mmol<180.15588000005408>/L`) was refused: every reading from a
   mmol/L region was lost. `yd`, `in`, `s` (running metrics), `oz` and `lb` (nutrition) were
   refused too.
4. Workout heart rate, energy, distance, speed, climb and VO2 max were never range-checked: a
   workout with an average heart rate of 2500 or a negative distance was stored.
5. Nutrition logs with negative energy, macros or nutrients (water from a Health Connect
   hydration record of -100 L) were stored.
6. Health Connect: the constant-name spelling `manual_entry` of `RECORDING_METHOD_MANUAL_ENTRY`
   did not set `was_user_entered`.
7. A NUL character in any string (a device name, a metadata value), which PostgreSQL text and
   jsonb refuse, failed the whole write: the job retried until it was parked and every line of
   the object was lost, a whole day of an upload for one bad character. Found only through the
   server run below (1051 parked jobs for Apple Health, 26 for Health Connect, others for Oura,
   intervals.icu and Omron). The ETL now removes NUL and invalid UTF-8 from every string of a
   mapped record and from the payload of a failed record (`model.ScrubText`).

Seen and left as they are, for a decision:

- Timestamps at the Unix or FIT epoch (1970, 1989) were stored, as no rule bounded the past.
  Decided since: every record kind refuses a time before 1990-01-01 UTC.
- A blood glucose of 0 mg/dL is stored (the hard band starts at 0, inclusive), as is an HRV of
  0 ms.
- A body mass of 3000 kg is stored with a suspect flag only (the hard band has no ceiling).
- About 25 HealthKit types are skipped as unknown by the Apple Health connector, among them
  `AppleWalkingSteadiness`, `SixMinuteWalkTestDistance`, `AppleSleepingBreathingDisturbances`,
  `HeartRateVariabilityRMSSD`, `TimeInDaylight`, `WorkoutEffortScore`,
  `AtrialFibrillationBurden`, `HeartRateRecoveryOneMinute`, `PeripheralPerfusionIndex`, the
  running and stair speeds, the paddle, skating and cross-country ski distances and
  `HeadphoneAudioExposureEvent`.
- Polar does not enumerate its sport names (its example only shows `OTHER`), Oura's workout
  activity is a free string and Suunto does not publish its activity ids: those renderers use a
  small set and the connector maps the rest as other.
