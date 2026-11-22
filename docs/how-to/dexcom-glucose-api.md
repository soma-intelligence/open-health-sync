# Get Dexcom CGM glucose readings through an API

To get Dexcom CGM glucose readings through an API, connect a Dexcom account to Open Health Sync
with OAuth: it pulls the readings from the official Dexcom API v3 into a database you run, and
serves them over a REST API, as CSV or Parquet, or as FHIR R4 Observations. Open Health Sync is
open source and self-hosted, and you register your own Dexcom developer app.

Each reading is one `blood_glucose` sample in mg/dL, with Dexcom's trend arrow and rate kept in
its metadata. Readings from the Dexcom mobile apps reach Dexcom's API 1 hour late in the US and
3 hours late elsewhere, so the newest hours of any pull are always empty.

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

## 2. Register a Dexcom app and pick a region

1. Create an account at [developer.dexcom.com](https://developer.dexcom.com) and create an app.
2. Set its redirect URI to `<OHS_PUBLIC_URL>/oauth/dexcom/callback` (the address `ohs doctor`
   prints; it must match exactly), and copy the client ID and secret.
3. Add them to `.env` and restart:

   ```sh
   OHS_DEXCOM_CLIENT_ID=...
   OHS_DEXCOM_CLIENT_SECRET=...
   OHS_DEXCOM_REGION=sandbox    # us (default), eu, jp or sandbox
   ```

| `OHS_DEXCOM_REGION` | API host | Accounts |
|---|---|---|
| `us` (default) | `api.dexcom.com` | United States |
| `eu` | `api.dexcom.eu` | every country outside the US except Japan |
| `jp` | `api.dexcom.jp` | Japan |
| `sandbox` | `sandbox-api.dexcom.com` | Dexcom's test users, with invented data and no device |

An instance serves one region: an account from another region cannot connect to it. An unknown
value stops startup.

**Sandbox first, production after approval.** A new Dexcom app works against the sandbox only.
Reading real people's data in production needs Dexcom's approval of your app. Start with
`sandbox`, check the flow end to end, then switch the region once Dexcom has approved the app.

## 3. Connect the account

```sh
go run ./recipes/connect dexcom
```

This opens Dexcom's consent page (with a sandbox app, sign in as one of Dexcom's test users).
Dexcom has one scope, `offline_access`. A new connection is backfilled over the last 90 days, in
windows of at most 30 days. Wait until `pending_jobs` is 0:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" | jq .pending_jobs
```

After that, readings arrive with scheduled pulls (Dexcom sends no notifications): every hour,
each pull re-reading the last 6 hours, and the first pull of each UTC day the last 2 days, so
late uploads are stored once they reach Dexcom's API
([sync policy](../background-sync.md#sync-policy-per-provider)). Readings uploaded over USB are
available at once.

## 4. Read the readings

As JSON, page by page:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/samples?type=blood_glucose" \
  -H "Authorization: Bearer $OHS_KEY" | jq '.data[:3]'
```

Each sample has its time (`systemTime`, in UTC), `value` and `unit` (`mg/dL`; a value sent in
mmol/L is converted), and in `metadata`:

| Key | Holds |
|---|---|
| `trend`, `trend_rate`, `trend_rate_unit` | Dexcom's trend arrow and rate of change |
| `specimen` | `interstitial_fluid` |
| `out_of_range` | `low` or `high` when Dexcom clamped the value (`39` means under 40 mg/dL, `401` over 400) |
| `utc_offset_minutes`, `timezone` | the offset of the reading's display time |

`device_model` is the transmitter generation (`g6`, `g7`), and `external_id` Dexcom's `recordId`.

## 5. Export as CSV

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/exports/samples?type=blood_glucose&format=csv&view=all" \
  -H "Authorization: Bearer $OHS_KEY"
```

`format=parquet` gives a file for DuckDB, Polars or pandas; `start` and `end` (`YYYY-MM-DD`)
a window. Keep the rows whose `provider` is `dexcom` if other sources also write glucose.
Columns are described in [CSV and Parquet exports](../exports.md). `go run ./recipes/export-all`
writes every list at once.

## 6. Get them as FHIR

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/fhir/Observation?type=blood_glucose&limit=5" \
  -H "Authorization: Bearer $OHS_KEY"
```

A CGM measures the fluid under the skin, so these Observations are coded with LOINC `99504-3`
(glucose in interstitial fluid) instead of `2339-0` (glucose in blood), the code the HL7 CGM
implementation guide uses. A `code` search for `2339-0` finds both kinds; a search for `99504-3`
finds nothing yet, so search by `type` ([details](../fhir.md#glucose-from-a-cgm)).

## What is not pulled yet

Dexcom events (carbs, insulin, exercise), calibrations and device records stay at Dexcom. Each
Dexcom refresh token works once and is replaced on every refresh; if Dexcom refuses one, or it
goes unused for a year, the connection turns `needs_reauth` and the account has to connect again.
The full mapping is in the [Dexcom connector guide](../connectors/dexcom.md).

## Related

- [Share your data with your clinician](share-with-clinician.md)
- [Merge several wearables into one timeline](merge-multiple-wearables.md)
- [Get notified of new data with webhooks](webhooks-new-data.md)
- [Get Oura data as FHIR R4](oura-fhir.md)
- [FHIR R4 output](../fhir.md)
- [README](../../README.md)
