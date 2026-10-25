# Background sync

Open Health Sync keeps each user's data current without anyone pressing "sync". This page
explains how, how to see how fresh the data is, and what a mobile app must do for the on-phone
stores.

## Cloud providers

The `collect` role does the work. Three mechanisms stack:

1. **Notifications first.** When a provider sends webhooks (Withings, and the app-level
   webhooks of other providers), each notification queues a pull of the window it names.
   Bursts are merged into one pull. Notification pulls are claimed before any backfill or
   backstop pull, so a burst of slot jobs or a rate limit never queues fresh data behind
   routine work. When the connector can (Withings), the pull fetches only what the
   notification concerns: a new weight costs 4 Withings calls instead of 9, a new sleep 2,
   which keeps the app's rate limit for what just changed. Data lands within seconds of the
   provider having it.
2. **A backstop pull.** Every connection is also pulled on a fixed slot, in case a
   notification was lost. By default:
   - every **6 hours** (00:00, 06:00, 12:00, 18:00 UTC) while the connection's notifications
     arrive (one came in the last 24 hours);
   - every **hour** otherwise: the provider sends none, they are not configured, or they
     stopped.
   Each pull starts again a little before the previous one ended (its *lookback*, an hour by
   default) for data that reached the provider late. After an outage, or when a window's pull
   failed, it reaches back to the end of the last complete pull, up to 7 days. A pull that a
   notification asked for, which fetches only one data type, does not count as complete, and
   neither does a short pull that starts after a gap.
   Each provider sets its own values: see [sync policy per provider](#sync-policy-per-provider).
3. **Repair.** A subscription that has heard nothing for 24 hours and is more than 7 days old
   is renewed at the provider, at most once a week per connection. Some providers drop a
   subscription after failed deliveries and say nothing.

Tokens are refreshed before use when they expire within 5 minutes. Since every connection is
pulled at least once a day, refresh tokens never go unused for long. A refresh the provider
refuses marks the connection `needs_reauth`: the user has to consent again.

Failures back off (30 s, doubling, 5 attempts per window). A provider rate limit (HTTP 429)
defers the pull until its `Retry-After` without counting a failure. The next slot catches up
whatever a failed window missed.

## Provider rate limits

Most providers limit an application as a whole, so one install that pulls too fast can get its
app blocked for every user. Three rules keep pulls inside each provider's budget:

- **Order.** Pulls a provider notification or a manual sync asked for go first, then the regular
  pulls, then backfills. A hundred new users backfilling 30 days each never hold back the
  regular pulls of everyone else.
- **Caps.** At most `OHS_PULLS_PER_PROVIDER` pulls of one provider run at once (default 4), of
  which at most `OHS_BACKFILLS_PER_PROVIDER` are backfills (default 2). The caps hold across every
  instance running the `collect` role: claims take turns on a database lock.
- **Shared backoff.** When a pull meets a rate limit, every pull of that provider pauses, not only
  that user's: for the provider's `Retry-After`, at least 30 s doubled for each limit met in a
  row, at most 15 minutes. The user's own pull waits its full `Retry-After`. The first successful
  pull resets the doubling.

`ohs_sync_provider_paused_seconds{provider}` on `/metrics` says how long a provider stays
paused. A connection whose provider asks to wait an hour or more is reported `rate_limited` in its
health (see [sync history and connection health](sync-health.md)).

## Sync policy per provider

Providers differ: Dexcom readings reach its API hours late, an OMRON cuff uploads when the phone
app syncs, Polar keeps only 28 days, Suunto's development tier allows about 200 calls a week.
So each connector declares how it is pulled (`connector.SyncPolicy`, see
[writing a connector](writing-a-connector.md#sync-policy)):

- **Interval**: how often the backstop pull runs, while notifications arrive and without them.
- **Lookback**: how far before the previous window each pull starts again.
- **Backfill**: how far back a new connection is pulled.
- **Reconciliation**: once a day, the slot ending at 00:00 UTC reaches back this far, so data
  revised later than the lookback (a re-scored sleep, an activity edited days later) or a window
  whose pull failed is fetched again.

| Provider | Interval with notifications | Interval without | Lookback | Backfill | Daily reconciliation |
|---|---|---|---|---|---|
| Default (Garmin) | 6 h | 1 h | 1 h | 30 days | none |
| Dexcom | | 1 h | 6 h | 90 days | 2 days |
| Eight Sleep | | 3 h | 36 h | 30 days | 7 days |
| Fitbit | 6 h | 3 h | 6 h | 30 days | 3 days |
| intervals.icu | 6 h | 1 h | 24 h | 30 days | 7 days |
| OMRON | | 3 h | 72 h | 30 days | 14 days |
| Oura | 6 h | 1 h | 24 h | 90 days | 7 days |
| Polar | 6 h | 1 h | 24 h | 28 days | 7 days |
| Strava | 12 h | 3 h | 24 h | 30 days | 7 days |
| Suunto | 24 h | 24 h | 24 h | 28 days | none |
| Ultrahuman | | 3 h | 48 h | 30 days | 7 days |
| WHOOP | 6 h | 1 h | 24 h | 90 days | 7 days |
| Withings | 6 h | 1 h | 24 h | 90 days | 3 days |

An empty cell means the provider sends no notifications. A Suunto pull makes 4 calls, so on the
Development API (about 200 calls a week) an application serves about 7 users; more needs
Suunto's Production API. Garmin pulls by upload time, so the
default lookback is enough there. `pull_every_seconds` and `next_pull_at` in the freshness
response follow the provider's interval.

## Freshness

`GET /v1/users/{id}/freshness` (tenant key) returns one entry per source:

```json
{
  "as_of": "2026-10-10T15:05:00Z",
  "data": [
    {
      "provider": "withings", "kind": "oauth", "status": "active", "state": "fresh",
      "synced_through": "2026-10-10T15:00:00Z", "lag_seconds": 300,
      "last_sync_at": "2026-10-10T15:00:41Z", "last_event_at": "2026-10-10T14:58:02Z",
      "push_healthy": true, "pull_every_seconds": 21600,
      "next_pull_at": "2026-10-10T18:00:00Z", "last_attempt_at": "2026-10-10T15:00:41Z",
      "last_error": null, "consecutive_failures": 0
    },
    {
      "provider": "apple_health", "kind": "device", "status": "active", "state": "fresh",
      "synced_through": "2026-10-10T13:40:00Z", "lag_seconds": 5100, "...": "..."
    }
  ]
}
```

- `synced_through` is the end of the most recent window pulled for every data type. It only
  moves forward: a backfill of last month does not make the data look fresh, and a
  notification pull of one type does not make the others look current. For a device source it is the
  time of the last upload.
- `state` is `fresh`; `stale` when behind by more than two pull intervals plus an hour (or
  no upload for 26 hours for a device source); `failing` after 3 failed pulls in a row (see
  `last_error`); `syncing` until the first pull finishes; `needs_reauth`; `disconnected`.
- `last_event_at` is the last provider notification accepted for the connection, a deletion
  notice included. `push_healthy` is true while notifications arrive within 24 hours: the
  regular pull is then only a backstop.

Each connection also has a `health` object that says whether it syncs and what fixes it, the
history of its pulls is at `GET /v1/users/{id}/sync-runs`, and a pull can be started on demand: see
[sync history and connection health](sync-health.md).

`/metrics` carries the same picture per provider: `ohs_sync_connections{provider,state}`,
`ohs_sync_max_lag_seconds{provider}` and `ohs_sync_push_silent_connections{provider}`. Alert on
`failing` and on silent connections of a provider that should be notifying.

## On-phone stores (Apple Health, Health Connect)

The server cannot pull from a phone: the app must upload. How fresh this data is depends on
how often the OS lets the app run. What an SDK needs:

### iOS, HealthKit

- An `HKObserverQuery` per uploaded type, registered at every launch (in
  `application(_:didFinishLaunchingWithOptions:)`), and `enableBackgroundDelivery(for:frequency:)`
  with `.immediate` for the types that matter (iOS caps some types, such as step count, at
  hourly). iOS then wakes the app when new samples are saved.
- In the observer's handler, an `HKAnchoredObjectQuery` from the last stored anchor, upload
  the new samples and deletions to `POST /v1/ingest/apple-health`, store the new anchor only
  after the server acknowledged the upload, then call the completion handler. Not calling it
  makes iOS back off and stop waking the app.
- The `com.apple.developer.healthkit.background-delivery` entitlement, and a
  `BGAppRefreshTask` as a backstop for missed wakes.
- Uploads must be idempotent (same batch id and part on retry), since a wake can be cut short.
- HealthKit data is unreadable while the device is locked: expect wakes to land after unlock.

### Android, Health Connect

- Health Connect has no push. A `PeriodicWorkRequest` (WorkManager, 15 minutes is the minimum
  interval, with a network constraint) reads `getChanges(token)` and uploads the changes
  and deletions, then stores the next changes token after the server acknowledged them.
- A changes token expires after 30 days unused: on `changesTokenExpired`, read the full
  window again from the last uploaded time and take a new token.
- Background reads need the `READ_HEALTH_DATA_IN_BACKGROUND` permission (Android 14+ /
  recent Health Connect); without it, sync only runs while the app is in the foreground.
- Also sync on app open, so a user who opens the app sees current data at once.

The server side is ready for both: uploads are idempotent per batch and part, and
`/freshness` reports a device source as `stale` after 26 hours without an upload, so an app can
nudge its user to open it.
