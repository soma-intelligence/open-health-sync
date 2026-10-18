# ROOK alternative

Open Health Sync is an open source, self-hosted alternative to ROOK for wearable data. Both give
your product one API for data from wearables and health apps. ROOK runs that API for you, adds
lab results, medical devices and health records next to it, and charges a monthly plan. Open
Health Sync is software you run yourself: one Go binary and Postgres, free under AGPL-3.0, with
the data on servers you choose. It has no lab or health record data today.

This page compares the two and says when ROOK is the better pick. Facts about ROOK come from its
own site and pricing page, checked on 2026-10-18. They change, so check
[tryrook.io](https://tryrook.io) as well.

## At a glance

| | Open Health Sync | ROOK |
|---|---|---|
| What it is | Open source software you run | A hosted health data aggregation platform, sold as a subscription |
| License | AGPL-3.0 | Proprietary |
| Price | Free. You pay for your own server | Core: $399 a month for up to 750 active users. Core+: $999 for up to 5,000. Business: $1,999 for up to 15,000. Enterprise on request. Lab data billed apart, from $249 a month for 300 files ([pricing](https://www.tryrook.io/pricing)) |
| Where the data is kept | Your Postgres and your raw store, for as long as you decide | ROOK's cloud. Dedicated servers on Enterprise |
| Sources | 13 available, 3 more on main for the next release ([list](../connectors/README.md)) | 500+ devices through one integration advertised (its site also lists 72 data sources), plus medical devices |
| Garmin | Not available: Garmin's developer program is not taking new applications. Garmin exports import as activity files (next release) | Yes |
| Lab results | No | Lab Data API, with biomarkers coded in LOINC and FHIR R4 output |
| Health records (EHR) | Planned, after the SDKs: SMART on FHIR patient access ([connectors](../connectors/README.md)) | EHR/EMR API with FHIR R4 output, on a waitlist on the pricing page |
| Data model | One canonical model, one unit per quantity, UCUM units and LOINC codes ([data model](../data-model.md)) | ROOK's normalized data |
| FHIR R4 | Built in, checked with the official HL7 validator ([FHIR R4](../fhir.md)) | "Clinical Ready (FHIR R4)", an add-on on Core, included from Business |
| Open mHealth | Yes, checked against the official schemas ([Open mHealth](../open-mhealth.md)) | Not found on its site |
| Raw payloads | Every payload kept verbatim, so a mapping fix can re-process history | Not stated |
| AI agents | Read-only MCP server on your instance, over stdio or Streamable HTTP ([MCP](../mcp.md)) | Not found on its site |
| Health scores | None: it keeps the scores providers send (readiness, recovery, strain) and computes none | ROOK Score (physical, sleep and body health), as an add-on or from Business |
| Mobile SDKs | Swift, Kotlin, React Native and Flutter, installed from the repository for now ([SDKs](../sdks/README.md)) | The ROOK SDK, and the ROOK App when you cannot embed an SDK |
| Compliance | Not certified. It depends on how you run it ([security model](../security-model.md)) | A HIPAA-aligned security program with BAAs, and GDPR as a data processor, per ROOK |
| Runs on | Linux, macOS, Docker, Kubernetes, a Raspberry Pi 5 | ROOK's cloud |

## What it costs

ROOK's Core plan costs $399 a month for up to 750 active users, and Core+ $999 for up to 5,000.
Past a plan's limit, billing follows usage at a rate the pricing page does not list. Lab data is
billed by the number of files a month. ROOK offers a 30-day pilot and month to month plans.

Open Health Sync costs nothing to use. You pay for the server it runs on, and for any vendor
program that charges developers (some partner programs do). It idles in about 22 MB of memory
and runs on a Raspberry Pi 5 with 4 GB ([benchmarks](../benchmarks/open-wearables.md)).

## Pick ROOK when

- **You need lab results or health records next to wearable data**, in one API, with FHIR R4
  output.
- **You need Garmin today**, or devices beyond the 13 sources here, under one contract.
- **Your users cannot install your app**: the ROOK App collects their data instead.
- **You want ready-made health scores and threshold alerts** (ROOK Score and ROOK Signals).
- **You do not want to run a server**, or to register a developer app with each vendor.

## Pick Open Health Sync when

- **The data has to stay on infrastructure you choose**, for privacy, data residency or a
  regulator.
- **Per-user fees do not fit**: a free tier for your users, a research study, a non-profit, or
  your own data on a home server.
- **You need standard output without add-ons**: FHIR R4, Open mHealth and Parquet are built
  in.
- **You want every original payload kept**, so a fix in a mapper re-processes history instead of
  losing it.
- **You want to read and change the code**: see how a value was converted, fix a mapper, add a
  source.
- **You want an AI agent to read the data** through a read-only MCP server you control.

## Moving from ROOK

ROOK's public pages describe its products but not their routes, so there is no route by route
mapping here. In Open Health Sync, each person is a user (`POST /v1/users` with your own
`external_ref`), connects providers through a hosted page
(`POST /v1/users/{id}/connect-sessions`), and their records are read from `samples`, `sleep`,
`workouts` and `daily` under `GET /v1/users/{id}/...` ([API](../api.md)). The `data.written`
webhook tells your server what changed ([webhooks](../webhooks.md)). For each cloud provider,
register your own developer app and set its keys ([connectors](../connectors/README.md)).

## Try it

The [quickstart](../../README.md#quickstart) starts an instance with Docker Compose and fills it
with 90 days of synthetic data from six sources, so you can call every route before you connect
a real account.

See also: [all the options compared](README.md), [Terra API alternative](terra.md) and
[Open Health Sync vs Open Wearables](open-wearables.md).
