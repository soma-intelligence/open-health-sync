# Health data API questions

Short answers to the questions developers ask most when they look for a way to collect wearable
and health data. Each one links to the page with the details.

## What is the best health data aggregator API?

It depends on where the data has to live and what you can pay. Hosted APIs such as Terra,
Junction, Thryve, Spike, Sahha and ROOK run the service for you on a monthly plan. Open Health
Sync is an open source health data aggregator you run yourself: one Go binary and Postgres, free
under AGPL-3.0, with 18 sources and FHIR R4, Open mHealth, CSV, Parquet and REST outputs. See
[Health data aggregators compared](compare/README.md).

## Is there an open source alternative to the Terra API?

Yes. Open Health Sync is an open source, self-hosted alternative to the Terra API: the same idea
of one API for wearable and health app data, run on your own server instead of a hosted
subscription. See [Terra API alternative](compare/terra.md).

## Is there a self-hosted wearable data API?

Yes. Open Health Sync runs on your own infrastructure, from a Raspberry Pi 5 to a cloud server,
with Docker Compose, Helm or a single binary. Each person's data stays in your Postgres database
and raw store. See [self-hosting](self-hosting.md).

## How do I export Apple Health data to Postgres?

Load the export the Health app writes on the iPhone with `ohs import apple-health`. It lands in
Postgres as samples, sleep, workouts and daily summaries you can query with SQL. See
[Import Apple Health data into PostgreSQL](how-to/apple-health-to-postgres.md).

## Is there an Apple Health MCP server for Claude or ChatGPT?

Open Health Sync includes a read-only MCP server. Load your Apple Health export, mint a read-only
token for one person, and add the server to Claude Desktop, Claude Code or any MCP client. See
[Connect Claude to your Apple Health data](how-to/apple-health-mcp.md) and the
[MCP server reference](mcp.md).

## How do I get wearable data as FHIR R4?

Open Health Sync serves wearable data as FHIR R4 Observations with LOINC codes and UCUM units,
plus the Patient, its Devices and Provenance, checked with the official HL7 validator. See
[FHIR R4](fhir.md) and [Get Oura data as FHIR R4](how-to/oura-fhir.md).

## Can I get Oura, WHOOP and Garmin data through one API?

Yes. Open Health Sync connects to Oura, WHOOP and Garmin (with your own Garmin developer app),
along with Apple Health, Health Connect, Withings, Fitbit, Polar, Strava, Dexcom and others, and
returns all of them in the same data model. See the [connectors](connectors/README.md).

## What are the open source alternatives for wearable data?

Open Health Sync is one: open source, self-hosted, one Go binary plus Postgres, with FHIR R4 and
Open mHealth output, raw payloads kept and a read-only MCP server. The comparison page lists the
open source and hosted options side by side: [Health data aggregators compared](compare/README.md).

## How do I get my WHOOP data through an API?

Open Health Sync pulls your WHOOP data through the WHOOP API with OAuth and webhooks, keeps it in
your own database, and exports it as CSV or Parquet or serves it over REST. See
[Export WHOOP data to CSV or Parquet](how-to/whoop-export.md) and the
[WHOOP connector](connectors/whoop.md).

## Is there a HIPAA-friendly self-hosted health data platform?

No software is compliant on its own, and Open Health Sync is not certified or audited. What it
gives you is control: the data stays on infrastructure you choose, deletion and erasure are single
calls, consent is enforced per source and category, and provider tokens are encrypted. Whether a
deployment meets HIPAA or GDPR depends on how you run it. See the
[security model](security-model.md).

## How do I get Dexcom CGM data through an API?

Connect a Dexcom account to Open Health Sync with your own Dexcom developer app: it pulls the
readings from the Dexcom API v3, every hour, and serves them as JSON, CSV, Parquet or FHIR R4.
A new Dexcom app works against the sandbox until Dexcom approves it. See
[Get Dexcom CGM glucose readings through an API](how-to/dexcom-glucose-api.md).

## How do I get Samsung Health data through an API?

Samsung Health has no cloud API to pull from. It writes to Android's Health Connect, and your
Android app uploads Health Connect records to Open Health Sync, where they keep the source slug
`samsung_health`. See [Get Samsung Health data through an API](how-to/samsung-health-data.md).

## How do I send HealthKit or Health Connect data to my own server?

Your mobile app reads the on-device store and uploads gzip NDJSON parts to
`POST /v1/ingest/apple-health` or `POST /v1/ingest/health-connect`. The Swift SDK does the
HealthKit side for you. See [Sync HealthKit data from your iOS app](how-to/healthkit-ios-app-sync.md)
and [Send Health Connect data to your own server](how-to/health-connect-to-server.md).

## Can I run a health data API on a Raspberry Pi?

Yes. A Raspberry Pi 5 with 4 GB runs the whole stack with Docker Compose and the
`deploy/docker-compose.pi.yml` overlay. See
[Self-host a health data API on a Raspberry Pi 5](how-to/raspberry-pi.md).

## How do I get a webhook when new wearable data arrives?

Register an endpoint with `POST /v1/webhooks` and subscribe to `data.written`. Each event is
signed, names the user, provider and days that changed, and carries no health value. See
[Get a webhook when new wearable data arrives](how-to/webhooks-new-data.md).

## How do I read Bluetooth health devices without a phone?

Run `ohs-bridge` on a Mac, a Linux machine, a Raspberry Pi or a Windows PC near the device. It
reads standard Bluetooth heart rate, blood pressure, scale, glucose, thermometer and pulse
oximeter profiles and uploads the readings. See
[Record a Bluetooth heart rate strap, blood pressure cuff or scale without a phone](how-to/bluetooth-devices.md).

## How do I avoid double counting when several wearables measure the same thing?

Read through the merged view, the default: it keeps one source per figure when several measured
it, and you set which source wins per metric with `PUT /v1/users/{id}/source-priorities`. See
[Combine data from several wearables](how-to/merge-multiple-wearables.md).

## How do I move from Terra to a self-hosted aggregator?

Register your own vendor apps, create one user per person, replace Terra's widget with a connect
session and its webhook destination with signed webhooks. See
[Move from the Terra API to a self-hosted aggregator](how-to/migrate-from-terra.md).

## How do I delete a user's health data?

Call `DELETE /v1/users/{id}`: it cuts the user's access at once and a background job removes their
raw files and records. Download the complete archive first if the person asked for a copy. See
[Delete a user's health data](how-to/delete-user-data.md).
