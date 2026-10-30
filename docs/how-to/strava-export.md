# Import a Strava archive into PostgreSQL or CSV

A Strava account archive includes the original FIT, GPX or TCX files of your
activities. Open Health Sync reads those files into PostgreSQL as workouts,
routes, heart rate and power samples. You can then query them with SQL or export
the workout table as CSV or Parquet. This path uses your downloaded files and
an instance running on your own computer.

## 1. Request your Strava archive

On the Strava website:

1. Open **Settings**, then **My Account**.
2. Under **Download your account**, choose **Get Started**, then **Request download**.
3. Wait for the email containing the download link, download the archive and unpack it.

The [Strava export instructions](https://support.strava.com/en-us/articles/15401919-how-do-i-export-my-strava-data)
describe the current menus and single-activity downloads. Keep any `.fit.gz`,
`.gpx.gz` or `.tcx.gz` files compressed: the import reads them directly. The CSV
and other account files are skipped; this guide imports activities.

Keep the archive private. Activity files can contain your locations and heart
rate; do not attach them to an issue or pull request.

## 2. Start PostgreSQL and Open Health Sync

You need Docker with Compose v2.24 or later, plus `curl`, `jq` and `openssl`.
From a fresh clone:

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
```

Create a personal user and save the instance URL, tenant key and user id:

```sh
(umask 077; set -C
 docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
   /ohs admin personal setup > ~/.ohs.env)
```

This refuses to overwrite an existing `~/.ohs.env`. If you already have that
file for this instance, reuse it. Otherwise, choose a different filename in
both the setup command and the next command. Keep the file private: its key
can read all data in the instance.

```sh
set -a; . ~/.ohs.env; set +a
```

## 3. Import the archive

Install the `ohs` command from the [latest release](https://github.com/soma-intelligence/open-health-sync/releases):
unpack the archive for your system (`darwin` is macOS) and put `ohs` on your
`PATH`. Create a user token with only the upload scope:

```sh
export OHS_TOKEN=$(curl -fsS -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" \
  -H "Content-Type: application/json" -d '{"scopes":["upload"]}' | jq -er .token)
```

Replace the example path below with the unpacked archive folder:

```sh
ohs import files ~/Downloads/strava-archive/ --dry-run
ohs import files ~/Downloads/strava-archive/
```

The dry run parses and counts the files without uploading. The actual import
walks the folder and its subfolders, including compressed activity files.
Rerunning it with the same files is safe: the server recognizes parts already
stored. Use `--since 2025-01-01` to keep activities starting on or after that
UTC day. See the [activity files guide](../connectors/activity-files.md#2-import-them)
for the other options.

Processing runs in the background after upload, so allow the ETL workers to
finish before querying the tables.

The source comes from the recorder information recognized inside the file,
not the site you downloaded it from. For example, a Strava GPX file has
`source_slug = 'strava'`, while a Garmin FIT recording downloaded from Strava
keeps `source_slug = 'garmin'`. A file without a recognized recorder uses
`source_slug = 'activity_files'`.

The examples below select files identified as Strava. Use `garmin` for Garmin
recordings, or omit the source condition to inspect all files imported for
your user.

## 4. Query the activities with SQL

Open `psql`, passing your user id as a variable:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

Set the tenant variable for that user in the same `psql` session:

```sql
SELECT tenant_id AS tenant
FROM ohs_core.end_users
WHERE id = :'user'
\gset
```

Your last Strava-recorded activities:

```sql
SELECT start_date, activity_type, duration_s / 60.0 AS minutes,
       round((distance_m / 1000)::numeric, 2) AS km,
       avg_heart_rate, elevation_gain_m
FROM ohs_data.workouts
WHERE tenant_id = :'tenant' AND user_id = :'user'
  AND provider = 'activity_files' AND source_slug = 'strava'
ORDER BY start_date DESC
LIMIT 10;
```

Running distance, month by month:

```sql
SELECT date_trunc('month', start_date)::date AS month,
       count(*) AS runs,
       round((sum(distance_m) / 1000)::numeric, 1) AS km
FROM ohs_data.workouts
WHERE tenant_id = :'tenant' AND user_id = :'user'
  AND provider = 'activity_files' AND source_slug = 'strava'
  AND activity_type = 'running'
GROUP BY month
ORDER BY month DESC
LIMIT 12;
```

The GPS route of your last Strava-recorded ride:

```sql
SELECT r."timestamp", r.latitude, r.longitude, r.altitude_m
FROM ohs_data.route_points r
WHERE r.tenant_id = :'tenant' AND r.user_id = :'user'
  AND r.workout_id = (
    SELECT id FROM ohs_data.workouts
    WHERE tenant_id = :'tenant' AND user_id = :'user'
      AND provider = 'activity_files' AND source_slug = 'strava'
      AND activity_type = 'cycling'
    ORDER BY start_date DESC LIMIT 1)
ORDER BY r."timestamp";
```

Per-second heart rate and power, when present in the file, are in
`ohs_data.quantity_samples`, under `heart_rate`, `cycling_power` and
`running_power`. Apply the same tenant, user and source filters when reading
those samples.

## 5. Export the workouts to CSV or Parquet

```sh
curl -fSsOJ "$OHS_URL/v1/users/$OHS_USER/exports/workouts?format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
```

Use `format=parquet` for pandas, Polars or DuckDB, and `start` and `end`
(`YYYY-MM-DD`) to restrict the time window. The export includes your user's
workouts from other sources too; its provenance fields identify each source.
The default merged view selects the preferred copy of overlapping activities;
`view=all` keeps every stored copy. See [CSV and Parquet exports](../exports.md)
for the columns and options.

## What the archive import does not give

Only streams and figures present in FIT, GPX and TCX activity files can be
imported. Account CSV files, photos and other archive contents are not read.
The import does not connect to Strava or fetch later activities: import
another archive or new activity files when you want to update your database.

For the mapping, file limits and duplicate rules, see the
[activity files guide](../connectors/activity-files.md). The
[Strava connector](../connectors/strava.md) is the separate OAuth path for
ongoing cloud synchronization.
