# Get Oura data as FHIR R4

The Oura API returns Oura's own JSON. A clinical system, an EHR integration or a research
pipeline usually wants FHIR instead. Open Health Sync pulls a person's Oura data and serves it
as FHIR R4 Observations: heart rate, SpO2, HRV and the other measurements with UCUM units and
LOINC codes where one applies, each night as a sleep session, and each day's readiness, sleep
and activity scores as a daily summary. The output is checked with the official HL7 validator
in CI.

You can try the FHIR output in five minutes with synthetic Oura data, then connect a real ring.

## 1. Start an instance

Follow the [quickstart](../../README.md#quickstart) to *Start the stack*: clone the repository,
create `.env` with a fresh encryption key, and start it with Docker Compose.

## 2. Try it with synthetic Oura data

`ohs seed --demo` writes 90 days of invented data from six sources, Oura among them (sleep with
stages, night heart rate, HRV, breathing rate, and a daily summary with readiness, sleep and
activity scores), and prints a demo API key and user id:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs seed --demo
export OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... OHS_USER=...   # from its output
```

Read the sleep sessions as FHIR (LOINC 93832-4):

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/fhir/Observation?code=http://loinc.org%7C93832-4&view=all&limit=3" \
  -H "Authorization: Bearer $OHS_KEY" | jq '.entry[].resource | {id, effectivePeriod, source: .meta.source}'
```

Each entry is an Observation whose `meta.source` ends in `/fhir/provider/oura` for the ring's
nights, with the stages and the sleep score as components. `view=all` keeps every source's
record; without it the server returns the merged view, where a better ranked source of the same
night replaces the ring's ([why](../api.md#several-sources-of-one-metric-the-merged-view)).

The daily summaries, with the readiness score:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/fhir/Observation?code=https://github.com/soma-intelligence/open-health-sync/fhir/CodeSystem/record-type%7Cdaily_summary&view=all&limit=3" \
  -H "Authorization: Bearer $OHS_KEY"
```

And the person as a Patient, with every Observation, Device and Provenance in pages:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/fhir/Patient/$OHS_USER/\$everything?limit=50" \
  -H "Authorization: Bearer $OHS_KEY"
```

Follow the `next` link of each Bundle until there is none. The full mapping, the search
parameters and example resources are in [FHIR R4](../fhir.md).

## 3. Connect a real Oura ring

Oura data reaches the API through an Oura API application that you register yourself, in a few
minutes:

1. In the Oura developer portal, create an application. Set its redirect URI to
   `<OHS_PUBLIC_URL>/oauth/oura/callback` and copy the client ID and secret.
2. Add them to `.env` and restart:

   ```sh
   OHS_OURA_CLIENT_ID=...
   OHS_OURA_CLIENT_SECRET=...
   ```

3. Create your own user, then open Oura's consent page:

   ```sh
   docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
     /ohs admin personal setup > ~/.ohs.env
   chmod 600 ~/.ohs.env
   go run ./recipes/connect oura
   ```

The first data arrives a few seconds after you allow access, and the history follows in the
background. Rings from Gen3 on need an active Oura Membership for the API to serve their data,
and a new Oura application works for up to 10 people until Oura approves it. The
[Oura connector guide](../connectors/oura.md) covers the scopes, webhooks and errors.

Then read your own data with the same calls, using `OHS_URL`, `OHS_KEY` and `OHS_USER` from
`~/.ohs.env` (`set -a; . ~/.ohs.env; set +a`). With only the ring connected, every Observation
comes from Oura.

## What maps to what

| Oura | FHIR R4 Observation |
|---|---|
| Sleep periods, with stages | A sleep session: LOINC 93832-4, stage durations and the sleep score as components |
| Heart rate series | Quantity samples, LOINC 8867-4, unit `/min` |
| SpO2, breathing rate | Quantity samples, LOINC 59408-5 and 9279-1 |
| HRV (RMSSD) | Quantity samples in `ms`, coded with the `hrv_rmssd` type (no LOINC code) |
| Daily readiness, sleep and activity scores | A daily summary, one per local day, with each score as a component |
| Workouts | A workout, LOINC 55411-3 |

Records also come as Open mHealth data points ([Open mHealth](../open-mhealth.md)) and in
the REST JSON of the [API](../api.md), from the same stored rows.

## Next

- Add other sources: the same FHIR routes serve every [connector](../connectors/README.md).
- [Get your data as CSV or Parquet](../exports.md) for analysis.
- [Hand a person all their data](../exports.md#complete-user-archive-data-portability) in one
  zip, FHIR Bundle included.
