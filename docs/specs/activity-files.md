# Spec: activity files (FIT, GPX, TCX)

Status: shipped in v0.2.0 (built 2026-10-14). Guide:
[connectors/activity-files.md](../connectors/activity-files.md).

## Problem

Most sports watches and bike computers (Garmin, COROS, Wahoo, Suunto, Polar, Bryton,
Hammerhead) and training apps (Zwift, Strava) record a workout as a file in an open format:
FIT, the binary format the devices write, or the XML formats GPX and TCX. Their cloud APIs are
partner programs: Garmin's is for approved businesses and has not reviewed new requests since
spring 2026 ([research notes](../connectors/garmin.md)); COROS and Suunto admit companies only.
Today a workout of these devices reaches Open Health Sync only when another service re-hosts it
(Strava, as summary figures; intervals.icu; Apple Health or Health Connect, when the vendor app
writes there), and never with its full heart rate, power and GPS streams.

Every one of these vendors lets a person download their own activity files, one by one or as an
account export. Importing those files needs no partner program, no operator keys and no vendor
API call.

## Integration paths, as checked on 2026-10-14

| Vendor | API | Files a person can download |
|---|---|---|
| Garmin | Connect Developer Program: businesses only, new requests paused since spring 2026. The API connector stays on hold and is not touched here | Account export: garmin.com/account/datamanagement, Export Your Data. The zip holds the original FIT files in further zips under `DI_CONNECT/DI-Connect-Uploaded-Files/`. One activity: Export Original in Garmin Connect on the web |
| COROS | Partner API, by request to COROS | Training Hub, Activity List, Export Data: every activity as FIT or TCX, sent by email (COROS support article) |
| Wahoo | Not evaluated here | Wahoo app, History, a workout, share the `.fit` file (third-party description, to confirm). No account export found |
| Zwift | No public API | The FIT file of each ride on the computer running Zwift, and Download FIT File on zwift.com (Zwift support article) |
| Suunto | Partner API (the `suunto` connector, summary figures only) | Suunto app, a workout, export as FIT or GPX, one at a time (Suunto help page). No bulk export found |
| Polar | AccessLink (the `polar` connector, last 30 days) | Polar Flow web, Diary, a session, Export session: TCX, GPX, CSV or a zip of them (Polar support). The account data download is JSON only |
| Strava | API v3 (the `strava` connector, summary figures only) | Settings, My Account, Download your account (Strava help). The archive keeps each activity's original file in `activities/`, some gzip compressed (third-party descriptions, to confirm) |

Chosen: a device connector that takes the files themselves, and an importer for single files,
folders and these exports. Rejected: parsing each vendor's own JSON (Garmin wellness files,
Polar sessions, Strava CSV); the formats are undocumented and we have no real sample to test
against.

## Scope

1. A device connector, slug `activity_files`, route `POST /v1/ingest/activity-files`. The slug
   names the data rather than the transport: `files` alone would also fit a later import of
   other files (a weight CSV), and the slug is visible in every row, in source priorities and in
   `DELETE /v1/users/{id}/data/{provider}`.
2. One part holds one file. The body is the usual gzip NDJSON part with one line:
   `{"type":"fit","data":"<base64 of the file>"}`, `type` being `fit`, `gpx` or `tcx` and equal
   to `X-OHS-Type`. Why a line and not the bare file: the raw store key, the ETL reader, the
   drainer, the requeue recipe and the user archive all handle NDJSON objects; wrapping the
   file keeps the core untouched and the original file byte for byte in the raw store. The
   file is at most 20 MiB once decoded (its base64 line stays under the ETL's 32 MiB line
   cap). Same headers, limits, idempotency and answers as the other device routes. The upload
   is refused (400, nothing stored) unless the file parses as an activity.
3. Mapping, in `Map`, from the stored line:
   - one `Workout` per FIT session (a triathlon file gives one per leg), per TCX activity, per
     GPX file: activity type, start and end, timer duration, distance, energy, average and
     maximum heart rate, average speed, elevation gain where the file has them;
   - `RoutePoint` rows for every timed GPS fix, under the workout whose span holds it. A
     virtual activity (FIT sub sport `virtual_activity`, Zwift) keeps no route: its
     coordinates are a game map;
   - `heart_rate` (bpm) samples at the file's own rate (one per second on most devices);
   - `cycling_power` and `running_power` (W) samples, by the sport of the session, where the
     file records power. Cadence has no canonical quantity and stays in workout metadata
     (average and maximum); no new quantity type is added.
4. Provenance: provider `activity_files`. The source slug comes from the FIT `file_id`
   manufacturer (`garmin`, `coros`, `wahoo`, `suunto`, `polar`, `zwift`, `strava`, other
   manufacturers by their FIT name), or for GPX and TCX from the creator, author and device
   names when they name a known vendor; otherwise `activity_files`. The device is the FIT
   product (`Garmin edge_530`), the product name a non-Garmin file carries, or the TCX device
   name. Workouts gain `SourceSlug` and `DeviceID` in `internal/model`, and the ETL writer
   fills the existing `workouts.source_slug` and `device_id` columns with them (until now
   every workout's source was its provider). No migration.
5. `ohs import files <file|folder|zip>`: walks a folder or a zip, zips nested in zips included
   (the Garmin export), finds `.fit`, `.gpx` and `.tcx` files and their `.gz` versions,
   parses each, and uploads it with a user token. `--dry-run` counts, `--since` keeps
   activities that start on or after a UTC day. It holds one file at a time per upload slot
   (constant memory whatever the size of the export); a nested zip is copied to a temporary
   file, since a zip needs random access. Each file is its own batch,
   `af1-<first 32 hex of the SHA-256 of the file>`, part 0, day the UTC day of its start, so
   running it again, or importing another export holding the same file, uploads nothing twice
   (the server answers duplicate). File names are never printed or sent: a Garmin export names
   files after the account's email address.

## Deduplication, and other sources of the same activity

- The same file twice: the same bytes make the same part, answered `duplicate: true` whatever
  its batch or day, and nothing is queued again.
- The same activity in two files (a FIT and a GPX of it, or a file re-saved by the device):
  workouts are hashed on user, provider, source slug, device, file format and start second,
  and are not Replaceable: the first copy stored wins. Two formats of one activity give two
  workouts of the same source, because their route points (FIT semicircles against GPX decimal
  degrees) would not deduplicate under one workout. Heart rate and power samples are hashed on
  user, provider, source slug, type, time and value, so the copies of one reading collapse.
- The same activity from Strava, intervals.icu, Apple Health or Health Connect is stored under
  each provider. The merged view (package `fusion`) shows one: two workouts sharing at least
  half of the shorter one are the same session, and the better ranked source stays. The
  default workouts order becomes `polar, garmin, coros, wahoo, suunto, zwift, strava,
  apple_health, whoop, oura, withings`, so by default the file's copy (it has the streams) wins
  over a Strava or Apple Health copy. A user who prefers another order sets it with
  `PUT /v1/users/{id}/source-priorities`.
  intervals.icu workouts keep the source `intervals_icu` (their mapper keeps the upstream only
  in metadata); between two sources that no order lists, the tie is broken by provider name,
  which nobody should rely on. Heart rate follows the heart group order the same way
  (`apple_health` first): within 5 minutes of a better ranked reading, a file's reading is
  left out of the merged view, never deleted.

## Out of scope

- The non-activity content of vendor exports: Garmin wellness JSON (sleep, stress, daily
  summaries), Polar's JSON account download, Strava's CSV files, COROS and Suunto account data.
  The formats are undocumented and we have no real sample.
- FIT files that are not activities (monitoring, sleep, courses, workouts, settings): refused
  as such.
- Laps, swim lengths, FIT `hrv` messages (beat-to-beat intervals), developer fields (Stryd
  power written by a Connect IQ app, for one), per-second cadence, speed outside route points,
  temperature, running dynamics.
- Elevation gain computed from a GPX or TCX track (GPS altitude is too noisy to sum); the
  figure is kept only when the file states it (FIT).
- A time zone for GPX and TCX files, which carry none: their records fall on UTC days.
- KML, PWX, CSV activity files; password protected zips (unzip them first).
- Updating an activity already stored from an edited copy with the same start: first write
  wins. Deleting one imported activity: no route for it (the provider erasure removes all).
- Recordings under `test/recorded`: anonymizing a binary FIT file is its own project.

## Standards

- FIT protocol (Garmin FIT SDK profile 21.x, through the library below); GPX 1.1 with the
  Garmin TrackPointExtension (v1 and v2); TCX (Garmin Training Center Database v2) with
  ActivityExtension v2. Namespaces are matched by local name, so the prefixes files use
  (`gpxtpx:`, `ns3:`) do not matter.
- Units are the canonical storage units, UCUM in FHIR: `bpm` (UCUM `/min`), `W`, `m`, `m/s`,
  `kcal`; coordinates in decimal degrees (WGS 84).
- Activity types use the canonical vocabulary (the HealthKit workout activity names, as the
  Apple Health, Strava and intervals.icu connectors do).
- FIT times are seconds since 1989-12-31 UTC; the FIT `activity` message's `local_timestamp`
  gives the UTC offset, kept as `utc_offset_minutes` and, for a whole hour, as an `Etc/GMT`
  zone in `Metadata["timezone"]`.

## New dependency: github.com/muktihari/fit

Repository rule: standard library first, every dependency with a written reason.

- Why: FIT is a binary protocol with message definitions, compressed timestamps, component
  expansion (`speed` into `enhanced_speed`), developer fields, chained files and a CRC, all
  driven by a profile of several hundred messages that Garmin revises several times a year.
  A decoder of our own would be about 1,500 lines plus the generated profile, to maintain
  against those revisions. GPX and TCX are XML and use `encoding/xml`, streamed.
- Chosen: `github.com/muktihari/fit` v0.28.4 (tagged 2026-09-11), BSD-3-Clause, compatible
  with AGPL-3.0. Pure Go; the packages imported (`decoder`, `proto`, `profile/mesgdef`,
  `profile/typedef`, `profile/untyped`) import nothing outside the module. Its `encoder`
  builds the FIT test fixtures and is used by tests only. Releases are frequent (v0.28.x in
  2026) and it follows FIT profile 21.x.
- Rejected: `github.com/tormoder/fit` (MIT): last release v0.15.0 on 2023-10-01, an older
  profile, decodes into fixed structs only.
- Size: `make build` with the same version flags, from checkouts at paths of the same length,
  gave a `bin/ohs` of 57,572,345 bytes before this change and 58,764,304 bytes after:
  +1,191,959 bytes (+2.1 %), for the decoder, its profile tables and the new connector.
- Licence note: the module also ships Garmin's `Profile.xlsx` and SDK test files under Garmin's
  FIT SDK licence. They serve the library's code generation and tests, are not imported, and
  are not compiled into `ohs`. Reading and writing FIT files is use of the FIT protocol, which
  Garmin's FIT Protocol License grants free of charge.

## Requirements

- Auth flow (a user token with the `upload` scope, no operator keys), rate limits (no vendor
  call; the shared in-flight upload limit and the importer's retries), backfill (the export is
  the history; later activities by importing new files), mapping table and provenance: in the
  guide.
- Privacy: the importer prints counts and format names only, never a file name, a value or the
  token. The line carries no file name. The raw store keeps the original file, which may hold
  what the vendor wrote in it (a GPX author name, a device serial number); the mapper reads
  neither.
- Idempotency: as above; a re-import uploads nothing new.
- Tests use generated files with invented values: the FIT fixtures are written by the
  library's encoder from a test helper and committed, GPX and TCX are hand written.

## Open questions

1. The default workouts order now lists the device vendors that files bring (`garmin`,
   `coros`, `wahoo`, `suunto`, `zwift`) ahead of `strava`, since the original recording carries
   the streams and Strava's copy of it does not. To confirm in review.
2. Now that workouts carry a source slug, should intervals.icu write its upstream
   (`source_provider`, such as `garmin`) as the workout's source? The same activity from a
   file and from intervals.icu would then share a source.
3. The FIT licence position above, for the owner to confirm.
4. FIT `hrv` messages could feed `HeartbeatSample` (RR intervals), and developer field power
   (Stryd) `running_power`; both need real files to test against.
5. The layout of the Garmin export (inner zip names) and of the Strava archive comes from
   third-party descriptions; the importer does not depend on folder or file names.
