# Activity files (FIT, GPX, TCX)

Slug `activity_files`. Sports watches, bike computers and training apps record each workout as
a file: FIT, the binary format Garmin, COROS, Wahoo, Suunto, Polar, Zwift, Hammerhead and Bryton
devices write, or the XML formats GPX and TCX. Every one of these vendors lets a person download
their own files, one at a time or as an account export. Open Health Sync imports those files:
no vendor API, no partner program, no operator keys.

| Path | What it brings | How |
|---|---|---|
| Activity files | Workouts with their GPS route, per-second heart rate, cycling and running power | The user downloads files or an account export, and `ohs import files` uploads each file to `POST /v1/ingest/activity-files` |

A file carries the full streams of the recording, which the cloud APIs of
[Strava](strava.md), [Suunto](suunto.md) and [intervals.icu](intervals-icu.md) do not give
(summary figures only). The [Garmin API](garmin.md) is a partner program that has not taken new
requests since spring 2026; this path does not use it.

## 1. Get the files

Checked on 2026-10-14. Vendors move their menus; a step marked *to confirm* comes from a
third-party description and was not checked against the vendor's own documentation.

**Garmin.** The account export holds every activity ever synced, as the original FIT files:

1. sign in at [garmin.com/account/datamanagement](https://www.garmin.com/account/datamanagement/)
   and open **Export Your Data**;
2. choose **Request Data Export**;
3. Garmin emails a download link, usually within 48 hours, sometimes up to 30 days later.

The zip holds the activity files in further zips under `DI_CONNECT/DI-Connect-Uploaded-Files/`
(*to confirm*); pass the whole zip to `ohs import files`, which opens the inner zips itself. The
JSON files next to them (sleep, stress, daily summaries) are not read. One activity: in Garmin
Connect on the web, open it, then the gear menu, **Export Original** (a zip holding the FIT file,
*to confirm*).

**COROS.** In the [Training Hub](https://t.coros.com), **Activity List**, **Export Data**: every
activity as FIT or TCX, sent by email
([COROS support](https://support.coros.com/hc/en-us/articles/33125636125204-Bulk-Export-Historical-Activity-Data)).

**Wahoo.** In the Wahoo app (ELEMNT, KICKR and RIDE), **History**, open a workout, then share
its `.fit` file (*to confirm*; [Wahoo support](https://support.wahoofitness.com/hc/en-us/articles/22710510686098)).
No account export was found.

**Zwift.** Each ride is a FIT file on the computer that ran Zwift: `~/Documents/Zwift/Activities`
on macOS, `C:\Users\<user>\AppData\Local\Zwift\Activities` on Windows since February 2026
(`Documents\Zwift\Activities` before). From the web: zwift.com, **Feed**, **Just me**, open an
activity, the gear menu, **Download FIT file**
([Zwift support](https://support.zwift.com/en_us/locating-your-activity-log-and-workout-files-BykKO63fr.md)).
Zwift rides are virtual: their workout keeps no route.

**Suunto.** In the Suunto app, open a workout, the three dots menu, export as FIT or GPX, one
at a time ([Suunto help](https://us.suunto.com/pages/what-type-of-files-can-i-export-from-the-suunto-app)).
No bulk export was found.

**Polar.** In Polar Flow on the web, **Diary**, open a training session, **Export session**: TCX,
GPX, CSV or a zip of them ([Polar support](https://support.polar.com/export-training-sessions-flow)).
Take TCX: it carries the heart rate. The account data download at account.polar.com is JSON only
and is not read.

**Strava.** **Settings**, **My Account**, **Download or Delete Your Account**, request the
archive ([Strava support](https://support.strava.com/en-us/articles/15401919-how-do-i-export-my-strava-data)).
The archive keeps the original file of each activity under `activities/`, some gzip
compressed (*to confirm*); `ohs import files` reads the `.gz` files as they are. The CSV files
are not read.

Other devices and apps that save FIT, GPX or TCX files work the same way.

## 2. Import them

```sh
export OHS_URL=https://ohs.example.org
export OHS_TOKEN=ohs_ut_...        # a user token with the upload scope
ohs import files ~/Downloads/garmin-export.zip
ohs import files ~/Downloads/strava-archive/
ohs import files ~/Downloads/morning-ride.fit
```

The command takes one file, a folder (walked in full) or a zip (zips inside it included), and
picks the `.fit`, `.gpx` and `.tcx` files and their `.gz` versions. It parses each file before
sending it, so a file the server would refuse is counted and skipped. `--dry-run` parses and
counts without uploading; `--since 2026-01-01` keeps activities that start on or after that UTC
day; `--concurrency` sets the uploads in flight (default 4). `--tz` and `--batch` do not apply.

Each file is uploaded once. Its batch is `af1-` and the first 32 hex digits of its SHA-256, so
running the command again, or importing another export that holds the same file (a Garmin export
and a Strava archive often do), sends parts the server answers as duplicates. The command holds
one file per upload slot in memory whatever the size of the export; a zip inside a zip is copied
to a temporary file, removed once read.

The command prints counts and format names only, never a file name (a Garmin export names its
files after the account's email address), a value or the token. A file the server refuses is
counted under its reason and the run goes on; the command then exits with an error.

## 3. Auth flow

No OAuth and no operator keys: the connector is always on. The user gets the files from their own
account and uploads them with a user token (`ohs_ut_`) that has the `upload` scope, like an Apple
Health export. The server never holds vendor credentials.

## 4. Rate limits

Nothing is pulled from a vendor. Uploads share the instance's limit of uploads in flight with the
other device routes (`503` with `Retry-After` above it); `ohs import files` retries those, `429`
and `5xx` with backoff.

## 5. Backfill and updates

An account export is the full history. For later activities, import new files (or a new export:
files already stored are skipped by the server). An activity edited on the vendor's side and
downloaded again keeps the first stored copy of the workout (see section 8).

## 6. Upload contract

`POST /v1/ingest/activity-files` works like the [Apple Health upload](apple-health.md): a user
token with the `upload` scope, the headers `X-OHS-Type`, `X-OHS-Day`, `X-OHS-Batch` and
`X-OHS-Part`, and a gzip NDJSON body of at most 64 MiB. A part holds one file, on one line:

```json
{"type":"fit","data":"DhAIDdQBAAAuRklUeR..."}
```

- `type`: `fit`, `gpx` or `tcx`, equal to `X-OHS-Type`.
- `data`: the file, standard base64, at most 20 MiB once decoded.

The line carries no file name. The raw store keeps the part as sent, so the original file stays
available byte for byte. `ohs import files` sends `X-OHS-Day` as the UTC day the activity starts
and `X-OHS-Part` 0.

The part is refused with 400, and nothing is stored, when it holds more or less than one line,
the line is not as above, or the file does not parse as an activity: a FIT file with a broken
CRC, a FIT course, settings or monitoring file, a GPX route or track without times, a TCX file
without an `Activity`, an activity that starts more than 48 hours from now. The message names
the problem, never the file's content.

## 7. Mapping

A FIT file gives one workout per `session` message (a multisport file gives one per leg), a TCX
file one per `Activity`, a GPX file one over all its timed track points.

| Canonical record | FIT | TCX | GPX |
|---|---|---|---|
| Workout activity type | `sport` and `sub_sport` (`cycling`, `running`, `swimming`...; a `training` session by its sub sport: `strength_training` is `traditional_strength_training`) | `Sport` (`Running`, `Biking`, `Other`) | track `type` |
| Start, end | `start_time`; end is start plus `total_elapsed_time` | `Id` or first lap start; the later of the last lap end and the last track point | first and last timed point |
| Duration | `total_timer_time` (pauses excluded) | sum of lap `TotalTimeSeconds` | elapsed time |
| Distance, energy | `total_distance` (m), `total_calories` (kcal) | sum of lap `DistanceMeters`, `Calories` | distance along the fixes |
| Average, maximum heart rate | `avg_heart_rate`, `max_heart_rate` | lap figures, weighted by lap time | from the points |
| Average speed | `enhanced_avg_speed` | distance over duration | distance over duration |
| Elevation gain | `total_ascent` (m) | none | none |
| `RoutePoint` | every `record` with a position: latitude, longitude, altitude, speed | every `Trackpoint` with a `Position` | every `trkpt` with a time |
| `heart_rate` samples (bpm) | `record.heart_rate` | `HeartRateBpm` | `hr` of the TrackPointExtension (v1, v2) |
| `cycling_power`, `running_power` samples (W) | `record.power`, by the session's sport | `Watts` of ActivityExtension v2 | `power` in the point's extensions |

- Units are the canonical storage units, UCUM in FHIR: `bpm` (UCUM `/min`), `W`, `m`, `m/s`,
  `kcal`; coordinates in decimal degrees.
- Samples are instants at the file's own rate (one per second on most devices). A heart rate
  outside 20 to 300 bpm, a cycling power above 5000 W or a running power above 3000 W is a sensor
  placeholder and is left out, and so is a second reading at the same time. Power of other sports
  (rowing, skiing) has no canonical quantity and is not stored as samples.
- A virtual activity (FIT sub sport `virtual_activity`: Zwift, other indoor apps) keeps no route,
  since its coordinates are a game map. Heart rate and power are kept.
- Workout metadata: `file_format`, `file_sha256`, `fit_sport` and `fit_sub_sport` (or `tcx_sport`,
  `gpx_type`), `indoor`, `virtual`, `device_name`, `elapsed_time_s`, `moving_time_s`,
  `total_descent_m`, `max_speed_mps`, `avg_power_w`, `max_power_w`, `normalized_power_w`,
  `avg_cadence` and `max_cadence` (rpm, as the file states them), `session_index` and `sessions`
  for a multisport file. Cadence has no canonical quantity: it is kept here only.
- Time zone: a FIT `activity` message's `local_timestamp` gives the UTC offset, kept as
  `utc_offset_minutes` and, for a whole hour, as an `Etc/GMT` zone in `Metadata["timezone"]`
  (workouts and samples), which files the records under their local day. GPX and TCX times carry
  no zone; their records fall on UTC days.

Not mapped, kept in the stored file: laps, swim lengths, FIT `hrv` messages (beat-to-beat
intervals), developer fields (such as running power written by a Connect IQ app), per-second
cadence, temperature, running dynamics, elevation gain from a GPX or TCX track.

## 8. Provenance, and other sources of the same activity

Every record has provider `activity_files`. The source slug names who recorded the file:

- FIT: the `file_id` manufacturer, by its FIT name, with `wahoo_fitness` as `wahoo`,
  `polar_electro` as `polar` and `stages_cycling` as `stages` (`garmin`, `coros`, `suunto`,
  `zwift`, `strava`, `hammerhead`, `bryton`...). The device (`DeviceID`) is the Garmin product name
  for a Garmin device (`Garmin edge_530`), else the product name the file carries
  (`Wahoo ELEMNT BOLT`).
- GPX and TCX: the vendor named by the TCX device (`Creator`), the TCX author or the GPX `creator`
  (`Garmin Connect`, `Polar Vantage V3`, `StravaGPX`). The TCX device name is the device.
- A file that names no known vendor (a FIT `development` manufacturer, an unknown creator) has
  the source slug `activity_files`.

Workouts carry the source slug and the device (`workouts.source_slug`, `device_id`), as samples
do. FHIR output carries them as [Provenance](../fhir.md).

Duplicates:

- **The same file twice**: the same part, answered `duplicate: true`; nothing is processed again.
- **The same activity in two files** (a FIT and a GPX of one ride, or a file the device saved
  again): a workout is identified by user, provider, source slug, device, file format and start
  second, and the first stored copy wins. A FIT and a GPX of one activity give two workouts of the
  same source, each with its own route; their heart rate and power samples, identified by source,
  type, time and value, are stored once.
- **The same activity from another provider** (Strava, intervals.icu, Apple Health, Health
  Connect) is stored under each provider. The merged views (`view=merged`, the default) show one:
  two workouts that share at least half of the shorter one are the same session, and the better
  ranked source stays. The default workouts order is `polar, garmin, coros, wahoo, suunto, zwift,
  strava, apple_health, whoop, oura, withings`: a file's copy, which has the streams, wins over
  a Strava or Apple Health copy by default. A user who prefers another order sets it:

  ```sh
  curl -X PUT "$OHS_URL/v1/users/$USER_ID/source-priorities" \
    -H "Authorization: Bearer $OHS_API_KEY" -H "Content-Type: application/json" \
    -d '{"priorities":{"workouts":["strava","garmin"]}}'
  ```

  Heart rate follows the heart order the same way (`apple_health` first by default): a file's
  reading within 5 minutes of a better ranked one is left out of the merged view, never deleted.

`DELETE /v1/users/{id}/data/activity_files` erases everything imported from files. There is no
route to delete one imported activity.

## 9. Testing

`connectors/activityfiles/testdata` holds invented files: FIT files written by the FIT
library's encoder from `fixtures_test.go` (a Garmin road ride, a COROS trail run with running
power and a UTC+5:30 offset, a Wahoo indoor ride, a Zwift virtual ride, a Garmin multisport file,
a file without sessions, a monitoring file, a file with a broken CRC), and hand-written GPX and
TCX files (a Strava TCX starting with spaces, as Strava writes them). Regenerate the FIT files with
`go test ./connectors/activityfiles -run TestFITFixtures -update`. `FuzzInspect` feeds the
parsers random input (`go test ./connectors/activityfiles -fuzz FuzzInspect`).

If a real file reads wrong, open an issue with the vendor, the device, the format and what was
wrong (the activity type, a missing stream), never the file itself: it holds your positions and
heart rate.
