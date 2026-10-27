<p align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/readme/koi-hero-dark.gif">
  <img src="docs/assets/readme/koi-hero-light.gif" width="100%" alt="Sync health data in peace. Koi drawn in characters swim around the line, in a seamless loop.">
</picture>
</p>

<div align="center">

# Open Health Sync

**One API for all your health data. Free, open source, on your own server.**

[![CI](https://github.com/soma-intelligence/open-health-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/soma-intelligence/open-health-sync/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/soma-intelligence/open-health-sync?color=0e9f8e)](https://github.com/soma-intelligence/open-health-sync/releases)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-555)](LICENSE)

</div>

## What it does

Every health app and wearable sends data in its own format. Open Health Sync collects it all and
gives it back in one clean format, through one API.

- **In:** Apple Health, Health Connect, Garmin, Oura, WHOOP, Fitbit, Withings, Polar, Strava,
  Dexcom and [more](docs/connectors/README.md).
- **Out:** a REST API, FHIR R4, CSV and Parquet, and an [MCP server](docs/mcp.md) so an AI agent
  can read your data.
- **Runs anywhere:** one program plus Postgres, even on a Raspberry Pi. Your data stays with you.

## Try it

<a id="quickstart"></a>You need Docker.

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs seed --demo
```

The last command loads 90 days of sample data and prints an API key and calls to try.

<a id="install-the-ohs-binary"></a>Next steps: [API guide](docs/api.md),
[self-hosting](docs/self-hosting.md), [releases](https://github.com/soma-intelligence/open-health-sync/releases).

## Don't want to run a server?

<a id="hosted-beta"></a>Use the [free hosted beta](https://app.openhealthsync.com/portal/signup?utm_source=github&utm_medium=readme&utm_campaign=section):
same API, nothing to install. Test data only for now.

## Learn more

[Documentation](docs/api.md) &middot; [FAQ](docs/faq.md) &middot;
[Compare with Terra and others](docs/compare/README.md) &middot;
<a id="connect-an-ai-agent-mcp"></a>[Connect an AI agent](docs/mcp.md) &middot;
[Contributing](CONTRIBUTING.md) &middot; [Security](SECURITY.md)

Not a medical device. Licensed under [AGPL-3.0](LICENSE).
