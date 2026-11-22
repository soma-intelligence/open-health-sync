# How-to guides

Short guides for one task each, from a fresh instance to the result. Each one starts with the
answer, then the commands, run against an Open Health Sync instance you start with the
[quickstart](../../README.md#quickstart).

## Apple Health and Android

| Guide | What you get |
|---|---|
| [Import Apple Health data into PostgreSQL](apple-health-to-postgres.md) | your Health app export as clean tables, and the SQL to query them |
| [Convert an Apple Health export to CSV](apple-health-to-csv.md) | your Health app export as CSV or Parquet files, one per kind of data, ready for a spreadsheet |
| [Connect Claude to your Apple Health data](apple-health-mcp.md) | an AI assistant answering questions from your own numbers, through a read-only token |
| [Sync HealthKit data from your iOS app to your own backend](healthkit-ios-app-sync.md) | new HealthKit data uploaded by your app with the Swift SDK, deletions and edits included |
| [Send Android Health Connect data to your own server](health-connect-to-server.md) | Health Connect records uploaded by your Android app, in the same model as every other source |
| [Get Samsung Health data through an API](samsung-health-data.md) | Samsung Health steps, sleep, heart rate and more, through Health Connect |

## Wearables and apps

| Guide | What you get |
|---|---|
| [Get Oura data as FHIR R4](oura-fhir.md) | your ring's sleep, heart rate and daily scores as FHIR Observations |
| [Export WHOOP data to CSV or Parquet](whoop-export.md) | recovery, strain, sleep and workouts in files you can open anywhere |
| [Export Withings weight and blood pressure to CSV or SQL](withings-export.md) | every weigh-in and blood pressure reading in a database you query, and in files |
| [Export Garmin Connect activities to PostgreSQL or CSV](garmin-export.md) | every activity of your Garmin account with its route and heart rate, in tables and files |
| [Get Fitbit data through the Google Health API and export it](fitbit-export.md) | steps, heart rate, HRV, sleep, workouts and body measurements from a Google Account |
| [Export Polar data to CSV or PostgreSQL](polar-export.md) | exercises, sleep, Nightly Recharge and daily activity from Polar AccessLink |
| [Export Strava activities to PostgreSQL or CSV](strava-export.md) | one workout row per Strava activity, and full streams from the Strava archive |
| [Export Eight Sleep data to CSV or SQL](eight-sleep-export.md) | your nights, sleep score, HRV and bed temperature in a database and in files |
| [Get Dexcom CGM glucose readings through an API](dexcom-glucose-api.md) | CGM readings with trend arrows, as JSON, CSV, Parquet or FHIR R4 |
| [Export Xiaomi Mi Fitness data to CSV or PostgreSQL](mi-fitness-export.md) | your Mi Fitness history from the Xiaomi data copy, in tables and files |
| [Repair Fitbit rows of relayed Apple Health and Health Connect data](fitbit-relayed-points.md) | Fitbit rows stored before relayed points were told apart, fixed in place |

## Files and devices

| Guide | What you get |
|---|---|
| [Import FIT, GPX and TCX activity files into PostgreSQL](fit-gpx-tcx-import.md) | workouts with routes and per-second heart rate from COROS, Wahoo, Zwift, Garmin or Strava files |
| [Record a Bluetooth heart rate strap, blood pressure cuff or scale without a phone](bluetooth-devices.md) | readings from standard Bluetooth health devices, through `ohs-bridge` on a computer or a Pi |

## Outputs and sharing

| Guide | What you get |
|---|---|
| [Get a webhook when new wearable data arrives](webhooks-new-data.md) | a signed `data.written` event for each sync or upload, and the code to verify it |
| [Export wearable data as Open mHealth JSON](open-mhealth-export.md) | data points that follow the Open mHealth schemas, from every source |
| [Give an AI agent read-only access to wearable data over MCP](mcp-remote-agent.md) | an MCP client reading sleep, training and daily figures from your instance |
| [Share your health data with your clinician through a link](share-with-clinician.md) | a link with a passcode, an expiry and only the categories you picked |
| [Combine data from several wearables without counting it twice](merge-multiple-wearables.md) | one figure per day when a ring, a watch and a phone all measured it |

## Running it and data rights

| Guide | What you get |
|---|---|
| [Run Open Health Sync with Docker Compose and HTTPS](docker-compose-https.md) | the compose stack behind Caddy with an automatic TLS certificate |
| [Self-host a health data API on a Raspberry Pi 5](raspberry-pi.md) | the whole stack on a 4 GB Pi, with backups and upgrades |
| [Delete a user's health data (erasure and data portability)](delete-user-data.md) | a full archive for the person, then one call that erases them |
| [Move from the Terra API to a self-hosted aggregator](migrate-from-terra.md) | Terra's concepts mapped to routes on your own instance |

For longer projects, see [Learn by building](../learn/README.md). For small tools on top of
your data, see [Build with your own health data](../recipes/README.md). Side by side with hosted
APIs: [Health data aggregators compared](../compare/README.md).
