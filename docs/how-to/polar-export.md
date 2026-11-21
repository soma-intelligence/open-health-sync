# Export Polar data to CSV or PostgreSQL

To export Polar data to CSV or PostgreSQL, connect your Polar Flow account to Open Health Sync
through Polar's AccessLink API: it pulls exercises, sleep, Nightly Recharge, daily activity and
continuous heart rate into a PostgreSQL database you run, and exports them as CSV or Parquet at
any time. Open Health Sync is open source and self-hosted, and you register your own AccessLink
client.

AccessLink only serves a rolling window of recent data: about 28 days of sleep and Nightly
Recharge, 30 days of exercises. Connect early, and keep the instance running: once stored, the
data stays after Polar's window has moved on.

## 1. Start an instance

Follow the [quickstart](../../README.md#quickstart) to clone the repository, create `.env` with
a fresh encryption key and start it with Docker Compose (the demo seed is optional). Then create
your own user:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
```

No keys yet? `ohs seed --demo --persona runner` writes 90 days of synthetic data that includes a
Polar watch (runs, rides, walks and strength sessions with heart rate, energy, distance and a GPS
route), so you can try the queries below first ([demo data](../demo.md#personas)).

## 2. Connect your Polar account

1. Sign in at `admin.polaraccesslink.com` with a Polar Flow account, accept the AccessLink terms
   and create a client with the callback URL `<OHS_PUBLIC_URL>/oauth/polar/callback`
   (`ohs doctor` prints it; it must match exactly, scheme included).
2. Add its keys to `.env` and restart:

   ```sh
   OHS_POLAR_CLIENT_ID=...
   OHS_POLAR_CLIENT_SECRET=...
   ```

3. Open Polar's consent page and allow access (one scope, `accesslink.read_all`):

   ```sh
   go run ./recipes/connect polar
   ```

If you have not accepted Polar's mandatory consents in Polar Flow, Polar refuses the
registration: accept them there and connect again. Wait until `pending_jobs` is 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 3. What the backfill reaches

A new connection is backfilled over the last 28 days, then pulled every hour (every 6 hours once
the AccessLink webhook is set up, see the [connector guide](../connectors/polar.md#6-webhooks)).

| Data | Reaches back |
|---|---|
| Exercises | 30 days, by upload time |
| Sleep, Nightly Recharge | 28 days |
| Daily activity | 365 days |
| Continuous heart rate | as Polar keeps it |
| Physical info | current values only |

For older training sessions, export them from Polar Flow on the web (**Diary**, a session,
**Export session**, TCX) and load the files with `ohs import files`
([activity files](../connectors/activity-files.md#2-import-them)).

## 4. Query it in PostgreSQL

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

Your last exercises:

```sql
SELECT start_date, activity_type, duration_s / 60 AS minutes,
       round((distance_m / 1000)::numeric, 2) AS km, avg_heart_rate, active_energy_kcal
FROM ohs_data.workouts
WHERE user_id = :'user' AND provider = 'polar'
ORDER BY start_date DESC
LIMIT 10;
```

Your last nights:

```sql
SELECT bedtime_start, round(total_sleep_seconds / 3600.0, 1) AS hours,
       deep_sleep_seconds / 60 AS deep_min, rem_sleep_seconds / 60 AS rem_min, sleep_score
FROM ohs_data.sleep_sessions
WHERE user_id = :'user' AND provider = 'polar'
ORDER BY bedtime_start DESC
LIMIT 14;
```

Nightly Recharge HRV (RMSSD), night by night:

```sql
SELECT start_date, round(value::numeric, 1) AS hrv_rmssd_ms
FROM ohs_data.quantity_samples
WHERE user_id = :'user' AND provider = 'polar' AND quantity_type = 'hrv_rmssd'
ORDER BY start_date DESC
LIMIT 14;
```

Every table is described in the [data model](../data-model.md).

## 5. Export to CSV or Parquet

```sh
go run ./recipes/export-all                    # every list as CSV, into health-export-<date>/
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/workouts?format=csv&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

`format=parquet` gives a file for DuckDB, Polars or pandas, and `start` and `end`
(`YYYY-MM-DD`) a window. Keep the rows whose `provider` is `polar`. Training load, running index
and the Polar sport name are in each workout's `metadata` column. Columns are described in
[CSV and Parquet exports](../exports.md).

## What the export does not give

The 5 minute HRV and breathing samples of a night are not stored (their times carry no date).
Polar sends no deletion events, so data deleted in Polar Flow stays in Open Health Sync. The
full mapping is in the [Polar connector guide](../connectors/polar.md).

## Related

- [Export Strava activities to PostgreSQL or CSV](strava-export.md)
- [Import FIT, GPX and TCX files](fit-gpx-tcx-import.md)
- [Merge several wearables into one timeline](merge-multiple-wearables.md)
- [Export Garmin Connect activities to PostgreSQL or CSV](garmin-export.md)
- [CSV and Parquet exports](../exports.md)
- [README](../../README.md)
