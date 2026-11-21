# Get Fitbit data through the Google Health API and export it

To get Fitbit data through an API now, connect the Google Account that holds it to Open Health
Sync: it pulls steps, heart rate, HRV, SpO2, sleep, workouts and body measurements from the
Google Health API into a database you run, and exports them as CSV or Parquet at any time. Open
Health Sync is open source and self-hosted, and you create your own Google Cloud project.

Google turns off the legacy Fitbit Web API (`api.fitbit.com`) and the Fitbit OAuth endpoints on
2026-11-02. This connector never used them: it reads `health.googleapis.com` (v4) only, and
every user consents again with their Google Account.

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

No keys yet? `ohs seed --demo` writes 90 days of synthetic data that includes a Fitbit band
(hourly steps, distance, active energy and floors, heart rate, resting heart rate, SpO2), so you
can try the exports first ([demo data](../demo.md)).

## 2. Create a Google Cloud project

Before you start, know the limits ([access](../connectors/fitbit.md#access-read-this-first)):
every Google Health API scope is restricted, so an app Google has not verified serves at most 100
users, and while its consent screen is in *Testing*, refresh tokens expire after 7 days. A
person or a family fits well within that.

1. Create a Google Cloud project and enable the **Google Health API**.
2. On the OAuth consent screen choose **External**; while in Testing, add each user's Google
   Account as a test user.
3. Add the three read scopes listed in the [connector guide](../connectors/fitbit.md#3-scopes).
4. Create an OAuth client ID of type **Web application** with the redirect URI
   `<OHS_PUBLIC_URL>/oauth/fitbit/callback` (`ohs doctor` prints it).
5. Add its keys to `.env` and restart:

   ```sh
   OHS_FITBIT_CLIENT_ID=...apps.googleusercontent.com
   OHS_FITBIT_CLIENT_SECRET=...
   ```

## 3. Connect your Google Account

First link your Google Account to Google Health in the Fitbit (Google Health) mobile app;
without that, the connect fails with a message saying so. Then:

```sh
go run ./recipes/connect fitbit
```

A new connection is backfilled over the last 30 days, then pulled every 3 hours (every 6 while
webhook notifications arrive). Wait until `pending_jobs` is 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

Webhooks are optional: Google's subscriber belongs to your Cloud project, so you create it once
yourself ([webhooks](../connectors/fitbit.md#6-webhooks)). Without it the scheduled pull still
collects everything.

## 4. Export

Everything at once, one file per list:

```sh
go run ./recipes/export-all                    # CSV, into health-export-<date>/
go run ./recipes/export-all -format parquet    # Parquet, for DuckDB, Polars or pandas
```

Or one list at a time over the API, for example per-minute steps:

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/samples?type=step_count_intraday&format=csv&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

| File | Fitbit data in it |
|---|---|
| `samples` | `step_count_intraday`, `distance_walking_running_intraday`, `active_energy_burned_intraday`, `flights_climbed` (per minute); `heart_rate`, `resting_heart_rate`, `hrv_rmssd`, `oxygen_saturation`, `respiratory_rate`, `body_mass`, `body_fat_percentage`, `height`, `vo2_max`, `blood_glucose`, `body_temperature`, `skin_temperature` |
| `sleep` | each night with deep, REM, light and awake minutes |
| `workouts` | activity, duration, energy, distance, elevation, average speed and heart rate |
| `daily` | one summary row per calendar day |

Every row has a `provider` column: keep the rows where it is `fitbit`. `view=all` keeps every
source's records; the default merged view keeps one source per figure
([why](../api.md#several-sources-of-one-metric-the-merged-view)). Add `start` and `end`
(`YYYY-MM-DD`) for a window. Columns and types are in [CSV and Parquet exports](../exports.md).

```sh
duckdb -c "SELECT start_date::date AS day, sum(value) AS steps
           FROM 'health-export-2026-11-21/samples.parquet'
           WHERE provider = 'fitbit' AND type = 'step_count_intraday'
           GROUP BY day ORDER BY day DESC LIMIT 14"
```

## What to know about the data

- **Per-minute activity.** Steps, distance, floors and active energy come per minute under the
  `*_intraday` names; Google's daily roll-ups are not pulled.
- **HRV is RMSSD**, stored as `hrv_rmssd`.
- **Relayed data.** If you linked Apple Health or Health Connect in the Google Health app, their
  points come back through this API too. They keep provider `fitbit` but get source `apple_health`
  or `health_connect`, so they never add up with the tracker's minutes. Rows stored before that
  change can be fixed with [Repair Fitbit rows of relayed data](fitbit-relayed-points.md).
- **Not pulled:** nutrition, hydration, ECG, active zone minutes, menstrual and mood data,
  exercise routes. Data deleted at Google stays stored.

The full mapping is in the [Fitbit connector guide](../connectors/fitbit.md).

## Related

- [Send Health Connect data to your server](health-connect-to-server.md)
- [Merge several wearables into one timeline](merge-multiple-wearables.md)
- [Export WHOOP data to CSV or Parquet](whoop-export.md)
- [Export Withings weight and blood pressure to CSV or SQL](withings-export.md)
- [CSV and Parquet exports](../exports.md)
- [README](../../README.md)
