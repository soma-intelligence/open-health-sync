# Dexcom connector

Slug `dexcom`. Users connect their own Dexcom account over OAuth; Open Health Sync pulls their
continuous glucose monitor (CGM) readings from the official
[Dexcom API v3](https://developer.dexcom.com/docs/dexcomv3/endpoint-overview) on a schedule.
You register your own Dexcom developer app: nothing is shared between instances.

Status: available. It is tested against a fake of the
API built from the published documentation. The points marked *unverified* below are the ones
to check first, ideally against the Dexcom sandbox.

## 1. Get your keys

1. Create an account at [developer.dexcom.com](https://developer.dexcom.com) and create an app.
2. Set the **redirect URI** to `<OHS_PUBLIC_URL>/oauth/dexcom/callback`, the address
   `ohs doctor` prints. It must match exactly.
3. Copy the **client ID** and the **client secret**.

A new app works against the **sandbox** (`sandbox-api.dexcom.com`), whose test users have
invented data and need no device. Using real users' data in production needs Dexcom's approval
of the app.

## 2. Configure the instance

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_DEXCOM_CLIENT_ID=...
OHS_DEXCOM_CLIENT_SECRET=...
# Optional: us (default), eu, jp or sandbox.
OHS_DEXCOM_REGION=sandbox
```

| `OHS_DEXCOM_REGION` | API host | Users |
|---|---|---|
| `us` (default) | `api.dexcom.com` | United States |
| `eu` | `api.dexcom.eu` | Every country outside the US except Japan |
| `jp` | `api.dexcom.jp` | Japan |
| `sandbox` | `sandbox-api.dexcom.com` | Dexcom's test users |

An instance serves one region: a user whose account is in another region cannot connect to it.
An unknown value stops startup. *Unverified:* the login and token paths
(`/v3/oauth2/login`, `/v3/oauth2/token`) are documented for the US and sandbox hosts; the
connector assumes the EU and Japan hosts use the same paths.

## 3. Scopes and tokens

Dexcom has one scope, `offline_access`. Access tokens last 2 hours. Each refresh token works
**once**: a refresh returns a new one, and Open Health Sync stores it in place of the old one. A
refresh token Dexcom refuses (`invalid_grant`), or one unused for a year, means the user has to
connect again; the connection is then marked for reauthorization.

## 4. Connect a user

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/dexcom \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`. The authorization code Dexcom returns is valid
for one minute, so the callback exchanges it at once.

## 5. What is pulled

| Endpoint | Raw data type | Canonical records |
|---|---|---|
| `GET /v3/users/self/egvs` | `egvs` | `blood_glucose` quantity samples (mg/dL) |

Pulls run in windows of at most 30 days (the API refuses longer ones) and stop at the current
time. There are no notifications: data arrives with the scheduled pulls. Readings uploaded by
the Dexcom mobile apps reach the API **1 hour late in the US and 3 hours late elsewhere**, so the
latest hours of a pull are always empty. Readings uploaded over USB are available at once.
The API allows 60,000 calls per app per hour; a 429 puts the job back until `Retry-After`.

Not pulled yet: events (carbs, insulin, exercise), calibrations, devices and alerts.

## 6. Mapping

Each estimated glucose value (EGV) becomes one `blood_glucose` sample:

- **Time**: `systemTime`, in UTC. A record from a receiver carries no offset and is read as UTC.
- **Value and unit**: `value` in `unit` (`mg/dL`; a value sent in `mmol/L` is converted to
  mg/dL by the pipeline). Dexcom clamps the ends of its range: `39` means below 40 mg/dL and
  `401` above 400, with `status` `low` or `high`. The clamped value is stored, and
  `metadata.out_of_range` keeps the status.
- **Specimen**: `metadata.specimen` is `interstitial_fluid`. A CGM measures the fluid under the
  skin, not blood, so the [FHIR output](../fhir.md) codes these Observations with LOINC
  `99504-3` (glucose in interstitial fluid) instead of `2339-0` (glucose in blood).
- **Time zone**: from the offset of `displayTime`, or, when it has none, from the gap between
  `displayTime` and `systemTime` rounded to 15 minutes; stored as `metadata.utc_offset_minutes`
  and, for whole hours, `metadata.timezone`.
- **Trend**: `metadata.trend`, `metadata.trend_rate` and its unit.
- **Device**: `device_id` is the hashed transmitter id Dexcom returns, `device_model` the
  transmitter generation (`g6`, `g7`, ...).
- **Identity**: `external_id` is the `recordId`.

A record with a null `value` gives nothing.

## 7. Testing with real data

The unit tests use a fake of the API (`connectors/dexcom/dexcom_test.go`). To check against
Dexcom, set `OHS_DEXCOM_REGION=sandbox`, connect a sandbox user and compare
`GET /v1/users/{id}/samples?type=blood_glucose` with the sandbox data.
