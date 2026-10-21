# Export WHOOP data to CSV or Parquet

The WHOOP app can email you a one-off CSV export of your sleep, recovery and strain, organized
by physiological cycle (menu, *Data Export*, per
[WHOOP's article](https://www.whoop.com/thelocker/access-your-whoop-data-with-new-integrations-data-export-options/)).
When you want more than a one-off file, Open Health Sync pulls your WHOOP data through the WHOOP
API and keeps it in a database you run, so you can export it again at any time as CSV or
Parquet, by calendar day, in the same columns as your other devices, or read it over an API or
in SQL.

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

No WHOOP keys yet? `ohs seed --demo` writes 90 days of synthetic data that includes a WHOOP strap
(heart rate, sleep with stages, HRV, recovery and strain, workouts), so you can try the exports
below first ([demo data](../demo.md)).

## 2. Connect your WHOOP account

WHOOP data reaches the API through a WHOOP developer app that you register yourself:

1. In the WHOOP Developer Dashboard (`developer-dashboard.whoop.com`), create an app with the
   scopes the [WHOOP connector guide](../connectors/whoop.md#3-scopes) lists, and the redirect
   URL `<OHS_PUBLIC_URL>/oauth/whoop/callback`.
2. Add its keys to `.env` and restart:

   ```sh
   OHS_WHOOP_CLIENT_ID=...
   OHS_WHOOP_CLIENT_SECRET=...
   ```

3. Open WHOOP's consent page and allow access:

   ```sh
   go run ./recipes/connect whoop
   ```

The first data arrives a few seconds later, and the history follows in the background. Check
that it is done when `pending_jobs` reaches 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 3. Export

Everything at once, one file per list:

```sh
go run ./recipes/export-all                    # CSV, into health-export-<date>/
go run ./recipes/export-all -format parquet    # Parquet, for DuckDB, Polars or pandas
```

Or one list at a time over the API. The daily summaries hold WHOOP's recovery and strain scores
and the resting heart rate, one row per calendar day:

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/daily?format=csv&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

| File | WHOOP data in it |
|---|---|
| `daily` | `recovery_score`, `strain_score`, `resting_hr`, `total_kcal`, per day |
| `sleep` | each sleep and nap: stage durations (slow wave sleep is `deep`), sleep performance as `sleep_score`, respiratory rate |
| `workouts` | activity, energy, distance, average and maximum heart rate, elevation gain |
| `samples` | `hrv_rmssd`, `resting_heart_rate`, `oxygen_saturation`, `skin_temperature`, `strain`, `workout_strain`, `body_mass` |

Every row has a `provider` column: with other devices connected too, keep the rows where it is
`whoop`. `view=all` keeps every source's records; the default merged view keeps one source per
figure when several measured the same thing
([why](../api.md#several-sources-of-one-metric-the-merged-view)). Add `start` and `end`
(`YYYY-MM-DD`) for a window, and `type=hrv_rmssd` to the `samples` export for one measurement.
Columns and types are described in [CSV and Parquet exports](../exports.md).

```sh
duckdb -c "SELECT date, recovery_score, strain_score, resting_hr
           FROM 'health-export-2026-10-18/daily.parquet'
           WHERE provider = 'whoop' ORDER BY date DESC LIMIT 14"
```

## What the WHOOP API does not give

WHOOP's public API serves no raw heart rate series, no hypnogram and no ECG, so neither the API
nor this export has them. Its recovery, strain and sleep performance scores come as WHOOP
computes them.

## Next

- Keep it running: new data arrives on WHOOP's webhooks and scheduled pulls, and the same
  export picks it up next time.
- Add your other devices: every [connector](../connectors/README.md) exports in the same columns.
- [Get the data as FHIR R4](../fhir.md) for a clinical or research system.
