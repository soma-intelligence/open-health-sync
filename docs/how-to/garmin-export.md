# Export Garmin Connect activities to PostgreSQL or CSV

Garmin Connect lets you download your whole account: every activity you ever synced, as the
original FIT files. Those files are binary, so a spreadsheet or a database cannot read them as
they are. This guide loads a Garmin export into PostgreSQL with Open Health Sync: each activity
becomes a workout row with its distance, heart rate and elevation, plus its GPS route and
per-second heart rate and power. Then you query it in SQL or export it as CSV or Parquet. No
Garmin developer account is needed, and everything runs on your own computer.

## 1. Request your Garmin export

1. Sign in at [garmin.com/account/datamanagement](https://www.garmin.com/account/datamanagement/)
   and open **Export Your Data**.
2. Choose **Request Data Export**.
3. Garmin emails a download link, usually within 48 hours, sometimes later.

Keep the zip as it is: the import opens the zips inside it. The JSON files of the export
(sleep, stress, daily summaries) are not read; this guide is about activities. To get one
activity now, open it in Garmin Connect on the web, then the gear menu, **Export Original**.

## 2. Start Postgres and Open Health Sync

You need Docker with Compose v2.24 or later, plus `curl`, `jq` and `openssl`. From a clone of the
repository:

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
```

Then create your own user, and keep its details in a file the next steps read:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
```

The key in that file reads everything on your instance: treat it like a password.

## 3. Import the export

Install the `ohs` command on your computer: take the archive for your system from the
[latest release](https://github.com/soma-intelligence/open-health-sync/releases) (`darwin` is
macOS), unpack it and put `ohs` on your `PATH`. Then import through a token that can only
upload:

```sh
export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" -d '{"scopes":["upload"]}' | jq -r .token)
ohs import files ~/Downloads/garmin-export.zip --dry-run    # counts what it would send
ohs import files ~/Downloads/garmin-export.zip
```

The command prints counts and formats only, never a file name: Garmin names the files after
your email address. Each file is sent once, so running it again, or importing a newer export
later, only adds the new activities. `--since 2025-01-01` keeps the activities from that day on.
The other options are in the [activity files guide](../connectors/activity-files.md#2-import-them).

## 4. Query it with SQL

Open `psql` in the database container, with your user id in a variable:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

Your last activities:

```sql
SELECT start_date, activity_type, duration_s / 60 AS minutes,
       round((distance_m / 1000)::numeric, 2) AS km,
       avg_heart_rate, elevation_gain_m
FROM ohs_data.workouts
WHERE user_id = :'user' AND source_slug = 'garmin'
ORDER BY start_date DESC
LIMIT 10;
```

Running distance, month by month:

```sql
SELECT date_trunc('month', start_date)::date AS month,
       count(*) AS runs,
       round((sum(distance_m) / 1000)::numeric, 1) AS km
FROM ohs_data.workouts
WHERE user_id = :'user' AND activity_type = 'running'
GROUP BY month
ORDER BY month DESC
LIMIT 12;
```

The GPS route of your last ride, point by point:

```sql
SELECT r."timestamp", r.latitude, r.longitude, r.altitude_m
FROM ohs_data.route_points r
WHERE r.workout_id = (
  SELECT id FROM ohs_data.workouts
  WHERE user_id = :'user' AND activity_type = 'cycling'
  ORDER BY start_date DESC LIMIT 1)
ORDER BY r."timestamp";
```

Per-second heart rate and power are in `ohs_data.quantity_samples`, with the types
`heart_rate`, `cycling_power` and `running_power`.

## 5. Export to CSV or Parquet

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/workouts?format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
```

`format=parquet` gives a file for pandas, Polars or DuckDB, and `start` and `end`
(`YYYY-MM-DD`) a time window. Each row keeps the file's extra figures (power, cadence, moving
time, device name) in its `metadata` column. Columns and types are described in
[CSV and Parquet exports](../exports.md); `GET /v1/users/{id}/workouts/{workout_id}/route` gives
one route as JSON.

## What the export does not give

Laps, swim lengths, running dynamics and beat-to-beat intervals stay in the stored FIT file and
are not mapped to tables. Sleep, stress and Body Battery are in the export's JSON files, which
this import does not read. For those and for new data as it arrives, the
[Garmin connector](../connectors/garmin.md) uses Garmin's Health API, which needs keys from
Garmin's partner program.

## Next

- Import other devices' files the same way: COROS, Wahoo, Zwift, Suunto, Polar and Strava
  archives are covered in the [activity files guide](../connectors/activity-files.md).
- Add your other devices: every [connector](../connectors/README.md) lands in the same tables.
- Ask an AI assistant about your training through the read-only [MCP server](../mcp.md).
