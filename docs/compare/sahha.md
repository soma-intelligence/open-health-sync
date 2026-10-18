# Sahha alternative

Sahha and Open Health Sync both collect data from wearables and health apps, but they stop at
different places. Sahha is a hosted API that turns the data into health scores, biomarkers and
archetypes for your product, and charges a monthly plan. Open Health Sync is open source software
you run yourself: one Go binary and Postgres, free under AGPL-3.0. It stops at clean, traceable
data in standard formats, and computes no scores of its own.

This page compares the two and says when Sahha is the better pick. Facts about Sahha come from
its own site, pricing page and documentation, checked on 2026-10-18. They change, so check
[sahha.ai](https://sahha.ai) as well.

## At a glance

| | Open Health Sync | Sahha |
|---|---|---|
| What it is | Open source software you run | A hosted health data and scores API, sold as a subscription |
| License | AGPL-3.0 | Proprietary |
| Price | Free. You pay for your own server | Free sandbox for 30 days (25 development users). Starter: $299 a month billed annually, 1,000 users, then $0.40 a user. Pro: $1,499 a month billed annually, 10,000 users, then $0.20 a user. Enterprise on request ([pricing](https://sahha.ai/pricing)) |
| Where the data is kept | Your Postgres and your raw store, for as long as you decide | Sahha's cloud, for 30 days on Starter and 120 days on Pro |
| Sources | 13 available, 4 more on main for the next release ([list](../connectors/README.md)) | 800+ devices across 165 integrations advertised, Garmin included |
| Garmin | Next release: a Health API connector ([guide](../connectors/garmin.md)) for operators with their own Garmin app. Garmin has paused new developer access requests, per its replies to applicants. Garmin exports import as activity files (next release) | Listed, in beta, with Garmin's approval required |
| What you read | Records as measured: samples, sleep sessions and phases, workouts, daily summaries, in one unit per quantity ([data model](../data-model.md)) | Five health scores, 100+ biomarkers, 100+ archetypes and insights computed by Sahha |
| Health scores | None: it keeps the scores providers send (readiness, recovery, strain) and computes none | The product's core |
| FHIR and Open mHealth | FHIR R4 checked with the official HL7 validator, Open mHealth checked against the official schemas ([FHIR R4](../fhir.md), [Open mHealth](../open-mhealth.md)) | Not found on its site |
| Raw payloads | Every payload kept verbatim, so a mapping fix can re-process history | Not stated |
| How you get the data | REST read API, signed webhooks, CSV and Parquet exports, or SQL on your own Postgres | REST API and webhooks |
| AI agents | Read-only MCP server on your instance, over stdio or Streamable HTTP ([MCP](../mcp.md)) | Live context through MCP |
| Mobile SDKs | Swift, Kotlin, React Native and Flutter, installed from the repository for now ([SDKs](../sdks/README.md)) | iOS, Android, React Native and Flutter |
| Personal data | You hold it, so you decide what you store | No PII required, per Sahha |
| Compliance | Not certified. It depends on how you run it ([security model](../security-model.md)) | SOC 2 Type II, HIPAA and GDPR, per Sahha. A BAA from Pro |
| Runs on | Linux, macOS, Docker, Kubernetes, a Raspberry Pi 5 | Sahha's cloud |

## What it costs

Sahha's Starter plan costs $299 a month, billed annually, for 1,000 users, and $0.40 for each
user past those. By our arithmetic from the public [pricing page](https://sahha.ai/pricing), a
product with 2,000 users would pay about $699 a month on Starter.

Open Health Sync costs nothing to use. You pay for the server it runs on, and for any vendor
program that charges developers (some partner programs do). It idles in about 22 MB of memory
and runs on a Raspberry Pi 5 with 4 GB ([benchmarks](../benchmarks/open-wearables.md)).

## Pick Sahha when

- **You want scores, not raw figures**: health scores, biomarkers and archetypes you can show
  users without building the models yourself.
- **You need Garmin**, or sources beyond the 13 here, through one SDK and API.
- **You do not want to hold personal data**: Sahha works without PII.
- **You need your vendor's SOC 2 Type II report**, or a BAA on Pro.

## Pick Open Health Sync when

- **You want the measurements themselves**: every heart rate reading, sleep stage and workout,
  with its source and device, to build your own analysis or models on.
- **You want the history kept**: every record and every original payload stays in your database,
  not for a 30 or 120 day window.
- **The data has to stay on infrastructure you choose**, for privacy, data residency or a
  regulator.
- **Per-user fees do not fit**: a free tier for your users, a research study, a non-profit, or
  your own data on a home server.
- **You need standard output**: FHIR R4 for a clinical or research system, Open mHealth for
  analysis tools, Parquet for a data warehouse.
- **You want to read and change the code**: see how a value was converted, fix a mapper, add a
  source.

## Moving from Sahha

The two products answer different questions, so there is no route by route mapping. In Open
Health Sync, each person is a user (`POST /v1/users` with your own `external_ref`), phone data
comes from the [Swift and Kotlin SDKs](../sdks/README.md), cloud providers connect through a
hosted page (`POST /v1/users/{id}/connect-sessions`), and the records are read from `samples`,
`sleep`, `workouts` and `daily` ([API](../api.md)). Scores you compute yourself from those
records, or keep from the providers that send them.

## Try it

The [quickstart](../../README.md#quickstart) starts an instance with Docker Compose and fills it
with 90 days of synthetic data from six sources, so you can call every route before you connect
a real account.

See also: [all the options compared](README.md), [Terra API alternative](terra.md) and
[Open Health Sync vs Open Wearables](open-wearables.md).
