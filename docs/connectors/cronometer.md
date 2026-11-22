# Cronometer (research notes)

There is **no Cronometer connector**, because Cronometer publishes no public API. This page
records what was checked on 2026-11-12 and how Cronometer nutrition data can still reach Open
Health Sync.

## Why there is no connector

- Cronometer documents no developer API. Its data integrations are partnerships with named
  companies ([Cronometer Pro FAQ](https://cronometer.com/blog/cronometer-pro-faq/)), with no
  self-serve program a self-hosted instance could join.
- Community tools sign in with the member's password and read the website's private endpoints.
  Open Health Sync does not build on that.

## How Cronometer data reaches Open Health Sync

- **Apple Health.** Cronometer can write nutrition to Apple Health; the
  [Apple Health connector](apple-health.md) stores it.
- **Health Connect.** On Android, Cronometer can write nutrition to Health Connect; the
  [Health Connect connector](health-connect.md) stores it.
- **Data export.** Cronometer exports servings, daily nutrition, exercises and biometrics as CSV
  files from the account settings. Open Health Sync has no import for them yet.

## Revisit

If Cronometer opens a public API or a partner program you have access to, open an issue with the
[connector request form](https://github.com/soma-intelligence/open-health-sync/issues/new/choose).
