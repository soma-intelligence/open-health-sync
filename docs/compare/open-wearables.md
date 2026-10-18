# Open Health Sync vs Open Wearables

Open Wearables and Open Health Sync are both open source, self-hosted APIs for wearable and
health data, and both are free alternatives to hosted APIs such as [Terra](terra.md). Open
Wearables came first and has the larger community: about 2,700 GitHub stars, regular releases
(0.10.0 on 2026-10-16), a web dashboard and an MIT license. Open Health Sync puts standards
first: FHIR R4 and Open mHealth output checked against the official validators, every raw
payload kept, and one Go binary that runs on a Raspberry Pi.

Facts about Open Wearables come from its repository and documentation, checked on 2026-10-17.
Both projects move fast, so check [openwearables.io](https://openwearables.io) as well.

## At a glance

| | Open Health Sync | Open Wearables |
|---|---|---|
| License | AGPL-3.0 | MIT |
| Stack | One Go binary, Postgres with TimescaleDB | Python (FastAPI) API and worker, SvelteKit dashboard, Postgres and Redis |
| Cloud sources | Withings, Oura, WHOOP, Fitbit (Google Health API), Polar, Strava, Suunto, Ultrahuman, Dexcom, OMRON, intervals.icu | Garmin, Oura, WHOOP, Suunto, Polar, Ultrahuman, Strava, Fitbit, Withings, Google Health |
| On-device sources | Apple Health, Health Connect (Samsung Health and other Android apps through it) | Apple Health, Health Connect, Samsung Health |
| Next release | Garmin, Xiaomi, FIT/GPX/TCX activity files, Bluetooth health devices | Coros and Xiaomi listed as coming soon |
| Garmin | Next release: a Health API connector ([guide](../connectors/garmin.md)) for operators with their own Garmin app. Garmin exports import as activity files (next release) | Listed as available |
| Standard outputs | FHIR R4, Open mHealth, CSV, Parquet, alongside REST | REST and webhooks. FHIR and Open mHealth not found in its documentation |
| AI agents | Read-only MCP server over stdio and Streamable HTTP | MCP server over stdio |
| Dashboard | Admin console for the pipeline, no end-user dashboard | Web dashboard |
| Health scores | None: it stops at clean, traceable data | Open sleep, HRV, recovery and VO2 max algorithms, per its site |
| Tenancy | Several tenants per instance | One organization per deployment |
| Mobile SDKs | Swift, Kotlin, React Native, Flutter | Swift, Kotlin, Flutter, React Native |

## Performance

On 2026-10-11 we ran both on the same machine with the same Apple Health data. Open Health Sync
stored the records 2 to 3 times faster end to end and ran in 22 times less memory idle. Open
Wearables stored the data in 3.4 times less disk. The method, the scripts and the raw results are
in [the benchmark](../benchmarks/open-wearables.md), which anyone can run again. These are
numbers for one user, one machine and a synthetic dataset.

## Pick Open Wearables when

- **You need Garmin or the Samsung Health SDK path** and have the vendor access they require.
- **You want a dashboard and health scores out of the box**, not only an API.
- **The MIT license matters to you**: with AGPL-3.0, a modified Open Health Sync run as a network
  service has to publish its changes under the same license.
- **Your team works in Python**, or you want the larger community today.

## Pick Open Health Sync when

- **You need standard output**: FHIR R4 Observations with Provenance for a clinical or research
  system ([FHIR R4](../fhir.md)), Open mHealth data points for analysis tools
  ([Open mHealth](../open-mhealth.md)), Parquet for a warehouse ([exports](../exports.md)).
- **You want it small**: one binary and Postgres, about 22 MB of memory idle, on a laptop or a
  Raspberry Pi 5 with 4 GB.
- **You serve several teams or apps from one instance**, each with its own tenant and keys.
- **You want every raw payload kept**, so a mapping fix re-processes history.
- **You need consent per source and data category, an audit log of reads, or share links** a
  person can send to a clinician ([consent](../consent.md), [audit log](../audit-log.md),
  [share links](../share-links.md)).
- **You want an AI agent to read the data over HTTP** as well as stdio, with a token that cannot
  write ([MCP](../mcp.md)).

## Try it

The [quickstart](../../README.md#quickstart) starts an instance with Docker Compose and fills it
with 90 days of synthetic data from six sources.

See also: [all the options compared](README.md) and [Terra API alternative](terra.md).
