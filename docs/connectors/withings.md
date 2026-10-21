# Withings connector

Slug `withings`. Users connect their own Withings account over OAuth; Open Health Sync pulls
their data on a schedule and when Withings sends a notification. You register your own
Withings developer application: nothing is shared between instances.

## 1. Get your keys

1. Create a Withings developer account and open the developer dashboard
   (`developer.withings.com`). Portal labels change from time to time; the fields below are
   what matters.
2. Create an application (the public API integration type).
3. Set the **callback URL** (the OAuth redirect URI) to the address `ohs doctor` prints, which is
   `<OHS_PUBLIC_URL>/oauth/withings/callback`. It must match exactly, including the scheme.
   Withings can refuse an `http` callback outside local testing, so give a real instance an
   https `OHS_PUBLIC_URL`.
4. Copy the **client ID** and the **client secret** (Withings may call them application ID and
   consumer secret).

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_WITHINGS_CLIENT_ID=...
OHS_WITHINGS_CLIENT_SECRET=...
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL
to register:

```
ok    connector withings
      withings callback: https://ohs.example.org/oauth/withings/callback
```

Both keys are required: with only one set, `ohs doctor` fails and the connector stays off.
`GET /v1/providers` lists `withings` when it is on.

## 3. Scopes

The consent page asks for:

| Scope | Gives |
|---|---|
| `user.metrics` | body measurements (weight, composition, blood pressure, SpO2, temperature, vascular age, VO2 max) and heart rate and ECG data |
| `user.activity` | daily activity, intraday activity, workouts |
| `user.sleepevents` | sleep summaries and phases |

The connector does not ask for `user.info`. Withings may let the user untick a scope at consent; a data type whose scope was refused is
simply empty for that user.

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/withings \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`. After consent they land on your `redirect_uri`
with `status=connected`. A new connection is backfilled over the last 30 days in 7 day
windows, then pulled every 6 hours.

## 5. Notifications

After a user connects, Open Health Sync subscribes them to Withings notifications for
measurements, activity, sleep and workouts. The callback registered with Withings is
`<OHS_PUBLIC_URL>/inbound/withings?sig=...`. Withings does not sign its notifications, so the
`sig` parameter is the proof: an HMAC of the Withings user id keyed by your client secret. A
notification only triggers a pull of the recent window (at most 7 days back), so a forged one
cannot make the server fetch a user's whole history.

A notification pulls only what it concerns: a measurement pulls measures and ECG (4 calls),
activity pulls activity and intraday (2), sleep (2) and workouts (1), instead of all 9 calls
of a full pull. Notification pulls run before backfill and backstop pulls.

For notifications to arrive, `OHS_PUBLIC_URL` must be reachable from the internet. Without it
nothing breaks: the backstop pull still collects everything, every hour while no
notification arrives (see [background sync](../background-sync.md)).

The subscription is durable. Connecting writes it as pending; the `collect` role subscribes it
at Withings and, when that fails, retries with backoff (from 1 minute, doubling up to 6 hours)
until it works, logging why each attempt failed. A failure never blocks the connection: the 6
hour pull keeps running. Disconnecting the provider, or deleting the user, unsubscribes it at
Withings and marks it removed. The process that runs `collect` (and `etl`, which carries out
deletions) needs the Withings keys for this.

## 6. Rotating the client secret

The client secret signs every registered notification URL. After you change it, every connected
user's notifications are refused until they are subscribed again. Pulls and OAuth refresh are
unaffected, so data still flows every 6 hours.

To subscribe everyone again, restart with the new secret, then put the live subscriptions back
to pending in `psql`; the `collect` role subscribes them with the new signature within a minute
or two:

```sql
UPDATE ohs_sync.inbound_subscriptions
SET status = 'pending', attempts = 0, next_attempt_at = now(), last_error = NULL, updated_at = now()
WHERE provider = 'withings' AND status = 'subscribed';
```

The registrations made with the old secret stay at Withings (they can only be revoked under
their old signature). Their notifications are refused, which is harmless: the pulls and the
new subscriptions carry on.

## Data produced

| Withings data | Canonical records |
|---|---|
| Measures | quantity samples: `body_mass`, `height`, `lean_body_mass`, `body_fat_percentage`, `body_fat_mass`, `muscle_mass`, `water_mass`, `bone_mass`, `blood_pressure_systolic`, `blood_pressure_diastolic`, `heart_rate`, `body_temperature`, `skin_temperature`, `oxygen_saturation`, `pulse_wave_velocity`, `vo2_max`, `vascular_age`, `visceral_fat_rating` |
| Activity | quantity samples per day: `step_count`, `distance_walking_running`, `active_energy_burned`, `moderate_activity_time` and `vigorous_activity_time` (Withings' `moderate` and `intense` durations, sent in seconds, stored in minutes); the daily summary is derived from them |
| Intraday activity | `step_count_intraday`, `active_energy_burned_intraday`, `distance_walking_running_intraday` |
| Sleep | sleep sessions with phases |
| Workouts | workouts |
| ECG | ECG recordings with the waveform when Withings returns one |

Withings measure codes the connector does not know are skipped with a warning in the log.
Rate limits (HTTP 429) defer the connection's job until `Retry-After`.
If Withings refuses the refresh token the connection becomes `needs_reauth` and the user must
connect again.
