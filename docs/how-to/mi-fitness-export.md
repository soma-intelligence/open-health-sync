# Export Xiaomi Mi Fitness data to CSV or PostgreSQL

Xiaomi publishes no health API, but your Xiaomi account lets you download a copy of your Mi
Fitness data. Load that copy into Open Health Sync with `ohs import mi-fitness`: heart rate,
resting heart rate, SpO2, steps, weight, VO2 max, sleep with stages and workouts land in a
PostgreSQL database you run, in the same tables as your other devices, and export as CSV or
Parquet. Open Health Sync is open source and self-hosted, and no Xiaomi credentials or
reverse-engineered API are involved.

This covers Xiaomi bands and watches that sync to Mi Fitness (formerly Xiaomi Wear). Older Mi
Bands on Zepp Life reach Open Health Sync through Health Connect or Apple Health instead.

## 1. Request your data copy

Signed in to your Xiaomi account:

1. open [account.xiaomi.com](https://account.xiaomi.com), then **Privacy**, **Manage your data**;
2. pick **Mi Fitness** and its download button, and bind an email address when asked;
3. a download link arrives by email, usually within minutes.

The copy is a set of CSV files named `<date>_<account id>_MiFitness_<table>.csv` plus a PDF
guide. If it came as a password protected zip, unzip it; `ohs import` reads a folder or a plain
zip.

## 2. Start an instance

Follow the [quickstart](../../README.md#quickstart) to clone the repository, create `.env` with
a fresh encryption key and start it with Docker Compose (the demo seed is optional). Then create
your own user:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
```

The Xiaomi connector is always on: there are no keys to set.

## 3. Import the copy

Install the `ohs` command from the
[latest release](https://github.com/soma-intelligence/open-health-sync/releases) and put it on
your `PATH`. Then import through a token that can only upload:

```sh
export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" -d '{"scopes":["upload"]}' | jq -r .token)
ohs import mi-fitness ~/Downloads/MiFitness --dry-run    # converts and counts, uploads nothing
ohs import mi-fitness ~/Downloads/MiFitness
```

`--since 2026-01-01` keeps rows from that UTC day on. The command prints counts and key names
only, never a health value, the token or the account id. Running it again uploads nothing twice,
and a newer copy imported later updates rows in place. Wait until `pending_jobs` is 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 4. Query it in PostgreSQL

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

What came in, by type:

```sql
SELECT quantity_type, count(*) AS samples,
       min(start_date)::date AS first_day, max(start_date)::date AS last_day
FROM ohs_data.quantity_samples
WHERE user_id = :'user' AND provider = 'xiaomi'
GROUP BY quantity_type
ORDER BY samples DESC;
```

Steps per day (per-minute rows are deltas, so their sum is the day's total):

```sql
SELECT start_date::date AS day, sum(value)::int AS steps
FROM ohs_data.quantity_samples
WHERE user_id = :'user' AND provider = 'xiaomi' AND quantity_type = 'step_count'
GROUP BY day
ORDER BY day DESC
LIMIT 14;
```

Sleep sessions are in `ohs_data.sleep_sessions` and workouts in `ohs_data.workouts`, both with
`provider = 'xiaomi'`; the [data model](../data-model.md) describes every table.

## 5. Export to CSV or Parquet

```sh
go run ./recipes/export-all                    # every list as CSV, into health-export-<date>/
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/samples?type=heart_rate&format=csv&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

`format=parquet` gives a file for DuckDB, Polars or pandas, and `start` and `end`
(`YYYY-MM-DD`) a window. Keep the rows whose `provider` is `xiaomi`; the device model and the Mi
Fitness table and key of each row are in its `metadata` column. Columns are described in
[CSV and Parquet exports](../exports.md).

## Keep it up to date

The copy is a backfill. For what comes after, either request a new copy and import it again, or
turn on Mi Fitness's Health Connect (Android) or Apple Health (iPhone) sync. Its Health Connect
records keep provider `health_connect` with source `xiaomi`; on an iPhone they arrive with the
app's name as their source. A reading both imported from the copy and synced is stored under each
provider: read one provider for the period both cover.

## What the import does not give

Stress, HRV (Xiaomi does not say whether its value is RMSSD or SDNN), skin and body temperature,
blood pressure, daily aggregates and GPS tracks are stored raw and not mapped yet. Zero figures
are placeholders in the copy and give no record. The full mapping is in the
[Xiaomi connector guide](../connectors/xiaomi.md).

## Related

- [Send Health Connect data to your server](health-connect-to-server.md)
- [Import Apple Health data into PostgreSQL](apple-health-to-postgres.md)
- [Merge several wearables into one timeline](merge-multiple-wearables.md)
- [Export Polar data to CSV or PostgreSQL](polar-export.md)
- [CSV and Parquet exports](../exports.md)
- [README](../../README.md)
