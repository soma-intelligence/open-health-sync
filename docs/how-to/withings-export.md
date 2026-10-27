# Export Withings weight and blood pressure to CSV or SQL

The Withings app (Health Mate) can send you a one-off export of your measurements as CSV files.
When you want more than a one-off file, Open Health Sync pulls your Withings data through the
Withings API and keeps it in a PostgreSQL database you run: every weigh-in, body composition
reading and blood pressure measurement, in the same columns as your other devices. You can
export it again at any time as CSV or Parquet, read it over an API, or query it in SQL.

## 1. Start an instance

Follow the [quickstart](../../README.md#quickstart) to *Start the stack*: clone the repository,
create `.env` with a fresh encryption key, and start it with Docker Compose. Then create your
own user:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
```

No Withings keys yet? `ohs seed --demo` writes 90 days of synthetic data that includes a
Withings scale and blood pressure monitor (weight, body fat, lean mass, systolic and diastolic
pressure, heart rate), so you can try every step below first ([demo data](../demo.md)).

## 2. Connect your Withings account

Withings data reaches the API through a Withings developer application that you register
yourself:

1. In the Withings developer dashboard (`developer.withings.com`), create an application with
   the callback URL `<OHS_PUBLIC_URL>/oauth/withings/callback`
   ([details](../connectors/withings.md#1-get-your-keys)).
2. Add its keys to `.env` and restart:

   ```sh
   OHS_WITHINGS_CLIENT_ID=...
   OHS_WITHINGS_CLIENT_SECRET=...
   ```

3. Open Withings' consent page and allow access:

   ```sh
   python3 recipes/connect.py withings
   ```

The first data arrives a few seconds later. The connector backfills the last 30 days, then
pulls every 6 hours and whenever Withings notifies a new measurement. Check that the backfill
is done when `pending_jobs` reaches 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 3. Export to CSV or Parquet

One measurement at a time, with `type`:

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/samples?type=body_mass&format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/samples?type=blood_pressure_systolic&format=csv" \
  -H "Authorization: Bearer $OHS_KEY"
```

Or everything at once, one file per list:

```sh
python3 recipes/export-all.py                     # CSV, into health-export-<date>/
python3 recipes/export-all.py --format parquet    # Parquet, for pandas, Polars or DuckDB
```

| Withings data | `type` values in the `samples` export |
|---|---|
| Body measurements (scales) | `body_mass`, `body_fat_percentage`, `body_fat_mass`, `lean_body_mass`, `muscle_mass`, `water_mass`, `bone_mass`, `visceral_fat_rating`, `pulse_wave_velocity`, `vascular_age` |
| Blood pressure | `blood_pressure_systolic`, `blood_pressure_diastolic`, `heart_rate` |
| Other measures | `oxygen_saturation`, `body_temperature`, `skin_temperature`, `vo2_max` |
| Activity | `step_count`, `distance_walking_running`, `active_energy_burned`, per day and intraday |

Sleep, workouts and ECG recordings have their own exports (`sleep`, `workouts`, `ecg`), and the
full list is in the [Withings connector guide](../connectors/withings.md#data-produced).

Every row has a `provider` column, `withings` here. Add `start` and `end` (`YYYY-MM-DD`) for a window. Columns and types are
described in [CSV and Parquet exports](../exports.md).

```python
import pandas as pd
weight = pd.read_parquet("health-export-2026-10-19/samples.parquet")
weight = weight[(weight["provider"] == "withings") & (weight["type"] == "body_mass")]
print(weight.set_index("start_date")["value"].resample("W").mean().tail(12))
```

## 4. Query it with SQL

Open `psql` in the database container, with your user id in a variable:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

Weight, week by week:

```sql
SELECT date_trunc('week', start_date)::date AS week,
       round(avg(value)::numeric, 1) AS kg
FROM ohs_data.quantity_samples
WHERE user_id = :'user' AND provider = 'withings' AND quantity_type = 'body_mass'
GROUP BY week
ORDER BY week DESC
LIMIT 12;
```

Blood pressure readings, systolic and diastolic side by side. Withings sends both values of one
reading with the same time, so they pair on `start_date`:

```sql
SELECT s.start_date, s.value::int AS systolic, d.value::int AS diastolic
FROM ohs_data.quantity_samples s
JOIN ohs_data.quantity_samples d
  ON d.user_id = s.user_id AND d.start_date = s.start_date
 AND d.provider = 'withings' AND d.quantity_type = 'blood_pressure_diastolic'
WHERE s.user_id = :'user' AND s.provider = 'withings'
  AND s.quantity_type = 'blood_pressure_systolic'
ORDER BY s.start_date DESC
LIMIT 20;
```

Point Grafana, Metabase or a notebook at the same database for charts
([build a dashboard](../learn/your-dashboard.md)).

## What the Withings API does not give

The backfill covers the last 30 days. Older measurements are not pulled, so for years of
history, start from the Health Mate CSV export or, on an iPhone that syncs Withings to Apple
Health, import the [Apple Health export](apple-health-to-postgres.md). From then on the
connector keeps everything new.

## Next

- Keep it running: new readings arrive on Withings notifications and scheduled pulls, and the
  same export picks them up next time.
- Add your other devices: every [connector](../connectors/README.md) exports in the same columns.
- [Get the data as FHIR R4](../fhir.md) to hand it to a doctor's or a research system.
