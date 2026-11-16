# Export Dexcom CGM glucose data to CSV or SQL

To get your Dexcom continuous glucose monitor (CGM) readings out of the Dexcom cloud, connect
your account to Open Health Sync over Dexcom's official API. Every reading, one every five
minutes, lands in a PostgreSQL database you run. From there you query it in SQL, for time in
range or a daily average, or export it as CSV or Parquet next to your other devices. Open Health
Sync is open source and self-hosted.

Dexcom Clarity can show reports and download a CSV of your readings by hand. When you want your
readings kept up to date in your own database, Open Health Sync pulls them through the Dexcom
API v3 every hour.

## 1. Start an instance

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

## 2. Register a Dexcom app

Dexcom data reaches the API through a developer app that you register yourself:

1. Create an account and an app at [developer.dexcom.com](https://developer.dexcom.com).
2. Set its redirect URI to `<OHS_PUBLIC_URL>/oauth/dexcom/callback`, the address `ohs doctor`
   prints. It must match exactly.
3. Add its keys to `.env`, with the region of your Dexcom account, and restart:

   ```sh
   OHS_DEXCOM_CLIENT_ID=...
   OHS_DEXCOM_CLIENT_SECRET=...
   OHS_DEXCOM_REGION=us        # us, eu (every country outside the US except Japan), jp, or sandbox
   ```

A new app first works against Dexcom's sandbox, whose test users have invented readings:
`OHS_DEXCOM_REGION=sandbox` lets you try every step below before Dexcom approves the app for
real accounts. Details are in the [Dexcom connector guide](../connectors/dexcom.md).

## 3. Connect your account

```sh
go run ./recipes/connect dexcom
```

Sign in on Dexcom's page and allow access. The last 90 days are pulled in the background, in
windows of 30 days. Check that it is done when `pending_jobs` reaches 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

Readings uploaded by the Dexcom app reach the API 1 hour late in the US and 3 hours late
elsewhere, so the latest hours are always missing at first. The regular pulls read the last
hours again and store them once they arrive.

## 4. Export to CSV or Parquet

Your readings, one row each:

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/samples?type=blood_glucose&format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
```

| Column | Dexcom figure |
|---|---|
| `start_date` | the reading's time, in UTC |
| `value`, `unit` | the glucose value in `mg/dL` |
| `device_model` | the transmitter generation (`g6`, `g7`, ...) |
| `external_id` | Dexcom's `recordId` |
| `metadata` | `trend` and `trend_rate` (the arrow), `out_of_range` (`low` below 40, `high` above 400), `utc_offset_minutes` (your local time offset) and `specimen` |

For mmol/L, divide by 18. Add `start` and `end` (`YYYY-MM-DD`) for a window, and
`format=parquet` for pandas, Polars or DuckDB. `go run ./recipes/export-all` writes every list
at once. Columns and types are described in [CSV and Parquet exports](../exports.md).

## 5. Query it with SQL

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

Average, minimum and maximum per day, with the share of readings in range (70 to 180 mg/dL):

```sql
SELECT start_date::date AS day,
       count(*) AS readings,
       round(avg(value)) AS avg_mgdl,
       min(value) AS min_mgdl, max(value) AS max_mgdl,
       round(100.0 * count(*) FILTER (WHERE value BETWEEN 70 AND 180) / count(*)) AS in_range_pct
FROM ohs_data.quantity_samples
WHERE user_id = :'user' AND provider = 'dexcom' AND quantity_type = 'blood_glucose'
GROUP BY day
ORDER BY day DESC
LIMIT 14;
```

Days are in UTC here. For your own days, use `start_date AT TIME ZONE 'Europe/Paris'` (your
zone) in place of `start_date`.

Your glucose by hour of the day over the last 30 days, to see when it runs high:

```sql
SELECT extract(hour FROM start_date AT TIME ZONE 'Europe/Paris') AS hour,
       round(avg(value)) AS avg_mgdl,
       round(100.0 * count(*) FILTER (WHERE value > 180) / count(*)) AS above_pct
FROM ohs_data.quantity_samples
WHERE user_id = :'user' AND provider = 'dexcom' AND quantity_type = 'blood_glucose'
  AND start_date > now() - interval '30 days'
GROUP BY hour
ORDER BY hour;
```

## What you do not get

- Events (carbs, insulin, exercise), calibrations and alerts: the connector pulls glucose
  readings only for now.
- History older than 90 days at connection time: keep the instance running to build your
  history from there.

This data is for your own records and analysis, not for treatment decisions: use your Dexcom
app and your care team for those.

## Next

- Put meals and sleep next to it: [import Apple Health](apple-health-to-postgres.md) or connect
  your watch or ring, and the same tables hold every source.
- [Get the data as FHIR R4](../fhir.md): CGM readings are coded as glucose in interstitial
  fluid (LOINC `99504-3`).
