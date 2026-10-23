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
