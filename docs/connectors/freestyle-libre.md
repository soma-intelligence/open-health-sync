# FreeStyle Libre (research notes)

There is **no FreeStyle Libre connector**, because Abbott publishes no public developer API.
This page records what was checked, in October 2026, and how Libre glucose readings can still
reach Open Health Sync.

## What exists

- **LibreView** is Abbott's cloud for Libre readings, used by patients and clinics. Abbott
  offers no self-serve developer program for it. Data integrations exist only through
  agreements Abbott makes with companies, and their terms are not public.
- **LibreLinkUp** is Abbott's follower app, for caregivers. Community libraries call its
  private app API. That API is undocumented, unsupported and can change without notice, and
  using it means handling the user's Abbott password. Open Health Sync does not build on
  reverse-engineered APIs, so it will not use it.

## How Libre data reaches Open Health Sync

- **Android**: when a Libre app writes glucose to Health Connect, the
  [Health Connect connector](health-connect.md) stores it, with `source_slug`
  `freestyle_libre` and the specimen `interstitial_fluid`. Whether a given Libre app writes
  to Health Connect depends on the app and the country; check it in the app's settings.
- **iPhone**: when a Libre app writes glucose to Apple Health, the
  [Apple Health connector](apple-health.md) stores it as `blood_glucose`.

## Revisit

If Abbott opens a public API or a partner program you have access to, open an issue with the
[connector request form](https://github.com/soma-intelligence/open-health-sync/issues/new/choose).
