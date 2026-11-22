# Export Eight Sleep data to CSV or SQL

The Eight Sleep app shows your nights, sleep score, heart rate, HRV and bed temperature, but it
has no export button and Eight Sleep has no public API. Open Health Sync signs in to your Eight
Sleep account the way the app does, pulls your nights every few hours and keeps them in a
PostgreSQL database you run. From there you query them in SQL or export them as CSV or
Parquet, next to your other devices.

Before you start, read what this means: Eight Sleep does not document or approve this way in,
it can stop working when the app changes, and Eight Sleep's terms may not allow other software
to sign in. Your instance keeps your Eight Sleep password, sealed with your encryption key
([details](../connectors/eight-sleep.md#how-it-differs-from-the-other-connectors)).

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

The Eight Sleep connector needs no keys: it is on by default.

## 2. Connect your Eight Sleep account

```sh
go run ./recipes/connect eight_sleep
```

This opens a sign-in page served by your own instance, not by Eight Sleep. Enter the email and
password you use in the Eight Sleep app. An account that signs in with Google or Apple needs a
password set in the Eight Sleep app first.

The last 30 days arrive within a minute, and new nights follow with the regular pulls. Check that
the first pull is done when `pending_jobs` reaches 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 3. Export to CSV or Parquet

Your nights, one row per sleep session:

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/sleep?format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
```

| Column | Eight Sleep figure |
|---|---|
| `bedtime_start`, `bedtime_end` | the night, in UTC |
| `total_sleep_seconds` | deep + light + REM |
| `deep_sleep_seconds`, `light_sleep_seconds`, `rem_sleep_seconds`, `awake_seconds` | the stages |
| `sleep_score` | the day's sleep score, 0 to 100 |
| `hrv_avg_ms`, `respiratory_rate` | the night's HRV and breathing rate |
| `metadata` | `sleep_quality_score`, `sleep_routine_score`, `toss_and_turns`, and bed and room temperature (`bed_temperature_avg_c`, `room_temperature_avg_c`, with their minimum and maximum) |

The heart rate, HRV and breathing rate readings through the night are in the `samples` export
(`type=heart_rate`, `hrv_rmssd`, `respiratory_rate`). `go run ./recipes/export-all` writes
every list at once, and `-format parquet` gives files for pandas, Polars or DuckDB. Columns and
types are described in [CSV and Parquet exports](../exports.md).

## 4. Query it with SQL

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

Your last nights, with the bed temperature:

```sql
SELECT bedtime_start::date AS night,
       round(total_sleep_seconds / 3600.0, 1) AS hours,
       deep_sleep_seconds / 60 AS deep_min, rem_sleep_seconds / 60 AS rem_min,
       sleep_score, round(hrv_avg_ms::numeric) AS hrv,
       (metadata->>'bed_temperature_avg_c')::numeric AS bed_c
FROM ohs_data.sleep_sessions
WHERE user_id = :'user' AND provider = 'eight_sleep'
ORDER BY bedtime_start DESC
LIMIT 14;
```

Does a cooler bed go with more deep sleep? Average deep sleep by bed temperature:

```sql
SELECT round((metadata->>'bed_temperature_avg_c')::numeric) AS bed_c,
       count(*) AS nights,
       round(avg(deep_sleep_seconds) / 60) AS deep_min
FROM ohs_data.sleep_sessions
WHERE user_id = :'user' AND provider = 'eight_sleep'
  AND metadata ? 'bed_temperature_avg_c'
GROUP BY bed_c
ORDER BY bed_c;
```

## What you do not get

- History older than the first pull: the connector fetches the last 30 days when you connect,
  then keeps every new night. Keep the instance running to build your history.
- The Pod's settings, schedules and alarms, and the per-minute bed and room temperature (only
  the night's average, minimum and maximum are in columns; the readings stay in the stored raw
  night).

## Next

- Compare with your watch or ring: connect [Oura](../connectors/oura.md),
  [WHOOP](../connectors/whoop.md) or [import Apple Health](apple-health-to-postgres.md), and the
  same tables hold every source.
- [Read your sleep](../learn/read-your-sleep.md), a longer project on the same data.
