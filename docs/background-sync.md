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
   notification was lost:
   - every **6 hours** (00:00, 06:00, 12:00, 18:00 UTC) while the connection's notifications
     arrive (one came in the last 24 hours);
   - every **hour** otherwise: the provider sends none, they are not configured, or they
     stopped.
   Each pull overlaps the previous one by an hour for late data. After an outage it reaches
   back to the last successful sync, up to 7 days.
3. **Repair.** A subscription that has heard nothing for 24 hours and is more than 7 days old
   is renewed at the provider, at most once a week per connection. Some providers drop a
   subscription after failed deliveries and say nothing.

Tokens are refreshed before use when they expire within 5 minutes. Since every connection is
pulled at least every 6 hours, refresh tokens never go unused for long. A refresh the provider
refuses marks the connection `needs_reauth`: the user has to consent again.

Failures back off (30 s, doubling, 5 attempts per window). A provider rate limit (HTTP 429)
defers the pull until its `Retry-After` without counting a failure. The next slot catches up
whatever a failed window missed.

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
