# Export Strava activities to PostgreSQL or CSV

To export Strava activities to PostgreSQL or CSV, connect your Strava account to Open Health
Sync with OAuth: it pulls each activity's summary (sport, start, duration, distance, speed,
elevation, heart rate, energy) into a PostgreSQL database you run, as one workout row, and
exports them as CSV or Parquet at any time. Open Health Sync is open source and self-hosted, and
you register your own Strava API application.

The connector reads summary figures only. Streams (heart rate, power and GPS series), laps and
routes are not pulled yet; for those, import the original files of your Strava archive
(step 5).

## 1. Read Strava's terms first

- **Athlete limit.** A new Strava API application can be connected by 1 athlete, its owner. You
  can raise it to 10 yourself in the API settings; beyond that, Strava reviews the application.
- **API agreement.** Strava's 2026 API terms forbid using API data to train AI models and
  restrict apps that pass Strava data on to third parties as an intermediary platform. Read the
  current [API agreement](https://www.strava.com/legal/api) and decide whether your use fits it.
- **Rate limits.** By default 100 read requests per 15 minutes and 1,000 per day, shared by every
  connected athlete. That is why only summaries are pulled (one request per 200 activities).

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

## 3. Connect your Strava account

1. At [strava.com/settings/api](https://www.strava.com/settings/api), create an application. Set
   the **Authorization Callback Domain** to the host of `OHS_PUBLIC_URL` alone (for
   `https://ohs.example.org`, enter `ohs.example.org`; `localhost` works for local testing).
2. Add its keys to `.env` and restart:

   ```sh
   OHS_STRAVA_CLIENT_ID=...
   OHS_STRAVA_CLIENT_SECRET=...
   ```

3. Open Strava's consent page and allow access. Leave `activity:read_all` ticked: without an
   activity scope the pulls come back empty.

   ```sh
   go run ./recipes/connect strava
   ```

A new connection is backfilled over the last 30 days, then pulled every 3 hours (every 12 once
Strava's webhook events arrive, see [webhook events](../connectors/strava.md#5-webhook-events)).
Wait until `pending_jobs` is 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

## 4. Query it in PostgreSQL

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec postgres \
  psql -U ohs -d ohs -v user="$OHS_USER"
```

```sql
SELECT start_date, activity_type, duration_s / 60 AS minutes,
       round((distance_m / 1000)::numeric, 2) AS km, elevation_gain_m, avg_heart_rate,
       metadata->>'strava_sport_type' AS strava_sport
FROM ohs_data.workouts
WHERE user_id = :'user' AND provider = 'strava'
ORDER BY start_date DESC
LIMIT 10;
```

| Column | From Strava |
|---|---|
| `activity_type` | `sport_type`, mapped (`Run`, `TrailRun` to `running`; `Ride`, `GravelRide` to `cycling`...); an unknown sport is `other` |
| `duration_s` | `moving_time` (`elapsed_time` when missing) |
| `distance_m`, `avg_speed_mps`, `elevation_gain_m` | `distance`, `average_speed`, `total_elevation_gain` |
| `avg_heart_rate`, `max_heart_rate` | when recorded |
| `active_energy_kcal` | `calories`, or `kilojoules` one to one; `metadata.active_energy_source` says which |

## 5. Export to CSV or Parquet

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/workouts?format=csv&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

`format=parquet` gives a file for DuckDB, Polars or pandas, and `start` and `end`
(`YYYY-MM-DD`) a window; `go run ./recipes/export-all` writes every list at once. Keep the rows
whose `provider` is `strava`. Columns are described in [CSV and Parquet exports](../exports.md).

For the full streams, request your Strava archive (**Settings**, **My Account**, **Download or
Delete Your Account**) and import the original activity files it holds, with the `ohs` command
from the [latest release](https://github.com/soma-intelligence/open-health-sync/releases) and a
token that can only upload:

```sh
export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" -d '{"scopes":["upload"]}' | jq -r .token)
ohs import files ~/Downloads/strava-archive/
```

See [activity files](../connectors/activity-files.md#2-import-them). By default, a file's copy of
an activity, which has the streams, wins over the Strava API's copy in the merged view.

## What the export does not give

Activity names and descriptions (free text), detailed activities, streams, laps and routes are
not collected through the API. Deleted activities are removed only when the webhook subscription
id is configured. An activity edited more than 7 days after it started is picked up only if a
pull window covers its start. The full mapping is in the
[Strava connector guide](../connectors/strava.md).

## Related

- [Import FIT, GPX and TCX files](fit-gpx-tcx-import.md)
- [Export Garmin Connect activities to PostgreSQL or CSV](garmin-export.md)
- [Export Polar data to CSV or PostgreSQL](polar-export.md)
- [Merge several wearables into one timeline](merge-multiple-wearables.md)
- [CSV and Parquet exports](../exports.md)
- [README](../../README.md)
