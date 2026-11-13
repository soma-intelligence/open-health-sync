# Wahoo connector

Slug `wahoo`. Users connect their own Wahoo account over OAuth; Open Health Sync pulls their
workouts and workout summaries from the Wahoo Cloud API on a schedule and receives Wahoo's
workout summary webhooks. You register your own Wahoo application: nothing is shared between
instances.

Status: available. Tests run against a fake of the Wahoo Cloud API built from the example
payloads of Wahoo's [API reference](https://cloud-api.wahooligan.com/) (checked 2026-11-12;
its changelog's latest entry is 2026-10-09).

## Before you start: Wahoo's terms and limits

- **Self-serve, then reviewed.** Anyone can register an application at
  [developers.wahooligan.com](https://developers.wahooligan.com/). It starts as a **sandbox**
  application, usable at once at low limits. A **production** application has to pass a review
  by Wahoo; an application Wahoo has not approved can get HTTP 422.
- **Rate limits** per application, shared by every connected user:

  | Window | Sandbox | Production |
  |---|---|---|
  | 5 minutes | 25 | 200 |
  | 1 hour | 100 | 1,000 |
  | 1 day | 250 | 5,000 |

  A pull costs one request for the profile, one per page of workouts, and one per workout
  listed without its summary. On a sandbox application that is a handful of users.
- **API Agreement.** Read the [Wahoo API Agreement](https://www.wahoofitness.com/wahoo-api-agreement)
  before you offer Wahoo to your users. Among other things it forbids aggregating, caching or
  storing geographic location information, passing Wahoo data to third parties, and uses that
  compete with Wahoo; it allows caching Wahoo data, and asks that a user's personal data is
  deleted when they revoke access. The connector does not download the FIT files a summary
  links to (they hold the GPS track), and stores no location. When a user revokes the
  application, erase their Wahoo data with `DELETE /v1/users/{id}/data/wahoo`
  ([erasing one provider's data](../api.md#erasing-one-providers-data)).
- **Third-party workouts.** Since 2025-04-01 Wahoo does not share workouts that other apps
  wrote to the user's Wahoo account: only what Wahoo devices and apps recorded comes through.

## 1. Create a Wahoo application

1. Sign in at [developers.wahooligan.com](https://developers.wahooligan.com/) and open
   **Applications**.
2. Create an application. Set it **Confidential** (the server keeps the client secret).
3. Add the redirect URI `<OHS_PUBLIC_URL>/oauth/wahoo/callback` (`ohs doctor` prints it).
4. Tick the scopes `user_read`, `workouts_read` and `offline_data`.
5. For webhooks (optional, section 5): turn **webhook enabled** on, set the **webhook URL** to
   `<OHS_PUBLIC_URL>/inbound/wahoo` and choose a long random **webhook token**.
6. Copy the **Client ID** and the **Client Secret**.

## 2. Configure the instance

In `.env`:

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_WAHOO_CLIENT_ID=...
OHS_WAHOO_CLIENT_SECRET=...
# Webhooks (optional, see section 5):
OHS_WAHOO_WEBHOOK_TOKEN=the-webhook-token-set-on-the-application
```

Restart, then run `ohs doctor`. It reports the connector as enabled and prints the callback URL.
`CLIENT_ID` and `CLIENT_SECRET` are required: with only one set the connector stays off.
`WEBHOOK_TOKEN` is optional; without it webhooks are refused and data still arrives with the
scheduled pulls. `GET /v1/providers` lists `wahoo` when it is on. A tenant can bring its own
Wahoo application through the tenant provider keys, like every OAuth provider.

## 3. Scopes

The consent page asks for `user_read workouts_read offline_data`.

| Scope | Gives |
|---|---|
| `user_read` | the profile; Wahoo answers 403 to every request without it |
| `workouts_read` | workouts and workout summaries |
| `offline_data` | webhooks for the user |

A 403 from Wahoo marks the connection `needs_reauth` with a missing scope: the user connects
again and grants it.

## 4. Connect a user

Your server asks for a consent URL (see [api.md](../api.md#connecting-a-provider)):

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/wahoo \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

Send the user to the returned `authorize_url`. After consent they land on your `redirect_uri`
with `status=connected`. The exchange also reads `GET /v1/user` once: the token answer does not
name the user, and webhooks name them by that id. A new connection is backfilled over the last
30 days, then pulled every 6 hours, or every 12 hours while webhooks arrive; each pull reads the
day before again for late uploads.

Access tokens last 2 hours and are refreshed shortly before they expire. Every refresh returns
a new pair, and Wahoo revokes the old one once the new access token is used; it also caps a user
at 10 unrevoked tokens per application, which is why the connector refreshes only near expiry.
A refresh token Wahoo refuses makes the connection `needs_reauth`. Disconnecting revokes the
grant at Wahoo (`DELETE /v1/permissions`), best effort.

## 5. Webhooks

With webhooks on in the developer portal and `OHS_WAHOO_WEBHOOK_TOKEN` set, Wahoo POSTs each new
workout summary of a connected user to `/inbound/wahoo`:

```json
{"event_type": "workout_summary", "webhook_token": "...", "user": {"id": 60462},
 "workout_summary": {"id": 8297, "distance_accum": "24909.71", "...": "...",
   "workout": {"id": 56519, "starts": "2015-08-12T09:00:00.000Z", "workout_type_id": 40}}}
```

Wahoo signs nothing: the webhook token in the body is the only proof, compared in constant time
with `OHS_WAHOO_WEBHOOK_TOKEN`, and a body without it is refused with 401. A webhook carries the
whole summary, so nothing is pulled: the body, with its token removed, is stored as a raw object
(data type `webhook`) for that user's connection and mapped to the same workout a pull gives.
A later pull replaces it rather than adding one. Wahoo retries a webhook it did not get a 200
for after 30 minutes, 4, 24 and 72 hours, and may send one twice; the same body is stored once.
Other event types are answered 200 and ignored.

## Data produced

| Raw data type | From | Records |
|---|---|---|
| `workout` | `GET /v1/workouts`, with the summary embedded (fetched from `GET /v1/workouts/{id}/workout_summary` when the list leaves it out) | one workout |
| `webhook` | a workout summary webhook | the same workout |
| `user` | `GET /v1/user`, reduced to `id`, `height`, `weight` and `updated_at` before it is stored | `body_mass` (kg) and `height` (m, stored in cm), dated `updated_at` |

Each workout:

| Workout field | From |
|---|---|
| `activity_type` | `workout_type_id`, through Wahoo's workout type table: `BIKING*` and indoor trainers → `cycling`, `RUNNING*` → `running`, `WALKING*` → `walking`, `HIKING` → `hiking`, `SWIMMING_*` → `swimming`, `FE_ROWER` and `ROWING` → `rowing`, `YOGA` → `yoga`, and so on. `255` (UNKNOWN) or a new id is `other`; the id and name are always in `metadata.wahoo_workout_type_id` and `metadata.wahoo_workout_type` |
| `start_date`, `end_date` | `starts`, plus `duration_total_accum` (or `minutes` without a summary) |
| `duration_s` | `duration_active_accum`; both durations and `duration_paused_accum` are in metadata |
| `distance_m`, `avg_speed_mps`, `elevation_gain_m` | `distance_accum`, `speed_avg`, `ascent_accum` (zero is left empty) |
| `avg_heart_rate` | `heart_rate_avg`; Wahoo gives no maximum |
| `active_energy_kcal` | `calories_accum` |
| `metadata` | `average_watts`, `normalized_power_w`, `training_stress_score`, `average_cadence_rpm`, `work_j`, `timezone` (from `time_zone`), `indoor`, `manual`, `edited`, `wahoo_fitness_app_id` |

Wahoo sends decimals as strings (`"24909.71"`); the connector reads both strings and numbers.
Not collected: the workout name (free text), the FIT file and anything in it (GPS track,
per-second heart rate and power), plans, routes and power zones. Wahoo has no sleep, daily
activity, nutrition or glucose data.

Rate limits (HTTP 429) defer the connection's job until `Retry-After`, or `X-RateLimit-Reset`
seconds when Wahoo sends no `Retry-After`. Server errors are retried three times.

## Limitations

- **Deleted workouts** are not propagated: Wahoo sends no deletion webhook, and a pull cannot
  tell a deleted workout from an older one.
- **History.** Wahoo's list cannot be filtered by date and is sorted newest first, so a pull
  pages from the newest workout down to the start of its window. The backfill covers 30 days;
  older workouts come from FIT files with the [activity file import](activity-files.md).
- **Field names** follow Wahoo's published reference. Something differs from what you see with
  your account? Open an issue.
