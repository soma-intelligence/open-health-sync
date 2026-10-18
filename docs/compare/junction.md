# Junction alternative

Open Health Sync is an open source, self-hosted alternative to Junction, the health data API
formerly called Vital. Both give your product one API for data from wearables and health apps.
Junction runs that API for you, sells lab testing next to it, and charges a monthly plan. Open
Health Sync is software you run yourself: one Go binary and Postgres, free under AGPL-3.0, with
the data on servers you choose. It does no lab testing.

This page compares the two, says when Junction is the better pick, and maps Junction's concepts
to Open Health Sync for a move. Facts about Junction come from its own site, pricing page and
documentation, checked on 2026-10-18. They change, so check [junction.com](https://www.junction.com)
as well.

## At a glance

| | Open Health Sync | Junction |
|---|---|---|
| What it is | Open source software you run | A hosted API for wearable data and lab testing, sold as a subscription |
| License | AGPL-3.0 | Proprietary |
| Price | Free. You pay for your own server | Launch: $300 a month, billed monthly, for 1 to 500 connected users. Enterprise: custom, billed annually, unlimited users ([pricing](https://www.junction.com/pricing)) |
| Where the data is kept | Your Postgres and your raw store, for as long as you decide | Junction's cloud, in a US or an EU region ([quickstart](https://docs.junction.com/home/quickstart)) |
| Sources | 13 available, 3 more on main for the next release ([list](../connectors/README.md)) | 300+ devices advertised: cloud APIs such as Garmin, Fitbit, Oura, WHOOP, Withings, Polar and Dexcom, Apple HealthKit, Health Connect and Samsung Health through its mobile SDKs, and Bluetooth meters ([providers](https://docs.junction.com/wearables/providers/introduction)) |
| Garmin | Planned. Garmin has paused new developer access requests, per its replies to applicants. Garmin exports import as activity files (next release) | Yes |
| Lab testing | No | A lab testing API, at-home kits and phlebotomy, on Enterprise |
| Data model | One canonical model, one unit per quantity, UCUM units and LOINC codes ([data model](../data-model.md)) | Junction's own resources, such as the sleep summary at `/summary/sleep` |
| Raw payloads | Every payload kept verbatim, so a mapping fix can re-process history | Raw provider data through routes such as `/v2/summary/sleep/{user_id}/raw` |
| FHIR and Open mHealth | FHIR R4 checked with the official HL7 validator, Open mHealth checked against the official schemas ([FHIR R4](../fhir.md), [Open mHealth](../open-mhealth.md)) | Not in its documentation index |
| How you get the data | REST read API, signed webhooks, CSV and Parquet exports, or SQL on your own Postgres | REST requests, webhooks such as `daily.data.workouts.created`, and ETL pipelines to your warehouse on Enterprise |
| AI agents | Read-only MCP server on your instance, over stdio or Streamable HTTP ([MCP](../mcp.md)) | Not in its documentation index |
| SDKs | Swift, Kotlin, React Native and Flutter for apps; TypeScript and Python for your backend (next release). Installed from the repository for now ([SDKs](../sdks/README.md)) | iOS, Android and Flutter for apps; Python, TypeScript, Java and Go for your backend |
| Vendor developer apps | You register your own app with each vendor | Junction's by default. Custom OAuth credentials on Enterprise |
| Compliance | Not certified. It depends on how you run it ([security model](../security-model.md)) | HIPAA compliant and GDPR ready on every plan; a BAA, SOC 2 Type 2 and ISO 27001 on Enterprise, per Junction |
| Runs on | Linux, macOS, Docker, Kubernetes, a Raspberry Pi 5 | Junction's cloud |

## What it costs

Junction's Launch plan costs $300 a month for up to 500 connected users. Past 500, the pricing
page sends you to sales: the Enterprise price is not public.

Open Health Sync costs nothing to use. You pay for the server it runs on, and for any vendor
program that charges developers (some partner programs do). It idles in about 22 MB of memory
and runs on a Raspberry Pi 5 with 4 GB ([benchmarks](../benchmarks/open-wearables.md)).

## Pick Junction when

- **You need lab tests as well as wearable data.** Junction runs a lab testing API with a lab
  network, at-home kits and phlebotomy. Open Health Sync has no lab offering.
- **You need Garmin today**, or sources beyond the 13 here, such as Peloton, Zwift, Eight Sleep
  or Abbott LibreView, under one contract.
- **You do not want to run a server**, or to register a developer app with each vendor.
- **You need your vendor's attestations**: SOC 2 Type 2, ISO 27001 and a BAA, on Enterprise.
- **You want the data held in a US or an EU region** without operating anything yourself.

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
- **You want an AI agent to read the data** through a read-only MCP server you control.

## Moving from Junction

The main concepts have a direct counterpart:

| Junction | Open Health Sync |
|---|---|
| A user (`POST /v2/user/` with your `client_user_id`) | A user (`POST /v1/users` with your own `external_ref`), with any number of connections ([API](../api.md)) |
| A link token and the Link widget | A connect session (`POST /v1/users/{id}/connect-sessions`), a hosted page that lists the providers ([hosted connection page](../connect-page.md)) |
| Webhooks (`daily.data.<resource>.created`) | Outgoing webhooks: `data.written` names the user, provider and days that changed ([webhooks](../webhooks.md)) |
| Summary resources (`/summary/sleep` and the others) | `sleep`, `workouts`, `daily` and `samples` under `GET /v1/users/{id}/...` |
| Raw provider data (`.../raw`) | The raw store keeps every payload; the [user archive](../exports.md#complete-user-archive-data-portability) hands it over |
| Warehouse ETL | The data already sits in your Postgres. Exports as CSV or Parquet ([exports](../exports.md)) |
| Mobile SDK for HealthKit and Health Connect | The [Swift and Kotlin SDKs](../sdks/README.md), or the upload contract |

For each cloud provider, register your own developer app and set its keys: every connector guide
starts with that step ([connectors](../connectors/README.md)).

## Try it

The [quickstart](../../README.md#quickstart) starts an instance with Docker Compose and fills it
with 90 days of synthetic data from six sources, so you can call every route before you connect
a real account.

See also: [all the options compared](README.md), [Terra API alternative](terra.md) and
[Open Health Sync vs Open Wearables](open-wearables.md).
