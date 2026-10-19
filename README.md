<a href="#quickstart">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/readme/hero-dark.png">
  <img src="docs/assets/readme/hero-light.png" width="100%" alt="Open Health Sync: thirteen sources flow into one Go binary (background sync, raw store, normalize, Postgres). All thirteen are in v0.1.0: Apple Health, Withings, Health Connect, Oura, WHOOP, Fitbit, Polar, Strava, Suunto, Ultrahuman, Dexcom, OMRON and intervals.icu. Outputs, all in v0.1.0: REST API, read-only MCP, FHIR R4, Open mHealth, CSV/Parquet exports and outgoing webhooks. Garmin, Coros and Samsung Health via Health Connect are planned. The JSON shown is a real heart rate sample read back from the API.">
</picture>
</a>

<div align="center">

# Open Health Sync

## Every wearable. One API. Your server.

**Stop writing a parser per device.** Plug in Apple Health, Oura, WHOOP, Fitbit, Withings and eight more.<br>
Get one clean model back, over REST, FHIR R4 and MCP. Free and open source.

[![Release](https://img.shields.io/github/v/release/soma-intelligence/open-health-sync?color=0e9f8e)](https://github.com/soma-intelligence/open-health-sync/releases)
[![CI](https://github.com/soma-intelligence/open-health-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/soma-intelligence/open-health-sync/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-555)](LICENSE)
[![Hosted beta: sign up free](https://img.shields.io/badge/hosted_beta-sign_up_free-0e9f8e)](https://app.openhealthsync.com/portal/signup?utm_source=github&utm_medium=readme&utm_campaign=badge)

[**Start in 60 seconds**](#quickstart) &nbsp;&middot;&nbsp; [**Try the hosted beta free →**](https://app.openhealthsync.com/portal/signup?utm_source=github&utm_medium=readme&utm_campaign=hero) &nbsp;&middot;&nbsp; [Docs](#learn-more)

</div>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/demo/demo-dark.gif">
  <img src="docs/demo/demo-light.gif" width="100%" alt="Terminal recording of the quickstart. docker compose up starts ohs and Postgres; ohs seed --demo writes 90 days of synthetic data from Oura, WHOOP, Withings, Fitbit, Polar and Apple Health, 44144 rows. The daily endpoint returns one day with 8132 steps from apple_health, a resting heart rate of 53 and a sleep score of 62 from oura, and the source of every figure. One heart rate reading of 70 bpm comes back from /samples, as a FHIR R4 Observation with LOINC 8867-4 and UCUM /min, and as an Open mHealth heart-rate data point in beats/min. tools/list on the MCP endpoint lists sixteen read-only tools.">
</picture>

<div align="center"><sub>Real recording, v0.1.0, synthetic data. 90 days from six devices, merged, then read as REST, FHIR R4, Open mHealth and MCP.</sub></div>

<br>

<table>
<tr>
<td width="33%" valign="top">

### 🔌 13 sources, 1 model
Heart rate is `bpm`. Whatever the device called it. Every record keeps its provider, device and time zone.

</td>
<td width="33%" valign="top">

### ⚡ Always fresh
Webhooks pull only what changed. A backstop pull catches the rest. No sync button, ever.

</td>
<td width="33%" valign="top">

### 🤖 AI agent ready
A read-only MCP server ships in the box. Point Claude or any agent at your own health data.

</td>
</tr>
<tr>
<td valign="top">

### 🏥 Standards out
OpenAPI 3.1, FHIR R4 checked against the official HL7 validator, Open mHealth, CSV, Parquet, signed webhooks.

</td>
<td valign="top">

### 🧾 Nothing lost
Every raw payload kept. Fix a mapper, replay a user. Re-sent uploads de-duplicated.

</td>
<td valign="top">

### 🔒 Yours to keep
Self-hosted, no call home. One call hard-deletes a user. Consent per data category.

</td>
</tr>
</table>

## Works with

<p align="center">
<a href="docs/connectors/apple-health.md"><img alt="Apple Health" src="https://img.shields.io/badge/Apple_Health-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/health-connect.md"><img alt="Health Connect" src="https://img.shields.io/badge/Health_Connect-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/oura.md"><img alt="Oura" src="https://img.shields.io/badge/Oura-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/whoop.md"><img alt="WHOOP" src="https://img.shields.io/badge/WHOOP-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/fitbit.md"><img alt="Fitbit" src="https://img.shields.io/badge/Fitbit-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/withings.md"><img alt="Withings" src="https://img.shields.io/badge/Withings-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/polar.md"><img alt="Polar" src="https://img.shields.io/badge/Polar-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/strava.md"><img alt="Strava" src="https://img.shields.io/badge/Strava-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/suunto.md"><img alt="Suunto" src="https://img.shields.io/badge/Suunto-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/ultrahuman.md"><img alt="Ultrahuman" src="https://img.shields.io/badge/Ultrahuman-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/dexcom.md"><img alt="Dexcom" src="https://img.shields.io/badge/Dexcom-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/omron.md"><img alt="OMRON" src="https://img.shields.io/badge/OMRON-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/intervals-icu.md"><img alt="intervals.icu" src="https://img.shields.io/badge/intervals.icu-✓-0e9f8e?style=for-the-badge"></a>
<a href="docs/connectors/garmin.md"><img alt="Garmin: next release" src="https://img.shields.io/badge/Garmin-next_release-3d7ea6?style=for-the-badge"></a>
</p>

<p align="center"><sub>All in v0.1.0 except Garmin. Eight Sleep, Xiaomi and Bluetooth devices land in the next release too. <a href="docs/connectors/README.md">Every source and its status</a>.</sub></p>

## Quickstart

Docker with Compose v2.24+, plus `curl`, `jq` and `openssl`.

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs seed --demo
```

That's it. 90 days of synthetic data from six devices, plus a demo key and calls to try.

**Going for real?** Create a tenant key, a user and a token, then upload and read back:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs admin tenant create --name dev
export OHS=http://localhost:8080 KEY=ohs_sk_...    # the key printed above

USER_ID=$(curl -s -X POST $OHS/v1/users -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"demo"}' | jq -r .id)
TOKEN=$(curl -s -X POST $OHS/v1/users/$USER_ID/tokens -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload"]}' | jq -r .token)

curl -s "$OHS/v1/users/$USER_ID/samples?type=heart_rate" -H "Authorization: Bearer $KEY"
```

Upload contract, Health app export import, provider keys, Raspberry Pi, S3, Helm:
[first sync](docs/learn/first-sync.md) and [self-hosting](docs/self-hosting.md).

## Give your AI agent your health data

```json
{
  "mcpServers": {
    "open-health-sync": {
      "command": "/usr/local/bin/ohs",
      "args": ["mcp"],
      "env": { "OHS_MCP_URL": "http://localhost:8080", "OHS_MCP_KEY": "ohs_ut_..." }
    }
  }
}
```

A user token with only the `read` scope. It can read, never write. [MCP guide](docs/mcp.md).

## Why not Terra?

| | Open source | Price | FHIR R4 | Data lives |
|---|:---:|---|:---:|---|
| **Open Health Sync** | ✅ | **Free** | ✅ validated | **Your server** |
| [Terra](docs/compare/terra.md) | ❌ | from $399 / month | an option | Their cloud |
| [Junction](docs/compare/junction.md) | ❌ | from $300 / month | not listed | Their cloud |
| [Thryve](docs/compare/thryve.md) | ❌ | from 499 EUR / month | not listed | Their servers |

<sub>Vendor facts from their own sites, checked 2026-10-17. Need Garmin or a long tail of devices today? A hosted API may suit you better. <a href="docs/compare/README.md">Full comparison</a>.</sub>

## Rather not run a server?

**The hosted beta is the same API, run for you.** Key in a minute, demo data on the first call, code that moves to your own instance with a new URL.

[**Get started for free →**](https://app.openhealthsync.com/portal/signup?utm_source=github&utm_medium=readme&utm_campaign=section) &nbsp;&middot;&nbsp; [▶ 75-second tour](docs/demo/platform-tour.mp4)

<sub>Free beta, shared capacity, test and demo data only for now.</sub>

## Learn more

[API](docs/api.md) &nbsp;&middot;&nbsp; [Data model](docs/data-model.md) &nbsp;&middot;&nbsp; [FHIR](docs/fhir.md) &nbsp;&middot;&nbsp; [Webhooks](docs/webhooks.md) &nbsp;&middot;&nbsp; [SDKs](docs/sdks/README.md) &nbsp;&middot;&nbsp; [Examples](examples/README.md) &nbsp;&middot;&nbsp; [Architecture](docs/architecture.md) &nbsp;&middot;&nbsp; [Security model](docs/security-model.md) &nbsp;&middot;&nbsp; [Changelog](CHANGELOG.md)

<sub><b>v0.1.0, early preview.</b> APIs and schema may change in any 0.x release. Not a medical device: it moves data, it does not diagnose. Not HIPAA or GDPR certified on its own; see the <a href="docs/hardening.md">hardening checklist</a>.</sub>

## Contributing

Own a wearable? Record your account in five minutes with `make record PROVIDER=oura` ([#60](https://github.com/soma-intelligence/open-health-sync/issues/60)).
Or grab a [good first issue](https://github.com/soma-intelligence/open-health-sync/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22good%20first%20issue%22). First PRs get a review within 48 hours. [CONTRIBUTING.md](CONTRIBUTING.md).

<a href="https://github.com/soma-intelligence/open-health-sync/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=soma-intelligence/open-health-sync" alt="Contributors to Open Health Sync">
</a>

<br><br>

<div align="center">

**If it saves you one parser, [⭐ star it](https://github.com/soma-intelligence/open-health-sync/stargazers).**

<sub>AGPL-3.0-only. Security reports: <a href="SECURITY.md">SECURITY.md</a>.</sub>

</div>
