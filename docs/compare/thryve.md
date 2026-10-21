# Thryve alternative

Open Health Sync is an open source, self-hosted alternative to Thryve, the wearable data API
hosted in Germany. Both give your product one API for data from wearables and health apps.
Thryve runs that API for you from its German infrastructure and charges a monthly plan. Open
Health Sync is software you run yourself: one Go binary and Postgres, free under AGPL-3.0, in
the region and on the servers you choose, in Europe or anywhere else.

This page compares the two, says when Thryve is the better pick, and maps Thryve's concepts to
Open Health Sync for a move. Facts about Thryve come from its own site, pricing page and
documentation, checked on 2026-10-18. They change, so check [thryve.health](https://www.thryve.health)
as well.

## At a glance

| | Open Health Sync | Thryve |
|---|---|---|
| What it is | Open source software you run | A hosted wearable data API, sold as a subscription |
| License | AGPL-3.0 | Proprietary |
| Price | Free. You pay for your own server | Start Up: 499 EUR a month for 500 users. Scale Up: 1,000 EUR a month for 5,000 users. Enterprise: on request. A free trial for 25 users ([pricing](https://thryve.health/product/pricing)) |
| Where the data is kept | Your Postgres and your raw store, for as long as you decide | Thryve's infrastructure in Germany, for 2 weeks on Start Up and 1 month on Scale Up, per the pricing page |
| History when a user connects | A backfill of what each provider's API serves | Not on Start Up; up to 14 days on Scale Up |
| Sources | 18 available ([list](../connectors/README.md)) | 500+ wearables and health trackers advertised, Garmin included. Apple Health, Health Connect and Samsung Health through its SDK |
| Garmin | A Health API connector ([guide](../connectors/garmin.md)) for operators with their own Garmin app. Garmin has paused new developer access requests, per its replies to applicants. Garmin exports import as activity files | Yes, and a Garmin companion module for real-time Bluetooth data, which needs a Garmin license |
| Data model | One canonical model with named types (`heart_rate`, `step_count`), one unit per quantity, UCUM units and LOINC codes ([data model](../data-model.md)) | Epoch data and daily data, with 250+ numbered data types (`1000` steps, `3000` heart rate) |
| FHIR and Open mHealth | FHIR R4 checked with the official HL7 validator, Open mHealth checked against the official schemas ([FHIR R4](../fhir.md), [Open mHealth](../open-mhealth.md)) | Not found on its site |
| Raw payloads | Every payload kept verbatim, so a mapping fix can re-process history | Not stated |
| How you get the data | REST read API, signed webhooks, CSV and Parquet exports, or SQL on your own Postgres | Data push webhooks that carry the data, notification webhooks, and API requests |
| AI agents | Read-only MCP server on your instance, over stdio or Streamable HTTP ([MCP](../mcp.md)) | Not found on its site |
| Mobile SDKs | Swift, Kotlin, React Native and Flutter, installed from the repository for now ([SDKs](../sdks/README.md)) | Swift, Kotlin, React Native and Flutter |
| Vendor developer apps | You register your own app with each vendor | Thryve's shared OAuth clients by default. Your own from Scale Up |
| Compliance | Not certified. It depends on how you run it ([security model](../security-model.md)) | GDPR and HIPAA, ISO 27001 and ISO 9001 certified, per Thryve |
| Runs on | Linux, macOS, Docker, Kubernetes, a Raspberry Pi 5 | Thryve's cloud in Germany |

## What it costs

Thryve's Start Up plan costs 499 EUR a month for 500 users and its Scale Up plan 1,000 EUR a
month for 5,000 users, with more users sold on top. Both plans keep the data at Thryve for a
short window (2 weeks and 1 month), so your product stores its own copy.

Open Health Sync costs nothing to use. You pay for the server it runs on, and for any vendor
program that charges developers (some partner programs do). The database you run is the copy,
for as long as you keep it. It idles in about 22 MB of memory and runs on a Raspberry Pi 5 with
4 GB ([benchmarks](../benchmarks/ingest.md)).

## Pick Thryve when

- **You want an EU provider to run it**: Thryve hosts everything in Germany and holds ISO 27001
  and ISO 9001 certificates, so you have no server of your own to secure and audit.
- **You need Garmin without a Garmin app of your own**, or trackers beyond the 18 sources here, under one contract.
- **You need real-time Garmin data in your app** through its Garmin companion module.
- **You do not want to register a developer app with each vendor**: Thryve's shared OAuth
  clients cover that until you want your own.

## Pick Open Health Sync when

- **The data has to stay on infrastructure you choose**, in your own cloud account, your own
  data center, or a country other than Germany.
- **You want the history kept**: every record and every original payload stays in your database,
  so a fix in a mapper re-processes history instead of losing it.
- **Per-user fees do not fit**: a free tier for your users, a research study, a non-profit, or
  your own data on a home server.
- **You need standard output**: FHIR R4 for a clinical or research system, Open mHealth for
  analysis tools, Parquet for a data warehouse.
- **You want to read and change the code**: see how a value was converted, fix a mapper, add a
  source.
- **You want an AI agent to read the data** through a read-only MCP server you control.

## Moving from Thryve

The main concepts have a direct counterpart:

| Thryve | Open Health Sync |
|---|---|
| An end user (`endUserId`, with your `endUserAlias`) | A user (`POST /v1/users` with your own `external_ref`), with any number of connections ([API](../api.md)) |
| The data source connection widget | A connect session (`POST /v1/users/{id}/connect-sessions`), a hosted page that lists the providers ([hosted connection page](../connect-page.md)) |
| Data push and notification webhooks | `data.written` names the user, provider and days that changed; your server then reads them ([webhooks](../webhooks.md)) |
| Epoch data, by numbered data type (`1000` steps, `3000` heart rate) | `samples`, by named type (`step_count`, `heart_rate`), in one unit per type |
| Daily data | `daily`: one summary per local calendar day |
| Sleep data types (`2000` sleep duration and the others) | `sleep`, and each session's `phases` |
| Native sources through the SDK (Apple Health, Health Connect) | The [Swift and Kotlin SDKs](../sdks/README.md), or the upload contract |

For each cloud provider, register your own developer app and set its keys: every connector guide
starts with that step ([connectors](../connectors/README.md)).

## Try it

The [quickstart](../../README.md#quickstart) starts an instance with Docker Compose and fills it
with 90 days of synthetic data from six sources, so you can call every route before you connect
a real account.

See also: [all the options compared](README.md) and [Terra API alternative](terra.md).
