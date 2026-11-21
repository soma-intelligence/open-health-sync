# Import FIT, GPX and TCX activity files into PostgreSQL

`ohs import files` loads FIT, GPX and TCX files (one file, a folder or a whole account export
zip) into the PostgreSQL database of an Open Health Sync server you run. Each activity becomes a
workout row with its GPS route and per-second heart rate and power, from any device or app that
saves these files: Garmin, COROS, Wahoo, Zwift, Suunto, Polar, Strava archives and more. No vendor
API, partner program or operator keys are involved.

## 1. Get your files

| Source | Where the files come from | What to take |
|---|---|---|
| Garmin | account export at garmin.com/account/datamanagement, **Request Data Export** | the whole zip; step by step in [Export Garmin Connect activities](garmin-export.md) |
| COROS | [Training Hub](https://t.coros.com), **Activity List**, **Export Data**, sent by email | every activity as FIT or TCX |
| Wahoo | the Wahoo app, **History**, open a workout, share its `.fit` file | one file per workout; no account export was found |
| Zwift | `~/Documents/Zwift/Activities` on macOS, `C:\Users\<user>\AppData\Local\Zwift\Activities` on Windows, or **Download FIT file** on zwift.com | the folder |
| Suunto | the Suunto app, a workout, the three dots menu, export as FIT or GPX | one file at a time |
| Polar | Polar Flow on the web, **Diary**, a session, **Export session** | TCX, which carries the heart rate |
| Strava | **Settings**, **My Account**, **Download or Delete Your Account** | the archive; its `activities/` folder holds the original files (`.gz` files are read as they are) |

Vendors move their menus; the [activity files guide](../connectors/activity-files.md#1-get-the-files)
has the links and marks the steps that were not checked against the vendor's own docs.

## 2. Start the server and get an upload token

Follow the [quickstart](../../README.md#quickstart) to *Start the stack*, then create your user:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" -d '{"scopes":["upload"]}' | jq -r .token)
```

Install the `ohs` command from the
[latest release](https://github.com/soma-intelligence/open-health-sync/releases) and put it on
your `PATH`. The token can upload and nothing else.

## 3. Import

```sh
ohs import files ~/Downloads/coros-export/ --dry-run   # parses and counts, sends nothing
ohs import files ~/Downloads/coros-export/
ohs import files ~/Downloads/strava-archive/
ohs import files ~/Documents/Zwift/Activities
ohs import files ~/Downloads/morning-ride.fit
```

It takes a file, a folder (walked in full) or a zip (with zips inside it), and picks the `.fit`,
`.gpx` and `.tcx` files and their `.gz` versions. Each file is parsed first, so one the server
would refuse is counted and skipped.

| Flag | Effect |
|---|---|
| `--dry-run` | parse and count, upload nothing |
| `--since 2026-01-01` | keep activities that start on or after that UTC day |
| `--concurrency N` | uploads in flight (default 4) |

Each file goes up once, under a batch named after its SHA-256, so running the command again, or
importing a Strava archive that holds the same files as a Garmin export, sends nothing new. The
output has counts and format names only, never a file name or a value.

## 4. Query in SQL

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

```sql
SELECT start_date, source_slug, activity_type, duration_s / 60 AS minutes,
       round((distance_m / 1000)::numeric, 2) AS km, avg_heart_rate
FROM ohs_data.workouts
WHERE user_id = :'user' AND provider = 'activity_files'
ORDER BY start_date DESC
LIMIT 10;
```

Routes are in `ohs_data.route_points`, and per-second `heart_rate`, `cycling_power` and
`running_power` in `ohs_data.quantity_samples`. The source slug names the maker from the file
(`garmin`, `coros`, `wahoo`, `zwift`, `strava`...). The [Garmin guide](garmin-export.md#4-query-it-with-sql)
has more queries.

## What each format gives

| | FIT | TCX | GPX |
|---|---|---|---|
| Workouts | one per `session` (one per leg in multisport) | one per `Activity` | one over all timed points |
| Route | every `record` with a position | every `Trackpoint` with a `Position` | every timed `trkpt` |
| Heart rate, power | yes | `HeartRateBpm`, `Watts` | from TrackPointExtension and point extensions |
| Elevation gain, local time zone | yes | no | no |

A Zwift ride or other virtual activity keeps heart rate and power but no route. Laps, swim
lengths, beat-to-beat intervals, developer fields and running dynamics stay in the stored file
and are not mapped. The same activity from two sources (a file and Strava, say) is stored under
each; the merged view shows one, the file's copy first by default
([provenance](../connectors/activity-files.md#8-provenance-and-other-sources-of-the-same-activity)).
`DELETE /v1/users/{id}/data/activity_files` erases everything imported from files.

## From a watch over USB

A Garmin or other FIT watch mounted over USB can also upload through the desktop bridge, without an export:
`ohs-bridge files --watch /Volumes/GARMIN/Garmin/Activity` sends each new file once, with the
same batch naming, so a later export import adds no duplicate ([bridge](../bridge.md)).

## Related

- [Export Garmin Connect activities to PostgreSQL or CSV](garmin-export.md)
- [Record a Bluetooth heart rate strap, blood pressure cuff or scale without a phone](bluetooth-devices.md)
- [Import Apple Health data into PostgreSQL](apple-health-to-postgres.md)
- [Activity files connector](../connectors/activity-files.md)
- [README](../../README.md)
