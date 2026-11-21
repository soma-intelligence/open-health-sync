# MyFitnessPal (research notes)

There is **no MyFitnessPal connector**, because MyFitnessPal does not grant API access. This page
records what was checked on 2026-11-12 and how MyFitnessPal nutrition data can still reach Open
Health Sync.

## Why there is no connector

- MyFitnessPal's developer page, [myfitnesspalapi.com](https://myfitnesspalapi.com/), says it is
  not accepting requests for API access. Its API is private: there is no way for a self-hosted
  instance to get credentials.
- Community libraries sign in with the member's password and read the website. Open Health Sync
  does not build on that.

## How MyFitnessPal data reaches Open Health Sync

- **Apple Health.** MyFitnessPal can write nutrition (energy, macronutrients and more) to Apple
  Health; the [Apple Health connector](apple-health.md) stores it.
- **Health Connect.** On Android, MyFitnessPal can write nutrition to Health Connect; the
  [Health Connect connector](health-connect.md) stores it.
- **Data export.** Premium members can export their diary as CSV files from the website. Open
  Health Sync has no import for them yet.

## Revisit

If MyFitnessPal opens API access again, open an issue with the
[connector request form](https://github.com/soma-intelligence/open-health-sync/issues/new/choose).
