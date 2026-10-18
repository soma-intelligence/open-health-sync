# Eight Sleep connector

Slug `eight_sleep`. Users connect their own Eight Sleep account (Pod 3, Pod 4, Pod 5) with the
email and password they use in the Eight Sleep app. Open Health Sync pulls their nightly sleep
data on a schedule: sleep stages, heart rate, HRV, breathing rate, and bed and room temperature.

Status: available on `main`, next release.

**Eight Sleep has no public API and no partner program.** This connector signs in the way the
Eight Sleep app does and reads the same API the app reads. Nothing about it is documented or
approved by Eight Sleep: it can stop working whenever Eight Sleep changes its app, and Eight
Sleep's terms of service may not allow access by other software. Read them before you turn the
connector on, and tell your users what it does.

The way in comes from [pyEight](https://github.com/mezz64/pyEight) and its maintained fork in
[lukas-clarke/eight_sleep](https://github.com/lukas-clarke/eight_sleep), open source Python
libraries under the MIT license. The connector is a Go re-implementation of their approach; no
code is copied from them. Tests run against a fake of the Eight Sleep API built from the payload
shapes those libraries read. Statements they leave open are marked **(to confirm)**. Checked on
**2026-10-18**.

## How it differs from the other connectors

Every other pulled source uses OAuth: the user signs in on the provider's site and Open Health
Sync gets a token, never a password. Eight Sleep offers no such sign-in for other apps, so:

- **The user types their Eight Sleep password into a page served by your instance**
  (`/oauth/eight_sleep/signin`), not into Eight Sleep's site. Your app never sees it.
- **Your instance keeps the password.** Eight Sleep's access token expires (the sign-in answer
  says when) and the community libraries use no refresh token, so the server signs in again
  with the password. It is stored with
  the connection in the refresh token column, sealed with AES-256-GCM under `OHS_ENCRYPTION_KEY`
  and bound to the row, like every OAuth token (see the [security model](../security-model.md)).
  It is never logged and never returned by the API.
- **Disconnecting wipes it**, with the access token, as for any connection. If the user changes
  their Eight Sleep password, the next sign-in fails and the connection goes to `needs_reauth`:
  they connect again with the new one.
- Accounts that sign in to Eight Sleep with Google or Apple and have no password cannot connect
  **(to confirm)**: set a password in the Eight Sleep app first.

## 1. Client keys

The sign-in needs a client id and secret. Eight Sleep issues none to third parties: they are the
Eight Sleep app's own, and the community libraries above publish the values the app uses
(`KNOWN_CLIENT_ID` and `KNOWN_CLIENT_SECRET` in the fork's `pyEight/constants.py`). Open Health
Sync builds those values in as defaults (`DefaultClientID` and `DefaultClientSecret` in
`connectors/eightsleep/connector.go`), so the connector works with no keys. They belong to Eight
Sleep's app, not to this project: if Eight Sleep changes them, sign-ins fail until the defaults
are updated or you set the new values yourself.

## 2. Configure the instance

Nothing to set beyond `OHS_ENCRYPTION_KEY`, which any pulled source needs. The connector is listed
(`GET /v1/providers`) by default; turn it off in the [console](../console.md) if you do not want
to offer it. To override the built-in keys, set both:

```sh
OHS_EIGHT_SLEEP_CLIENT_ID=...
OHS_EIGHT_SLEEP_CLIENT_SECRET=...
```

Setting only one leaves the connector off. There is no callback URL to register: the sign-in page
lives on your instance, under `OHS_PUBLIC_URL`.

## 3. Connect a user

Your server asks for a connection URL exactly as for an OAuth source (see
[api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/eight_sleep \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

The returned `authorize_url` is your instance's sign-in page
(`$OHS_PUBLIC_URL/oauth/eight_sleep/signin?state=...`). The user enters their Eight Sleep email
and password. A wrong password shows the form again with a message; the link stays valid for its
10 minutes. On success the user lands on your `redirect_uri` with `status=connected`, or on the
built-in result page, and the last 30 days are pulled. The [hosted connection
page](../connect-page.md) works the same way and says on the page that Eight Sleep asks for a
password.

| | |
|---|---|
| Sign-in | `POST https://auth-api.8slp.net/v1/tokens`, JSON body `{"client_id", "client_secret", "grant_type": "password", "username", "password"}` |
| Answer | `{"access_token", "expires_in", "userId"}` |
| Calls | `Authorization: Bearer <access_token>` on `https://client-api.8slp.net/v1` |
| Expired or refused token (401) | The server signs in again with the stored password and retries once |
| Refused password | The connection is marked `needs_reauth` |

## 4. How data arrives

The connector pulls `GET /v1/users/{userId}/trends` with `tz=UTC`, `from` and `to` dates,
`include-main=false`, `include-all-sessions=true` and `model-version=v2` (the parameters pyEight
sends), 14 days per request, paced at one request a second. Each night is one raw line
(`trends` data type), filed under its `day`. Eight Sleep has no notifications: new nights arrive
with the regular pulls. A night still being processed (`processing: true`) is pulled again later
and replaces the stored copy.

**Rate limits.** Eight Sleep publishes none. HTTP 429 backs off for the time its `Retry-After`
header asks (one minute without one), for every Eight Sleep connection of the instance.

**Backfill.** The first pull covers the last 30 days. A wider pull covers at most 90 days.

## Data produced

| Eight Sleep field | Canonical records |
|---|---|
| A session's `stages` (`awake`, `light`, `deep`, `rem`, each with a `duration` in seconds, one after the other from the session's `ts`) | `SleepSession` per session: bed time from `ts` to the end of the last stage, deep, light, REM and awake seconds, total sleep = deep + light + REM. `SleepPhase` rows for each stage. `out` (out of bed) is not a phase and a trailing `out` is not in the night |
| The day's `score` | The session's sleep score (0 to 100) |
| `sleepQualityScore.hrv.current` | The session's HRV average (ms). When the night has no HRV readings, also one `hrv_rmssd` sample over the night (**to confirm** that Eight Sleep's HRV is RMSSD, as its app says) |
| `sleepQualityScore.respiratoryRate.current` | The session's respiratory rate (breaths per minute) |
| `timeseries.heartRate` | `heart_rate` samples (bpm) |
| `timeseries.hrv` | `hrv_rmssd` samples (ms) and the session's HRV average when the day has none (**to confirm** the key) |
| `timeseries.respiratoryRate` | `respiratory_rate` samples (stored unit `bpm`, breaths per minute) |
| `timeseries.tempBedC`, `timeseries.tempRoomC` | No canonical quantity type for bed or room temperature: the session's metadata keeps `bed_temperature_avg_c`, `_min_c`, `_max_c` and the same for `room_temperature`. The readings stay in the raw line |
| `sleepQualityScore.total`, `sleepRoutineScore.total`, `tnt` | Session metadata `sleep_quality_score`, `sleep_routine_score`, `toss_and_turns` |

The day's figures go on its longest session; a nap the same day gets its own session with its
stages and readings only. A session is keyed by its `id`, or by its start when it has none.
Values the API sends as the string `"None"`, and readings outside plausible ranges (a heart rate of
0), are skipped.

Not pulled: the Pod's temperature settings and schedules, alarms, the base, the speaker, and the
sleep fitness sub scores.

**Provenance.** Every record carries `provider: eight_sleep` and the source name `Eight Sleep`, and
the raw night is kept. In FHIR output each Observation has a [Provenance](../fhir.md#provenance)
naming Eight Sleep, like any other source.

## Points to confirm

From community libraries, not from Eight Sleep. If your account shows something else, open an
[issue](https://github.com/soma-intelligence/open-health-sync/issues) with the shape (never the
values):

- The `timeseries` keys beyond `heartRate`, `tempBedC` and `tempRoomC`, in particular `hrv` and
  `respiratoryRate`.
- That Eight Sleep's HRV figure is RMSSD.
- That a session's `stages` follow each other from `ts` with no gaps.
- Whether accounts created with Google or Apple sign-in can use a password.
- The token lifetime, and any rate limit.

## Testing

`go test ./connectors/eightsleep/...` runs the sign-in, the pulls and the mapping against
`connectors/eightsleep/eightsleepfake`, an httptest fake answering from invented nights. The
sign-in page is covered by `internal/collect/signin_integration_test.go`.

## Sources

Checked on 2026-10-18.

- [mezz64/pyEight](https://github.com/mezz64/pyEight) (MIT): the original library
- [lukas-clarke/eight_sleep](https://github.com/lukas-clarke/eight_sleep) (MIT): the maintained
  fork, with the current sign-in (`pyEight/eight.py`) and trends call (`pyEight/user.py`)
