# Peloton (research notes)

There is **no Peloton connector**, because Peloton publishes no developer API. This page records
what was checked on 2026-11-12 and how Peloton workouts can still reach Open Health Sync.

## Why there is no connector

- Peloton runs no developer program and documents no API for third parties.
- The community libraries that read Peloton data call the private API of Peloton's own apps,
  with the member's Peloton password. Peloton's
  [Terms of Service](https://www.onepeloton.com/terms-of-service) (last updated 2025-10-01)
  forbid giving a username and password to third-party applications, automated access to the
  service and reverse engineering it.

Open Health Sync does not build on an undocumented API that the vendor's terms forbid using.

## How Peloton data reaches Open Health Sync

- **Apple Health.** The Peloton app can write workouts to Apple Health; the
  [Apple Health connector](apple-health.md) stores them.
- **Strava.** Peloton can share each workout to Strava; the [Strava connector](strava.md) stores
  it as a workout.
- **Health Connect.** On Android, what the Peloton app writes to Health Connect arrives through
  the [Health Connect connector](health-connect.md) (check the app's settings).
- **Workout history download.** On the Peloton website, the member's profile offers a CSV
  download of their workout history. Open Health Sync has no import for that CSV yet.

## Revisit

If Peloton opens a documented developer API, open an issue with the
[connector request form](https://github.com/soma-intelligence/open-health-sync/issues/new/choose).
