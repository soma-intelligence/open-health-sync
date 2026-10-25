# Strava connector

Slug `strava`. Users connect their own Strava account over OAuth; Open Health Sync pulls their
activities on a schedule and when Strava sends a webhook event. You register your own Strava
API application: nothing is shared between instances.

Status: available. Tests run against a fake of the Strava API built from the published
reference.

## Before you start: Strava's terms and limits

- **Athlete limit.** A new Strava API application can be connected by 1 athlete (its owner).
  You can raise it to 10 yourself in the API settings; beyond that, Strava reviews the
  application. Plan for this before you offer Strava to your users.
- **API agreement (2026 revision).** Strava's API terms forbid using API data to train AI
  models and restrict apps that pass Strava data on to third parties as an intermediary
  platform. Read the current [API agreement](https://www.strava.com/legal/api) and decide
  whether your use of Open Health Sync fits it. Third parties report that a paid Strava
  subscription is now needed for API access; Strava's developer site is the reference.
- **Rate limits.** By default an application may make 200 requests per 15 minutes and 2,000
  per day overall, and 100 per 15 minutes and 1,000 per day for reads. The limits are per
  application, shared by every connected athlete. The connector pulls summary activities
  only (one request per 200 activities) for that reason.

## 1. Create a Strava API application

1. Sign in to Strava and open [strava.com/settings/api](https://www.strava.com/settings/api).
2. Create an application. Name, website and icon are yours to choose.
3. Set the **Authorization Callback Domain** to the host of `OHS_PUBLIC_URL`, without the
   scheme or path: for `https://ohs.example.org` enter `ohs.example.org`. Strava only checks
   the domain; the full callback Open Health Sync uses is
   `<OHS_PUBLIC_URL>/oauth/strava/callback` (`ohs doctor` prints it). `localhost` is accepted
   for local testing.
4. Copy the **Client ID** and the **Client Secret**.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_STRAVA_CLIENT_ID=...
OHS_STRAVA_CLIENT_SECRET=...
# Webhook events (optional, see section 5):
OHS_STRAVA_VERIFY_TOKEN=a-long-random-string
# OHS_STRAVA_SUBSCRIPTION_ID=...   set after creating the subscription
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL:

```
ok    connector strava
      strava callback: https://ohs.example.org/oauth/strava/callback
```

`CLIENT_ID` and `CLIENT_SECRET` are required: with only one set, `ohs doctor` fails and the
connector stays off. `VERIFY_TOKEN` and `SUBSCRIPTION_ID` are optional; without
`VERIFY_TOKEN` webhook events are refused and data still arrives every 3 hours, and without
`SUBSCRIPTION_ID` activity deletes are not applied.
`GET /v1/providers` lists `strava` when it is on.

## 3. Scopes

The consent page asks for `read,activity:read_all` with `approval_prompt=auto` (an athlete
who already agreed is not asked again).

| Scope | Gives |
|---|---|
| `read` | required by Strava for every application |
| `activity:read_all` | every activity, including those visible to "Only You", and privacy zone data |

Strava lets the athlete untick `activity:read_all` at consent. Without an activity scope the
pulls come back empty.

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/strava \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`. After consent they land on your `redirect_uri`
with `status=connected`. A new connection is backfilled over the last 30 days, then pulled
every 12 hours while webhook events arrive and every 3 hours without them (Strava's read limits are low). Access tokens last 6 hours and are refreshed before they expire; Strava may
rotate the refresh token, and the new one is stored.

Disconnecting the provider revokes the grant at Strava (`POST /oauth/revoke`), best effort.

## 5. Webhook events

Strava has one push subscription per application, for every athlete who authorized it. You
create it once, after the instance is reachable from the internet with
`OHS_STRAVA_VERIFY_TOKEN` set:

```sh
curl -X POST https://www.strava.com/api/v3/push_subscriptions \
  -F client_id=$OHS_STRAVA_CLIENT_ID \
  -F client_secret=$OHS_STRAVA_CLIENT_SECRET \
  -F callback_url=https://ohs.example.org/inbound/strava \
  -F verify_token=$OHS_STRAVA_VERIFY_TOKEN
# {"id": 123456}
```

Strava immediately sends `GET /inbound/strava?hub.mode=subscribe&hub.verify_token=...&hub.challenge=...`;
Open Health Sync answers `{"hub.challenge": "..."}` when the verify token matches, and 401
otherwise. The answer has to arrive within two seconds, so create the subscription against a
running instance. Then set `OHS_STRAVA_SUBSCRIPTION_ID` to the returned id and restart: events
naming another subscription are refused from then on.

To see or delete the subscription:

```sh
curl -G https://www.strava.com/api/v3/push_subscriptions \
  -d client_id=$OHS_STRAVA_CLIENT_ID -d client_secret=$OHS_STRAVA_CLIENT_SECRET
curl -X DELETE "https://www.strava.com/api/v3/push_subscriptions/123456?client_id=$OHS_STRAVA_CLIENT_ID&client_secret=$OHS_STRAVA_CLIENT_SECRET"
```

Strava does not sign events. An event is only a change signal: it queues a pull of a recent
window for an athlete who has an active connection here, read with that athlete's own token,
so a forged event cannot inject data or reach another user.

| Event | What happens |
|---|---|
| activity `create` or `update` | a pull of the last 7 days. An updated activity replaces the stored workout (same Strava id): no duplicate |
| activity `delete` | with `OHS_STRAVA_SUBSCRIPTION_ID` set, the workout of that activity is removed (see below); without it, nothing |
| athlete `update` with `updates.authorized = "false"` (the athlete revoked access in Strava) | a short pull. Strava has revoked the tokens, so it is refused, the refresh is refused too, and the connection becomes `needs_reauth` |
| anything else | ignored, answered 200 so Strava does not retry |

## Data produced

Each Strava activity (the summary of `GET /athlete/activities`) becomes one workout:

| Workout field | From |
|---|---|
| `activity_type` | `sport_type` (or the older `type`), mapped to the names Apple Health and Withings use: `Run`, `TrailRun`, `VirtualRun` → `running`; `Ride`, `MountainBikeRide`, `GravelRide`, `EBikeRide`, `VirtualRide` → `cycling`; `Walk` → `walking`; `Hike` → `hiking`; `Swim` → `swimming`; `WeightTraining` → `traditional_strength_training`; and so on. An unknown sport is `other`; the Strava name is always in `metadata.strava_sport_type` |
| `start_date`, `end_date` | `start_date`, plus `elapsed_time` |
| `duration_s` | `moving_time` (`elapsed_time` when missing); both are in metadata |
| `distance_m`, `avg_speed_mps`, `elevation_gain_m` | `distance`, `average_speed`, `total_elevation_gain`. A zero distance or speed (yoga, strength) is left empty |
| `avg_heart_rate`, `max_heart_rate` | `average_heartrate`, `max_heartrate`, when recorded |
| `active_energy_kcal` | `calories` when Strava gives them, otherwise `kilojoules` one to one (the convention Strava itself uses for power-based rides). `metadata.active_energy_source` says which |
| `metadata.timezone` | the IANA name from `timezone`, `"(GMT+01:00) Europe/Paris"` → `Europe/Paris` |
| `metadata.indoor` | `trainer`, or a virtual sport type |

Not collected: the activity name and description (free text), detailed activities, streams
(heart rate, power, GPS series), laps, and the route. The summary polyline has no timestamps,
and a route point needs one, so no route is stored.

Rate limits (HTTP 429) defer the connection's job: until `Retry-After` when Strava sends one,
otherwise until the next quarter hour, or until midnight UTC when the daily limit is spent (read
from `X-RateLimit-Usage` and `X-ReadRateLimit-Usage`). If Strava refuses the refresh token the
connection becomes `needs_reauth` and the user must connect again.

## Limitations

- **Deleted activities.** With `OHS_STRAVA_SUBSCRIPTION_ID` set, an activity `delete` event is
  stored verbatim as a raw object (data type `event`) for every connection of that athlete, and
  the ETL removes the workout of that Strava id. A tombstone keeps it from coming back with a
  later pull or a reprocessed raw object, even when the delete is processed first. Strava signs
  nothing, and a deletion, unlike a pull, removes data, so deletes are applied only when the
  subscription id is configured and matches; without it they are logged and ignored and the
  workout stays. Anyone who knows the subscription id, an athlete id and an activity id of that
  athlete could still forge one: keep the subscription id private. A deletion lost while the
  instance was unreachable is not recovered by a pull.
- **Late edits.** A pull asks Strava for activities by start time. An activity created or
  edited more than 7 days after it started is only picked up if a pull window covers its start
  (a new connection is backfilled over 30 days).
- **Field names** follow Strava's published reference. Something differs from what you see
  with your account? Open an issue.
