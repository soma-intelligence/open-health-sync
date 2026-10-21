# Spike API alternative

Open Health Sync is an open source, self-hosted alternative to the Spike API. Both give your
product one API for data from wearables and health apps, and both offer an MCP server for AI
agents. Spike runs that API for you, sells nutrition photo analysis and lab report products next
to it, and charges a monthly plan. Open Health Sync is software you run yourself: one Go binary
and Postgres, free under AGPL-3.0, with the data on servers you choose.

This page compares the two and says when Spike is the better pick. Facts about Spike come from
its own site, pricing page and documentation, checked on 2026-10-18. They change, so check
[spikeapi.com](https://www.spikeapi.com) as well.

## At a glance

| | Open Health Sync | Spike API |
|---|---|---|
| What it is | Open source software you run | A hosted wearables and IoT API, sold as a subscription |
| License | AGPL-3.0 | Proprietary |
| Price | Free. You pay for your own server | Wearables and IoT API from $450 a month (Sandbox plan), Enterprise on request. Nutrition Scanner AI from $450 a month ([pricing](https://spikeapi.com/pricing)) |
| Where the data is kept | Your Postgres and your raw store, for as long as you decide | Spike's cloud, with retention settings in its admin console |
| Sources | 13 available, 4 more on main for the next release ([list](../connectors/README.md)) | 40+ providers and 500+ devices advertised, Garmin included, plus medical IoT devices |
| Garmin | Next release: a Health API connector ([guide](../connectors/garmin.md)) for operators with their own Garmin app. Garmin has paused new developer access requests, per its replies to applicants. Garmin exports import as activity files (next release) | Yes |
| Data model | One canonical model, one unit per quantity, UCUM units and LOINC codes ([data model](../data-model.md)) | Spike's standardized data: statistics, sleep, workouts, samples and provider records |
| FHIR and Open mHealth | FHIR R4 checked with the official HL7 validator, Open mHealth checked against the official schemas ([FHIR R4](../fhir.md), [Open mHealth](../open-mhealth.md)) | Not found in its documentation |
| Raw payloads | Every payload kept verbatim, so a mapping fix can re-process history | Provider records, next to the normalized data |
| How you get the data | REST read API, signed webhooks, CSV and Parquet exports, or SQL on your own Postgres | REST requests and webhooks |
| AI agents | Read-only MCP server on your instance, over stdio or Streamable HTTP ([MCP](../mcp.md)) | An MCP server, with guides for desktop chat clients. "MCP-compatible" data is listed on Enterprise |
| Health scores | None: it keeps the scores providers send (readiness, recovery, strain) and computes none | Sleep, stress and activity scores (Spike Analytics), on Enterprise |
| Mobile SDKs | Swift, Kotlin, React Native and Flutter, installed from the repository for now ([SDKs](../sdks/README.md)) | iOS, Android (with Samsung Health), React Native and Flutter |
| Other products | None | Nutrition Scanner AI (calories and nutrients from a photo) and Lab Reports |
| Compliance | Not certified. It depends on how you run it ([security model](../security-model.md)) | GDPR, HIPAA and CCPA, per Spike |
| Runs on | Linux, macOS, Docker, Kubernetes, a Raspberry Pi 5 | Spike's cloud |

## What it costs

Spike's wearables API starts from $450 a month. The pricing page lists no number of users for
that price and no overage price: Spike sets them with you. Each plan includes a dedicated
implementation engineer.

Open Health Sync costs nothing to use. You pay for the server it runs on, and for any vendor
program that charges developers (some partner programs do). It idles in about 22 MB of memory
and runs on a Raspberry Pi 5 with 4 GB ([benchmarks](../benchmarks/ingest.md)).

## Pick Spike when

- **You need Garmin today**, or devices beyond the 13 sources here, including medical IoT
  devices, under one contract.
- **You also want nutrition from photos, or its Lab Reports product**, from the same vendor.
- **You want ready-made sleep, stress and activity scores** rather than raw figures.
- **You do not want to run a server**, and want an implementation engineer to help you ship.

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
- **You want the MCP server on your own machine**, with a token that can only read one person's
  data, and no third party between the agent and the database.

## Moving from Spike

Spike's public documentation names its concepts but not enough of its routes to map them one by
one. In Open Health Sync, each person is a user (`POST /v1/users` with your own `external_ref`),
connects providers through a hosted page (`POST /v1/users/{id}/connect-sessions`), and their
records are read from `samples`, `sleep`, `workouts` and `daily` under `GET /v1/users/{id}/...`
([API](../api.md)). The `data.written` webhook tells your server what changed
([webhooks](../webhooks.md)). For each cloud provider, register your own developer app and set its
keys ([connectors](../connectors/README.md)).

## Try it

The [quickstart](../../README.md#quickstart) starts an instance with Docker Compose and fills it
with 90 days of synthetic data from six sources, so you can call every route before you connect
a real account.

See also: [all the options compared](README.md) and [Terra API alternative](terra.md).
