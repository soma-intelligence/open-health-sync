# Export Oura data to CSV or Parquet

Open Health Sync pulls your Oura data into a database you run and exports it as CSV for a
spreadsheet or Parquet for a notebook. You can try the export with synthetic ring data before
connecting an Oura account.

## 1. Start an instance and try the demo

Follow the [quickstart](../../README.md#quickstart) from the repository root. It starts the
instance and runs `ohs seed --demo` once. The seed prints a user ID, an API key and a shell
`export` line. Paste that line, then set the variables the export recipe uses:

```sh
export OHS_KEY="$OHS_API_KEY"
export OHS_USER="<user ID printed by the seed>"
```

Replace the placeholder with the printed ID. The key is shown only on the first seed; see
[demo data](../demo.md#flags) if you need another. The default demo includes an Oura ring and
five other sources, with 90 days of invented data. No Oura keys are needed for this step.

## 2. Connect your own ring

To export your own data, create your own user and key (instead of using the demo credentials):

```sh
umask 077
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
set -a; . ~/.ohs.env; set +a
```

In the Oura developer portal (`cloud.ouraring.com`, under API Applications), register an
application with redirect URI `<OHS_PUBLIC_URL>/oauth/oura/callback`. Add its client ID and
secret to `.env`, then restart the instance:

```sh
OHS_OURA_CLIENT_ID=...
OHS_OURA_CLIENT_SECRET=...
```

The connector requests the `personal`, `daily`, `heartrate`, `workout`, `session`, and
`spo2Daily` scopes. See the [Oura connector guide](../connectors/oura.md#3-scopes) for what each
scope permits and the [setup steps](../connectors/oura.md#2-configure-the-instance) for the
callback and optional webhooks. Gen3 and later rings need an active Oura Membership for API
access. Start consent from the repository root:

```sh
go run ./recipes/connect oura
```

The first pull runs in the background. To check its progress:

```sh
curl -fsS "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 3. Export

Run the recipe from the repository root. It downloads all sources, one file per list, into a
dated directory:

```sh
go run ./recipes/export-all
go run ./recipes/export-all -format parquet
```

The first command writes CSV; the second writes Parquet. To download just the quantity samples
as CSV instead:

```sh
curl -fsS -o samples.csv "$OHS_URL/v1/users/$OHS_USER/exports/samples?format=csv&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

| File | Oura records |
|---|---|
| `samples` | one row per measurement, with `type`, `value` and `unit`: `readiness_score`, `readiness_hrv_balance`, `sleep_score`, `body_temperature_deviation`, `heart_rate`, `resting_heart_rate`, `oxygen_saturation`, `step_count` and more |
| `sleep` | sleep periods with stage durations, `hrv_avg_ms`, `resting_hr` and `respiratory_rate` when present |
| `workouts` | workouts and their canonical activity names |
| `category-samples` | `mindful_session` records for meditation, breathing or relaxation |
| `daily` | derived daily summaries where available; the demo also creates Oura daily score rows |

The real Oura connector stores readiness and daily sleep scores as **samples**, not as sleep
session scores or provider daily summary rows. The demo generates additional daily summaries
and a session sleep score for illustration, so its rows do not exactly mirror the connector's
mapping. A value Oura did not provide is left empty. See
[Oura data produced](../connectors/oura.md#data-produced) for the complete mapping, including
units and metadata.

Every row has a `provider` column. The recipe uses `view=all` to keep each source's records;
filter `provider` to `oura` in your spreadsheet or analysis tool. The default merged view can
choose another source for a figure. Add `start` and `end` (`YYYY-MM-DD`, end exclusive) to an
export URL to select a window, or `type=readiness_score` to the samples URL to select one
measurement. See [CSV and Parquet exports](../exports.md) for the schemas and parameters.

For example, with DuckDB installed, query the Parquet samples from today's export:

```sh
duckdb -c "SELECT start_date, type, value, unit FROM 'health-export-$(date +%F)/samples.parquet' WHERE provider = 'oura' AND type IN ('readiness_score', 'sleep_score', 'body_temperature_deviation') ORDER BY start_date DESC LIMIT 14"
```

In the demo this query can return temperature deviation; readiness and sleep scores are in its
`daily` file rather than those sample types. With your own Oura data, the available rows depend
on the scopes granted and the data Oura returned.

## Next

- Run the same export again after new data is pulled.
- [Get Oura data as FHIR R4](oura-fhir.md), or add other [connectors](../connectors/README.md).
