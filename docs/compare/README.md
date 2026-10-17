# Health data aggregators compared

A health data aggregator collects data from wearables and health apps (Apple Health, Health
Connect, Oura, WHOOP, Garmin, Fitbit, Withings and others) and gives your product one API for all
of them, instead of one integration per vendor. There are three ways to get one: a hosted API
such as Terra or Junction, an open source aggregator you run yourself such as Open Health Sync or
Open Wearables, or code you write and maintain.

This page compares the main options. Facts about other products come from their own sites,
pricing pages and repositories, checked on 2026-10-17. Prices and integration counts change, so
check their pages too.

## The options

| | Kind | License | Starting price | Sources it claims |
|---|---|---|---|---|
| **Open Health Sync** | Open source, self-hosted | AGPL-3.0 | Free, you pay for your server | 13 available ([list](../connectors/README.md)) |
| [Open Wearables](https://openwearables.io) | Open source, self-hosted | MIT | Free, custom Enterprise tier | 13 providers |
| [Terra](https://tryterra.co) | Hosted API | Proprietary | $399 a month billed annually, $499 monthly | 500+ wearables and sources |
| [Junction](https://junction.com) (formerly Vital) | Hosted API, plus lab testing | Proprietary | $300 a month for up to 500 users | 300+ devices |
| [Thryve](https://www.thryve.health) | Hosted API, hosted in Europe | Proprietary | 499 EUR a month for up to 500 users | 500+ trackers |
| [Spike API](https://www.spikeapi.com) | Hosted API | Proprietary | From $450 a month | 500+ integrations |
| [Sahha](https://sahha.ai) | Hosted API, with health scores | Proprietary | Free for 30 days, then from $299 a month | 165 integrations |
| [ROOK](https://www.tryrook.io) | Hosted API, with lab and EHR data | Proprietary | $399 a month for up to 750 users | 500+ devices |
| [Shimmer](https://github.com/openmhealth/shimmer) (Open mHealth) | Open source, self-hosted | Apache-2.0 | Free | 6 APIs. No commit since 2021, last release in 2017 |
| Your own code | Self-hosted | Yours | Your time | The ones you build |

Detailed comparisons:

- [Terra API alternative](terra.md): Open Health Sync and Terra, side by side, and how to move.
- [Open Health Sync vs Open Wearables](open-wearables.md): the two open source options.

## Hosted, open source or your own code

| | Open Health Sync | Hosted wearables API | Other open source aggregators | Writing it yourself |
|---|---|---|---|---|
| Where the data lives | Your server | The vendor's cloud, then yours | Your server | Your server |
| Cost | Free, plus hosting | A monthly plan, priced per connected user | Free, plus hosting | Your time |
| Standard outputs | FHIR R4, Open mHealth, CSV, Parquet, REST | Mostly their own JSON. Some add FHIR | Usually their own JSON | What you build |
| AI agents | Read-only MCP server, stdio and HTTP | Several ship an MCP server | Some ship an MCP server | What you build |
| Raw payloads kept | Always | Varies | Varies | What you build |
| Runs on a Raspberry Pi | Yes, one binary | No | Usually several services | Depends |
| Vendor developer apps | You register your own | Often theirs | You register your own | You register your own |
| Garmin | Planned | Most have it | Some have it | Needs Garmin's approval |
| Dashboard | Admin console, no end-user dashboard | Yes | Some ship one | What you build |

Pick a hosted API when you need Garmin or a long tail of devices today, do not want to run a
server, or need your vendor's SOC 2 and HIPAA attestations. Pick an open source aggregator when the
data has to stay on your infrastructure, per-user fees do not fit, or you want to read and change
the code. Pick Open Health Sync among those when standard formats (FHIR R4, Open mHealth), kept
raw payloads, several tenants on one instance, or a small footprint matter to you.

## Common questions

### Is there an open source alternative to Terra?

Yes. Open Health Sync and Open Wearables are both free, open source and self-hosted. Open Health
Sync puts standards first (FHIR R4, Open mHealth, raw payloads kept) and runs as one Go binary;
Open Wearables has the larger community, a dashboard and an MIT license. See
[Terra API alternative](terra.md) and [Open Health Sync vs Open Wearables](open-wearables.md).

### How much does a wearable data API cost?

Hosted APIs start between $300 and $500 a month and charge per connected user past what the
plan includes, as in the table above. An open source aggregator costs nothing to use: you pay for
the server, and for any vendor partner program that charges developers.

### How do I get my Apple Health data into a database?

Run Open Health Sync, then load the export the Health app writes on your iPhone (Settings,
Health, Export All Health Data) with `ohs import apple-health`. History included, it lands in
Postgres as samples, sleep, workouts and daily summaries you can query over the
[REST API](../api.md) or with SQL. For continuous sync, an iOS app uploads new HealthKit data to
the [Apple Health connector](../connectors/apple-health.md), with the
[Swift SDK](../sdks/swift.md#healthkit-sync) doing the reading.

### How do I give Claude or another AI assistant access to my health data?

Open Health Sync includes an MCP server. Mint a read-only token for one person and add the
server to Claude Desktop, Claude Code or any MCP client: the agent can read samples, sleep,
workouts and daily summaries, and cannot write or delete anything. See the
[MCP server reference](../mcp.md).

### How do I get wearable data as FHIR?

Open Health Sync serves FHIR R4 Observations (samples, blood pressure, sleep, workouts, daily
summaries) with LOINC codes and UCUM units, plus the Patient, its Devices and Provenance, checked
with the official HL7 validator. See [FHIR R4](../fhir.md).

### What happened to Open mHealth Shimmer?

Shimmer was the open source aggregator of the Open mHealth project: it read Fitbit, Withings and
a few other APIs and wrote Open mHealth data points. Its repository has had no commit since May
2021 and no release since 2017, and several of the APIs it supported no longer exist. Open
Health Sync writes the same [Open mHealth](../open-mhealth.md) data points, checked against the
official schemas, from 13 current sources.

### Which devices and apps does Open Health Sync support?

Thirteen sources are available: Apple Health, Health Connect (and through it Samsung Health and
other Android apps), Withings, Oura, WHOOP, Strava, Polar, Fitbit, Suunto, Ultrahuman,
intervals.icu, Dexcom and OMRON. Xiaomi, FIT, GPX and TCX activity files, and standard Bluetooth
health devices are on main for the next release. Garmin and others are planned. The
[connectors page](../connectors/README.md) lists the status and integration path of each.

## Next

- [Quickstart](../../README.md#quickstart): a running instance with synthetic data in a few
  minutes.
- [Architecture](../architecture.md): the pipeline from raw payload to clean record.
