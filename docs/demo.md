# Try it without a wearable

Connecting a real provider takes developer keys, an OAuth app and a device on someone's wrist.
To see what the API returns before any of that, fill an instance with synthetic data:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs seed --demo
```

Outside Docker, `ohs seed --demo` needs `OHS_DATABASE_URL` only, like `ohs admin`.

The command creates a tenant named `demo` with one API key, printed once, and a user with
`external_ref` `demo-user`. It writes 90 days of data for that user, ending yesterday, and prints
the ids and a few calls to try:

```
tenant  6f0c...
user    2b9e...
API key, shown only this once, store it now:
ohs_sk_...

Try it:
  export OHS_URL=http://localhost:8080 OHS_API_KEY=ohs_sk_...
  curl -s -H "Authorization: Bearer $OHS_API_KEY" "$OHS_URL/v1/users/2b9e.../daily"
  curl -s -H "Authorization: Bearer $OHS_API_KEY" "$OHS_URL/v1/users/2b9e.../sleep"
  curl -s -H "Authorization: Bearer $OHS_API_KEY" "$OHS_URL/v1/users/2b9e.../fhir/Observation?code=http://loinc.org%7C8867-4&limit=5"
```

Set `OHS_URL` before the command to print another base URL in the examples.

## What it writes

One invented person, living in `Europe/Paris`, seen through six sources that overlap the way
real devices do:

| Source | Device it plays | Data |
|---|---|---|
| `oura` | a ring, worn at night | sleep with stages, night heart rate, resting heart rate, HRV (RMSSD), breathing rate, temperature deviation, a daily summary with readiness, sleep and activity scores |
| `whoop` | a strap, worn day and night | heart rate every 10 minutes, sleep with stages and SpO2, HRV, resting heart rate, recovery and strain, the workouts it detects |
| `withings` | a scale and a blood pressure cuff | weight, body fat, lean mass and BMI most mornings; blood pressure and heart rate on Sundays |
| `fitbit` | a band, worn in the day | hourly steps, distance, active energy and floors; heart rate every 15 minutes; daily resting heart rate; the night's SpO2 |
| `polar` | a sports watch | runs, rides, walks and strength sessions with heart rate, energy, distance, a GPS route and a VO2 max estimate after runs |
| `apple_health` | a phone in a pocket | hourly steps, distance and floors (it stays home during runs), a weight typed in by hand on Sundays |

So heart rate comes from five sources, sleep from two, steps from two (three with the ring's
daily total), weight and workouts from two each, and the same night or run appears once per
source, a few minutes apart, as it would from real devices. About 35,000 quantity samples in all.

The values are plausible but made up: resting heart rate around 50 to 60 bpm, 7 to 8 hours of
sleep with the usual deep, REM and light pattern, 7,000 to 12,000 steps on most days, a weight
that drifts slowly, workouts on a weekly rhythm, a slow fitness cycle that moves HRV and resting
heart rate together. Every sample, sleep session, workout and provider daily summary carries
the metadata `{"synthetic": true}` (the derived daily rows carry `"derived": true`, as always),
and the external id of every sleep and workout starts with `demo-`, so demo rows are easy to
tell apart from real ones. None of the six connectors needs to be configured: the data is stored
and read like any other.

## How it is written

The records go through the same path as data from a provider: units, validation against the
plausible ranges, content hash deduplication, the daily summaries derived from steps, distance
and energy, coverage, and the `data.written` [webhook](webhooks.md) events of the tenant. Only the
raw store is skipped, because there is no provider payload to keep.

The data is deterministic: the same `--seed` gives the same values for a given date. Running the
command again adds nothing. Run on a later day, it adds the new days and leaves the others.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--demo` | required | Confirms that synthetic data is wanted. Without it the command refuses to run. |
| `--tenant <id>` | the `demo` tenant | Write into an existing tenant instead. No key is created or printed. |
| `--days <n>` | 90 | Days of data, ending yesterday, at most 365. |
| `--external-ref <ref>` | `demo-user` | The user to create, or to reuse when the tenant already has one with this ref. |
| `--seed <n>` | 1 | Another seed gives another person and other data. |

The key of the `demo` tenant is printed on the first run only. To get another one later:

```sh
ohs admin key create --tenant <tenant id>
```

## From the admin API

An instance admin key does the same over HTTP for any tenant, without a shell on the server:
`POST /v1/admin/tenants/{tenant_id}/demo-user` with `{"external_ref": "demo-user", "days": 30}`
(both optional, `days` at most 90) writes the same data through the same code, for the seed's
default person, and answers the user with `rows_written`. See
[admin-api.md](admin-api.md#demo-user).

## Things to try

With `OHS_URL`, `OHS_API_KEY` and `USER_ID` set from the output:

```sh
H="Authorization: Bearer $OHS_API_KEY"
# Heart rate from every source, and one source at a time
curl -s -H "$H" "$OHS_URL/v1/users/$USER_ID/samples?type=heart_rate&limit=5"
# Sleep sessions, then the stages of one night
curl -s -H "$H" "$OHS_URL/v1/users/$USER_ID/sleep?limit=2"
curl -s -H "$H" "$OHS_URL/v1/users/$USER_ID/sleep/<session id>/phases"
# Workouts and the GPS route of one
curl -s -H "$H" "$OHS_URL/v1/users/$USER_ID/workouts?limit=3"
curl -s -H "$H" "$OHS_URL/v1/users/$USER_ID/workouts/<workout id>/route"
# Daily summaries: provider scores and the totals derived from samples
curl -s -H "$H" "$OHS_URL/v1/users/$USER_ID/daily?limit=7"
# Open mHealth and a CSV export
curl -s -H "$H" "$OHS_URL/v1/users/$USER_ID/omh/data-points?schema=omh:heart-rate&limit=1"
curl -sOJ -H "$H" "$OHS_URL/v1/users/$USER_ID/exports/samples?type=step_count&format=csv"
```

For an agent, point `ohs mcp` at the instance with `OHS_MCP_KEY=$OHS_API_KEY` (see
[mcp.md](mcp.md)) and ask about the demo user's sleep or training. The routes are described in
[api.md](api.md); FHIR in [fhir.md](fhir.md), Open mHealth in [open-mhealth.md](open-mhealth.md),
exports in [exports.md](exports.md).

## Removing it

The demo user is an ordinary user: `DELETE /v1/users/{id}` erases it and its data. On a
development instance you can also leave the `demo` tenant in place; its rows never mix with
another tenant's.
