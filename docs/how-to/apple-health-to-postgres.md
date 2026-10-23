# Import Apple Health data into PostgreSQL

To export Apple Health data to PostgreSQL, run Open Health Sync and load the Health app export
with `ohs import apple-health`: your history lands in Postgres as tables you can query with SQL.
Open Health Sync is open source and runs on your own computer, so the data never leaves it.

The Health app on the iPhone can export everything it holds, but the export is one large XML
file that a spreadsheet or a database cannot read as it is. This guide loads it into PostgreSQL
as clean tables (samples, sleep sessions, workouts, daily summaries) with Open Health Sync, then
queries them with SQL. It takes about 15 minutes, and everything runs on your own computer.

## 1. Start Postgres and Open Health Sync

You need Docker with Compose v2.24 or later, plus `curl`, `jq` and `openssl`. From a clone of the
repository:

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
```

This starts PostgreSQL (with TimescaleDB) and the `ohs` server next to it. The
[quickstart](../../README.md#quickstart) explains each line.

## 2. Create your user

This creates a tenant called `personal`, a user called `me` and an API key, and writes the three
to a file the next steps read:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
```

The key in that file reads everything on your instance: treat it like a password.

## 3. Export from the iPhone

Open the Health app, tap your picture, then *Export All Health Data*. Send the `export.zip` to
your computer (AirDrop works). A few years of data make a file of a few hundred megabytes.

## 4. Import the export

Install the `ohs` command on your computer: take the archive for your system from the
[latest release](https://github.com/soma-intelligence/open-health-sync/releases) (`darwin` is
macOS), unpack it and put `ohs` on your `PATH`. Then import the export through a token that can
only upload:

```sh
export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" -d '{"scopes":["upload"]}' | jq -r .token)
ohs import apple-health export.zip --dry-run    # counts what it would send
ohs import apple-health export.zip
```

The import reads the zip without extracting it, in about 150 MB of memory, and prints counts,
never a health value. Running it again sends nothing twice, so an interrupted import is resumed
by running it again. Many samples carry no time zone: add `--tz Europe/Paris` (your own zone) so
they count on the right local day. All the options are in
[importing a Health app export](../connectors/apple-health.md#importing-a-health-app-export).

The server then converts units, rebuilds each night from its sleep stages and removes
duplicates. Wait until `pending_jobs` is 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 5. Query it with SQL

Open `psql` in the database container, with your user id in a variable:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

What came in, by type:

```sql
SELECT quantity_type, unit, count(*) AS samples,
       min(start_date)::date AS first_day, max(start_date)::date AS last_day
FROM ohs_data.quantity_samples
WHERE user_id = :'user'
GROUP BY quantity_type, unit
ORDER BY samples DESC
LIMIT 15;
```

Resting heart rate, week by week:

```sql
SELECT date_trunc('week', start_date)::date AS week,
       round(avg(value)::numeric, 1) AS resting_bpm
FROM ohs_data.quantity_samples
WHERE user_id = :'user' AND quantity_type = 'resting_heart_rate'
GROUP BY week
ORDER BY week DESC
LIMIT 12;
```

Your last nights:

```sql
SELECT bedtime_start, round(total_sleep_seconds / 3600.0, 1) AS hours,
       deep_sleep_seconds / 60 AS deep_min, rem_sleep_seconds / 60 AS rem_min
FROM ohs_data.sleep_sessions
WHERE user_id = :'user'
ORDER BY bedtime_start DESC
LIMIT 7;
```

Your last workouts:

```sql
SELECT start_date, activity_type, duration_s / 60 AS minutes,
       round((distance_m / 1000)::numeric, 2) AS km, avg_heart_rate
FROM ohs_data.workouts
WHERE user_id = :'user'
ORDER BY start_date DESC
LIMIT 10;
```

Steps need care: an iPhone and an Apple Watch both count them, so adding every sample counts a
walk twice. Look at each source apart:

```sql
SELECT (start_date AT TIME ZONE 'Europe/Paris')::date AS day, source_name,
       sum(value)::int AS steps
FROM ohs_data.quantity_samples
WHERE user_id = :'user' AND quantity_type = 'step_count'
  AND start_date > now() - interval '7 days'
GROUP BY day, source_name
ORDER BY day DESC, steps DESC;
```

The REST API does this choice for you: `GET /v1/users/{id}/daily` keeps one source per figure
and day ([the merged view](../api.md#several-sources-of-one-metric-the-merged-view)).

## About the tables

Every table is described in the [data model](../data-model.md). Times are stored in UTC, every
value in one unit per type (`bpm`, `kg`, `count`), and each row says where it came from
(`provider`, `source_name`, `device_model`). The tables follow the migrations of each release;
the REST API under `/v1` is the interface that stays stable, so prefer it for code you ship to
others.

## Keep it up to date

Export again and run the same import: only the new records are stored. For data that arrives on
its own, an iOS app built with the [Swift SDK](../sdks/swift.md#healthkit-sync) uploads new
HealthKit data in the background.

## Next

- [Connect Claude to your Apple Health data](apple-health-mcp.md) and ask it questions.
- [Export everything as CSV or Parquet](../recipes/export-all.md) for a spreadsheet or pandas.
- Add a ring or a band: every [connector](../connectors/README.md) lands in the same tables.
