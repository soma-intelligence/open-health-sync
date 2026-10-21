# Terra API alternative

Open Health Sync is an open source, self-hosted alternative to the Terra API. Both give your
product one API for data from wearables and health apps. Terra runs that API for you, as a
hosted service with a monthly subscription. Open Health Sync is software you run yourself: one
Go binary and Postgres, free under AGPL-3.0, with the data on servers you choose.

This page compares the two, says when Terra is the better pick, and maps Terra's concepts to
Open Health Sync for a move. Facts about Terra come from its own site and documentation, checked
on 2026-10-17. They change, so check [tryterra.co](https://tryterra.co) as well.

## At a glance

| | Open Health Sync | Terra API |
|---|---|---|
| What it is | Open source software you run | A hosted API, sold as a subscription |
| License | AGPL-3.0 | Proprietary |
| Price | Free. You pay for your own server | From $399 a month billed annually, or $499 billed monthly, with 100,000 credits. Each source a user connects costs 200 credits a month ([pricing](https://tryterra.co/pricing)) |
| Where the data is kept | Your Postgres and your raw store, for as long as you decide | Terra's cloud, usually for two days and at most six months, then your destination ([privacy policy](https://tryterra.co/end-user-privacy)) |
| Sources | 13 available, 4 more on main for the next release ([list](../connectors/README.md)) | 500+ wearables and health sources advertised, Garmin included |
| Garmin | Next release: a Health API connector ([guide](../connectors/garmin.md)) for operators with their own Garmin app. Garmin has paused new developer access requests, per its replies to applicants. Garmin exports import as activity files (next release) | Yes, through Terra's existing partnership |
| Data model | One canonical model, one unit per quantity, UCUM units and LOINC codes ([data model](../data-model.md)) | Terra's own JSON models: Activity, Body, Daily, Sleep, Nutrition, Menstruation and others |
| FHIR | FHIR R4, checked with the official HL7 validator in CI ([FHIR R4](../fhir.md)) | A FHIR processing option. The documentation names no FHIR version |
| Open mHealth | Yes, checked against the official schemas ([Open mHealth](../open-mhealth.md)) | Not found in its documentation |
| Raw payloads | Every payload kept verbatim, so a mapping fix can re-process history | Recent payloads in the dashboard |
| How you get the data | REST read API, signed webhooks, CSV and Parquet exports, or SQL on your own Postgres | Pushed to a destination (webhooks, Postgres, MySQL, S3, GCS, Azure Blob, SQS, Kafka), REST requests for history |
| AI agents | Read-only MCP server on your instance, over stdio or Streamable HTTP ([MCP](../mcp.md)) | Read-only MCP server ("AI Interface") |
| Mobile SDKs | Swift, Kotlin, React Native and Flutter, installed from the repository for now ([SDKs](../sdks/README.md)) | iOS, Android, React Native and Flutter |
| Vendor developer apps | You register your own app with each vendor | Terra's own for providers such as Fitbit, Oura, Polar and Withings. Yours for others, such as Strava, WHOOP and Dexcom |
| Compliance | Not certified. It depends on how you run it ([security model](../security-model.md)) | HIPAA, GDPR and SOC 2 Type II, per Terra |
| Runs on | Linux, macOS, Docker, Kubernetes, a Raspberry Pi 5 | Terra's cloud |

## What it costs

Terra's Quick Start plan includes 100,000 credits a month, and one connected source for one user
costs 200 credits a month (the first 400 events of each connection are free). By our arithmetic
from the public [pricing page](https://tryterra.co/pricing), that covers about 500 connections,
and each connection past those costs about $1 a month at the starting credit price, which Terra
lowers as usage grows. A product with 2,000 users who each connect one source would pay about
$2,000 a month at the listed starting prices, before any volume discount.

Open Health Sync costs nothing to use. You pay for the server it runs on, and for any vendor
program that charges developers (some partner programs do). It idles in about 22 MB of memory
and runs on a Raspberry Pi 5 with 4 GB ([benchmarks](../benchmarks/ingest.md)).

## Pick Terra when

- **You need Garmin today.** Garmin has not taken new developer applications since spring 2026,
  and Terra has held a Garmin integration for years.
- **You need devices beyond the 13 sources here**, such as Peloton, Zwift, Wahoo or COROS through
  their APIs, under one contract.
- **You do not want to run a server**, or to register a developer app with each vendor.
- **You need your vendor's compliance attestations**, such as SOC 2 Type II and HIPAA.
- **You also want lab results or ready-made health scores**, which Terra sells as add-ons.

## Pick Open Health Sync when

- **The data has to stay on infrastructure you choose**, for privacy, data residency or a
  regulator.
- **Per-user fees do not fit**: a free tier for your users, a research study, a non-profit, or
  your own data on a home server.
- **You need standard output**: FHIR R4 for a clinical or research system, Open mHealth for
  analysis tools, Parquet for a data warehouse.
- **You want every original payload kept**, so a fix in a mapper re-processes history instead of
  losing it.
- **You want to read and change the code**: see how a value was converted, fix a mapper, add a
  source.
- **You want it small**: one binary and Postgres, on a laptop, a VM or a Raspberry Pi.

## Moving from Terra

Most of Terra's concepts have a direct counterpart. The data types map like this:

| Terra | Open Health Sync |
|---|---|
| A Terra user (`user_id`) per connection, with your `reference_id` | One user per person (`POST /v1/users` with your own `external_ref`), with any number of connections ([API](../api.md)) |
| Widget session (`POST /api/widget/session`) | Connect session (`POST /v1/users/{id}/connect-sessions`), a hosted page that lists the providers ([hosted connection page](../connect-page.md)) |
| Webhook destination | Outgoing webhooks: `data.written` names the user, provider and days that changed ([webhooks](../webhooks.md)) |
| Database and storage destinations | The data already sits in your Postgres. Exports as CSV or Parquet ([exports](../exports.md)) |
| Activity | `workouts`, and each workout's `route` |
| Sleep | `sleep`, and each session's `phases` |
| Daily | `daily`: one summary per local calendar day |
| Body | `samples`: weight, body fat, blood pressure, glucose, heart rate and the other quantity types |
| Nutrition | `nutrition` |
| Menstruation | `category-samples` and `menstrual-cycles` |
| Historical data requests (`GET /activity`, `/sleep`, `/daily`, `/body`) | A backfill when a user connects, then reads with `start` and `end` on `GET /v1/users/{id}/<resource>` |
| FHIR processing | `GET /v1/users/{id}/fhir/Observation` and the other FHIR routes ([FHIR R4](../fhir.md)) |
| AI Interface (MCP) | `ohs mcp` for desktop agents, or `/mcp` over HTTP ([MCP](../mcp.md)) |

For each cloud provider, register your own developer app and set its keys: every connector guide
starts with that step ([connectors](../connectors/README.md)). Apple Health and Health Connect
data comes from your mobile app, through the [Swift and Kotlin SDKs](../sdks/README.md) or the
upload contract.

## Try it

The [quickstart](../../README.md#quickstart) starts an instance with Docker Compose and fills it
with 90 days of synthetic data from six sources, so you can call every route before you connect
a real account.

See also: [all the options compared](README.md), [Junction alternative](junction.md) and
[Spike API alternative](spike.md).
