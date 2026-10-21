<p align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/readme/koi-hero-dark.gif">
  <img src="docs/assets/readme/koi-hero-light.gif" width="100%" alt="Sync health data in peace. Koi drawn in characters swim around the line, in a seamless loop.">
</picture>
</p>

<div align="center">

# Open Health Sync

### One open API for wearable and health data. On your own server.

[![CI](https://github.com/soma-intelligence/open-health-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/soma-intelligence/open-health-sync/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/soma-intelligence/open-health-sync?color=0e9f8e)](https://github.com/soma-intelligence/open-health-sync/releases)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-555)](LICENSE)
[![Hosted beta: sign up free](https://img.shields.io/badge/hosted_beta-sign_up_free-0e9f8e)](https://app.openhealthsync.com/portal/signup?utm_source=github&utm_medium=readme&utm_campaign=badge)

<sub><b>v0.1.0, early preview.</b> APIs and schema may change in any 0.x release.</sub>

</div>

Open Health Sync (`ohs`) pulls health data from devices and apps, keeps every raw payload, maps it
to one model with one set of units, and serves it over REST, FHIR R4, Open mHealth and a read-only
MCP server. Free, self-hosted, one Go binary plus Postgres. Runs on a Raspberry Pi.

**Sources:** Apple Health, Health Connect, Withings, Oura, WHOOP, Fitbit, Polar, Strava, Suunto,
Ultrahuman, intervals.icu, Dexcom, OMRON. Garmin next. [All sources](docs/connectors/README.md).

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/demo/demo-dark.gif">
  <img src="docs/demo/demo-light.gif" width="100%" alt="Terminal recording of the quickstart: start the stack, load 90 days of synthetic data from six sources, then read one day of the merged view, one heart rate reading as REST, FHIR R4 and Open mHealth, and the MCP tools.">
</picture>

## Quickstart

Needs Docker Compose v2.24+, `curl`, `jq` and `openssl`.

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs seed --demo
```

`ohs seed --demo` loads 90 days of synthetic data and prints an API key and calls to try.
Real setup: [self-hosting](docs/self-hosting.md). Rather not run a server? Try the
[hosted beta](https://app.openhealthsync.com/portal/signup?utm_source=github&utm_medium=readme&utm_campaign=hero).

## Connect an AI agent (MCP)

Install `ohs` from the [releases page](https://github.com/soma-intelligence/open-health-sync/releases),
mint a user token with the `read` scope, then:

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

## Docs

- [Self-hosting](docs/self-hosting.md) and [hardening](docs/hardening.md)
- [API guide](docs/api.md) and [OpenAPI file](api/openapi.yaml)
- [Data model](docs/data-model.md), [FHIR R4](docs/fhir.md), [webhooks](docs/webhooks.md), [MCP](docs/mcp.md)
- [Connectors](docs/connectors/README.md) and [SDKs](docs/sdks/README.md)
- [Architecture](docs/architecture.md)
- [Compared with Terra and other health data APIs](docs/compare/README.md)
- [Changelog](CHANGELOG.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). New sources start with
[writing a connector](docs/writing-a-connector.md). Report vulnerabilities privately per
[SECURITY.md](SECURITY.md).

## License

AGPL-3.0-only, see [LICENSE](LICENSE). Not a medical device.
