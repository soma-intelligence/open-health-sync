# Convert an Apple Health export to CSV

The Health app on the iPhone exports everything it holds as `export.zip`, but inside it is one
large XML file (`export.xml`) that Excel, Numbers or Google Sheets cannot open as it is, often a
few gigabytes once unzipped. This guide turns that export into clean CSV files, one per kind of
data: daily summaries, every sample (heart rate, steps, weight, HRV...), sleep and workouts. It
uses Open Health Sync, which is open source and runs on your own computer, so your data never
leaves it. Parquet comes out the same way, for pandas, Polars or DuckDB.

## 1. Export from the iPhone

Open the Health app, tap your picture, then *Export All Health Data*. Send the `export.zip` to
your computer (AirDrop works). Keep it zipped: the import reads it as it is.

## 2. Start Open Health Sync

You need Docker with Compose v2.24 or later, plus `curl`, `jq`, `openssl` and Go 1.26 or later.
From a clone of the repository:

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
```

This starts PostgreSQL and the `ohs` server, and writes your user's address and key to
`~/.ohs.env`. Treat that file like a password.

## 3. Import the export

Install the `ohs` command: take the archive for your system from the
[latest release](https://github.com/soma-intelligence/open-health-sync/releases) (`darwin` is
macOS), unpack it and put `ohs` on your `PATH`. Then import through a token that can only
upload, with your own time zone:

```sh
export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" -d '{"scopes":["upload"]}' | jq -r .token)
ohs import apple-health export.zip --tz Europe/Paris
```

The import reads the zip without unpacking it and prints counts, never a value. Many Apple
Health samples carry no time zone; `--tz` files them under the right local day. Running it again
sends nothing twice. Wait until processing is done, when `pending_jobs` reaches 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 4. Write the CSV files

```sh
go run ./recipes/export-all                    # CSV, into health-export-<date>/
go run ./recipes/export-all -format parquet    # Parquet instead
```

| File | What is in it |
|---|---|
| `daily.csv` | one row per day: steps, distance, active and total energy, floors climbed, resting heart rate, average HRV |
| `samples.csv` | every measurement, one row each: `type`, `value`, `unit`, `start_date`, `end_date`, the app and device that wrote it |
| `sleep.csv` | each night with its time in bed, asleep, deep, core (`light`) and REM |
| `workouts.csv` | each workout: activity, duration, distance, energy, average and maximum heart rate |
| `category-samples.csv` | events such as stand hours, menstrual flow or high heart rate notifications |
| `nutrition.csv` | food and drink logged in apps that write to Apple Health |

Times are in UTC (RFC 3339) and daily dates are your local day. All columns are described in
[CSV and Parquet exports](../exports.md).

One measurement only, such as your weight, in a date range:

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/samples?type=body_mass&start=2025-01-01&format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
```

## iPhone and Apple Watch counted twice

The iPhone and the Apple Watch both count steps and distance, so `export.xml` holds both, and
summing every steps row of a day counts each walk twice. The CSV files above use the merged view,
which keeps one source per figure and day: `daily.csv` is safe to sum and chart as it is. To keep
every source's rows, as `export.xml` has them, add `view=all` to an export URL and filter on the
`source_name` column ([why](../api.md#several-sources-of-one-metric-the-merged-view)).

## Next

- Query the same data in SQL instead: [Import Apple Health data into PostgreSQL](apple-health-to-postgres.md).
- Ask an AI assistant about it: [Connect Claude to your Apple Health data](apple-health-mcp.md).
- Add your other devices: every [connector](../connectors/README.md) exports in the same columns.
