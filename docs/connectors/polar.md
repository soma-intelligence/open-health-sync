# Polar connector

Slug `polar`. Users connect their own Polar Flow account over OAuth; Open Health Sync registers
them with your AccessLink client, pulls their data on a schedule and when Polar sends a webhook
event. You register your own AccessLink client: nothing is shared between instances.

Status: available. Field names follow the
[AccessLink v3 documentation](https://www.polar.com/accesslink-api/); the points marked
*to confirm* below are the ones to check first.

## 1. Get your keys

1. Sign in at `admin.polaraccesslink.com` with a Polar Flow account and accept the AccessLink
   terms.
2. Create a client. Set the **callback URL** (the OAuth redirect URI) to the address
   `ohs doctor` prints, which is `<OHS_PUBLIC_URL>/oauth/polar/callback`. It must match
   exactly, including the scheme.
3. Copy the **client ID** and the **client secret**.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_POLAR_CLIENT_ID=...
OHS_POLAR_CLIENT_SECRET=...
# Optional, see "Webhooks" below.
OHS_POLAR_WEBHOOK_SECRET=...
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL
to register:

```
ok    connector polar
      polar callback: https://ohs.example.org/oauth/polar/callback
```

The client ID and secret are both required: with only one set, `ohs doctor` fails and the
connector stays off. `GET /v1/providers` lists `polar` when it is on.

## 3. Scopes

AccessLink has one scope, `accesslink.read_all`, which the consent page asks for. It covers every
data type below.

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/polar \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url` (on `flow.polar.com`). After consent, Open Health
Sync trades the code for a token at `polarremote.com` (HTTP Basic client authentication) and
registers the user with your client (`POST /v3/users`, member id `ohs-<Polar user id>`); a user
already registered (409) is fine. If the user has not accepted Polar's mandatory consents, Polar
refuses the registration and the connection fails: the user has to accept them in Polar Flow and
connect again.

Polar tokens last for years and come without a refresh token. When Polar refuses a token
(401), or answers 403 because the user is no longer registered, the connection becomes
`needs_reauth` and the user must connect again.

Disconnecting the provider, or deleting the user, de-registers the user from your client
(`DELETE /v3/users/{user-id}`), which also revokes the token at Polar.

## 5. What is pulled, and how far back

A new connection is backfilled over the last 30 days, then pulled every 6 hours. Only the
non-transactional AccessLink endpoints are used, so nothing is committed at Polar and a failed
pull simply runs again. Each endpoint serves a rolling window of recent data, which bounds the
backfill:

| Data | Endpoint | Reaches back |
|---|---|---|
| Exercises | `GET /v3/exercises` | 30 days (by upload time) |
| Sleep | `GET /v3/users/sleep` | 28 days |
| Nightly Recharge | `GET /v3/users/nightly-recharge` | 28 days |
| Daily activity | `GET /v3/users/activities?from=&to=` (28 day windows) | 365 days |
| Continuous heart rate | `GET /v3/users/continuous-heart-rate?from=&to=` (28 day windows) | as Polar keeps it |
| Physical info | `GET /v3/users/physical-info` | current values only |

## 6. Webhooks

AccessLink has **one webhook per client**, not per user, so Open Health Sync does not create it
for you. Create it once with your client credentials:

```sh
curl -s -X POST https://www.polaraccesslink.com/v3/webhooks \
  -u "$OHS_POLAR_CLIENT_ID:$OHS_POLAR_CLIENT_SECRET" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"events":["EXERCISE","SLEEP","CONTINUOUS_HEART_RATE","ACTIVITY_SUMMARY","PHYSICAL_INFORMATION"],
       "url":"https://ohs.example.org/inbound/polar"}'
```

The URL is `<OHS_PUBLIC_URL>/inbound/polar`. Polar sends a `PING` event to it while creating the
webhook and needs a 200 back; Open Health Sync answers a `PING` without checking its signature,
since you only learn the key from this very call. The answer holds a `signature_secret_key`,
shown **once**: set it as `OHS_POLAR_WEBHOOK_SECRET` and restart. From then on every event must
carry a valid `Polar-Webhook-Signature` (the hex HMAC-SHA256 of the body keyed by that secret),
or it is refused with 401.

Without `OHS_POLAR_WEBHOOK_SECRET`, events are accepted without a signature check. That is safe but not
recommended: an event only triggers a pull of the recent window (at most 7 days back) for a
connected user, so a forged one cannot make the server fetch anything else.

An event queues a pull of the user's recent window. `SLEEP_WISE_*` and unknown events are
acknowledged and ignored. For events to arrive, `OHS_PUBLIC_URL` must be reachable from the
internet. Without the webhook nothing breaks: the 6 hour pull still collects everything. Polar
deactivates a webhook after seven days of failed deliveries; reactivate it with
`POST /v3/webhooks/activate` (*to confirm* path) or recreate it.

## 7. Rate limits

AccessLink allows, per client, 500 + 20 × users requests per 15 minutes and 5000 + 100 × users
per 24 hours. A 429 defers the connection's job until `Retry-After`, or when Polar sends none,
until the `RateLimit-Reset` of the window that is used up. One pull of a user costs about six
requests.

## Data produced

| Polar data | Canonical records |
|---|---|
| Exercise | workouts: activity type from the Polar sport, start (local time and `start_time_utc_offset`), duration, distance, calories, average and maximum heart rate; training load, running index and the Polar sport names in the metadata |
| Sleep | sleep sessions (light, deep, REM, interruptions as awake time, sleep score) with phases from the hypnogram, and `heart_rate` samples from the sleep heart rate |
| Nightly Recharge | quantity samples `hrv_rmssd` (ms) and `respiratory_rate`; ANS charge, average heart rate, beat-to-beat interval and the status codes in the metadata |
| Daily activity | quantity samples per day: `step_count`, `active_energy_burned`, `distance_walking_running` (distance from steps); the daily summary is derived from them |
| Continuous heart rate | `heart_rate` samples |
| Physical info | `body_mass`, `height`, `resting_heart_rate`, `vo2_max`; maximum heart rate in the metadata |

Notes on the mapping:

- **HRV is RMSSD.** AccessLink documents `heart_rate_variability_avg` as RMSSD. It is stored as
  `hrv_rmssd` (a canonical name added with this connector, stored in ms), never as `hrv_sdnn`,
  which is a different statistic. The 5 minute HRV and breathing samples of a night are not
  stored (their times carry neither a date nor an offset).
- **Time zones.** Polar gives most times as local wall clock with a separate UTC offset
  (exercises) or an offset in the timestamp (sleep). The metadata keeps `utc_offset_minutes`
  and, for a whole-hour offset, a fixed `Etc/GMT±N` zone as `timezone`. Continuous heart rate
  times carry no offset at all: the connector borrows the offset of a sleep or an exercise of the
  same pull (the same date, or up to 3 days away); without one the times are read as UTC and the
  samples say so (`local_time_as_utc`).
- **Hypnogram** (*to confirm*): read as a map of local `HH:MM` change points to stage codes
  (0 wake, 1 REM, 2 and 3 light, 4 deep, 5 unknown), each stage lasting until the next entry.
  Unknown stages are dropped.
- **Sports** (*to confirm*): AccessLink does not publish the full list of sport names. Common ones
  are mapped; an unknown one becomes `other` with its Polar name in the metadata.

## Updates and deletions

- An exercise is identified by its Polar id and a night by its date, so the same record sent
  again never becomes a second one. Both are replaceable: if a user edits an exercise in Polar
  Flow (calories, sport) or Polar re-scores a night, the new copy replaces the stored figures (a
  figure it leaves out keeps its stored value) and a night's new hypnogram replaces the stored
  sleep phases. If the edit moves the exercise's or the night's start, the new copy replaces the
  stored record of the same id or date instead of adding a second one.
- Daily activity, Nightly Recharge, continuous heart rate and physical info samples are
  replaceable: a later value for the same day (or sample time) replaces the stored one.
- Polar sends no deletion events and the non-transactional endpoints do not say what was
  removed: data deleted in Polar Flow stays in Open Health Sync.

## Provenance

Every sample from this connector has `polar` as provider and source slug, and `Polar` as source
name. Sleep sessions and workouts name only the provider.

Only sleep keeps Polar's `device_id`: the sleep session stores it in its metadata (`device_id`),
and the `heart_rate` samples of the night get it as their device (`DeviceID`). AccessLink sends
an exercise with both a device name (`device`) and a `device_id`; the workout keeps the name in
its metadata and drops the id. Nightly Recharge, daily activity, continuous heart rate and
physical info samples have no device. The test fixtures use invented values (`DEV00001`,
`Polar Test Watch`).

In FHIR output, the [Provenance](../fhir.md#provenance) of each Observation names its device,
when it has one, and the provider.
