# Export intervals.icu activities and wellness to CSV or PostgreSQL

To get your intervals.icu data into a database you control, connect your intervals.icu account
to Open Health Sync over OAuth. It pulls your activities and your daily wellness records (HRV,
resting heart rate, weight, sleep score, steps) into PostgreSQL, keeps them up to date with
intervals.icu's webhooks, and exports them as CSV or Parquet at any time. Open Health Sync is
open source and self-hosted.

Many athletes already feed intervals.icu from Garmin, Wahoo, Zwift, Coros, Suunto or Polar, so
one connection reaches all of them, and each workout says which upstream it came from.

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

## 2. Register an intervals.icu app

intervals.icu data reaches the API through an OAuth app that you apply for yourself:

1. Signed in to intervals.icu, fill in the form at
   [intervals.icu/oauth/apply](https://intervals.icu/oauth/apply). The app shows as "Pending"
   under **Settings, Apps** until the intervals.icu developer approves it.
2. Once approved, open **Manage App** and add the redirect URI
   `<OHS_PUBLIC_URL>/oauth/intervals_icu/callback` (`ohs doctor` prints it).
3. Add its keys to `.env` and restart:

   ```sh
   OHS_INTERVALS_ICU_CLIENT_ID=...
   OHS_INTERVALS_ICU_CLIENT_SECRET=...
   ```

Webhooks are optional: without them, new data arrives with the pull every 6 hours. To get it
within a minute, set the webhook URL and secret as the
[intervals.icu connector guide](../connectors/intervals-icu.md#5-webhooks) explains.

## 3. Connect your account

```sh
go run ./recipes/connect intervals_icu
```

Allow access on the intervals.icu page. The last 30 days are pulled in the background. Check
that it is done when `pending_jobs` reaches 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 4. Export to CSV or Parquet

Everything at once, one file per list:

```sh
go run ./recipes/export-all                    # CSV, into health-export-<date>/
go run ./recipes/export-all -format parquet    # Parquet, for DuckDB, Polars or pandas
```

Or one list at a time over the API:

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/workouts?format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/samples?type=hrv_rmssd&format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
```

| File | intervals.icu data in it |
|---|---|
| `workouts` | each activity: type, start and end, moving time, distance, speed, elevation gain, average and maximum heart rate, energy; `metadata` holds the average power, the training load, the upstream (`source_provider`: `garmin`, `wahoo`, `zwift`, ...) and the intervals.icu type |
| `samples` | one value per day and wellness figure: `hrv_rmssd`, `hrv_sdnn`, `resting_heart_rate`, `body_mass`, `body_fat_percentage`, `sleep_score`, `step_count`, `oxygen_saturation`, `respiratory_rate`, `vo2_max`, blood pressure |

Add `start` and `end` (`YYYY-MM-DD`) for a window. Columns and types are described in
[CSV and Parquet exports](../exports.md).

## 5. Query it with SQL

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

Training load and HRV, day by day, to see how hard sessions show up the next morning:

```sql
WITH load AS (
  SELECT start_date::date AS day,
         sum((metadata->>'training_load')::numeric) AS training_load
  FROM ohs_data.workouts
  WHERE user_id = :'user' AND provider = 'intervals_icu'
  GROUP BY day
), hrv AS (
  SELECT start_date::date AS day, round(avg(value)::numeric) AS hrv_ms
  FROM ohs_data.quantity_samples
  WHERE user_id = :'user' AND provider = 'intervals_icu' AND quantity_type = 'hrv_rmssd'
  GROUP BY day
)
SELECT h.day, h.hrv_ms, coalesce(l.training_load, 0) AS load_that_day
FROM hrv h LEFT JOIN load l ON l.day = h.day
ORDER BY h.day DESC
LIMIT 21;
```

Distance by upstream and sport over the last 90 days:

```sql
SELECT metadata->>'source_provider' AS upstream, activity_type,
       count(*) AS activities,
       round((sum(distance_m) / 1000)::numeric, 1) AS km
FROM ohs_data.workouts
WHERE user_id = :'user' AND provider = 'intervals_icu'
  AND start_date > now() - interval '90 days'
GROUP BY upstream, activity_type
ORDER BY km DESC NULLS LAST;
```

## What you do not get

- Activities that reached intervals.icu from Strava: intervals.icu does not hand them to other
  apps. For those, [export your Strava activities](strava-export.md) directly.
- Streams (per-second heart rate, power, GPS), laps and the original files. For full streams,
  [import the FIT files](fit-gpx-tcx-import.md) from your device or its app.
- Bedtimes: intervals.icu gives a nightly sleep score but no start and end, so no sleep session
  is created.
- Fitness, fatigue and form (`ctl`, `atl`) and subjective fields such as mood or soreness.

## Next

- Add your watch or ring directly: every [connector](../connectors/README.md) exports in the
  same columns, and [merging several wearables](merge-multiple-wearables.md) explains how
  overlapping figures are handled.
- [Read your HRV trend](../learn/hrv-trend.md), a longer project on the same data.
