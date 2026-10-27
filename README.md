<p align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/readme/koi-hero-dark.gif">
  <img src="docs/assets/readme/koi-hero-light.gif" width="100%" alt="Sync health data in peace. Koi drawn in characters swim around the line, in a seamless loop.">
</picture>
</p>

<div align="center">

# Open Health Sync

### One open API for wearable and health data. On your own server.

Apple Health, Withings, Oura, WHOOP, Fitbit, Health Connect, Dexcom, Garmin and ten more,<br>
in one model, over REST, FHIR R4 and a read-only MCP server for AI agents.<br>
One Go binary plus Postgres. Free, self-hosted, runs on a Raspberry Pi.

[![CI](https://github.com/soma-intelligence/open-health-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/soma-intelligence/open-health-sync/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/soma-intelligence/open-health-sync?color=0e9f8e)](https://github.com/soma-intelligence/open-health-sync/releases)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-555)](LICENSE)
[![Hosted beta: sign up free](https://img.shields.io/badge/hosted_beta-sign_up_free-0e9f8e)](https://app.openhealthsync.com/portal/signup?utm_source=github&utm_medium=readme&utm_campaign=badge)

[**Quickstart**](#quickstart) &nbsp;&middot;&nbsp; [Hosted beta](#hosted-beta) &nbsp;&middot;&nbsp; [Sources](#sources-and-outputs) &nbsp;&middot;&nbsp; [MCP](#connect-an-ai-agent-mcp) &nbsp;&middot;&nbsp; [Compare](#open-health-sync-vs-terra) &nbsp;&middot;&nbsp; [Docs](#documentation)

<sub><b>v0.2.0, early preview.</b> APIs, configuration and schema may change in any 0.x release.</sub>

</div>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/demo/demo-dark.gif">
  <img src="docs/demo/demo-light.gif" width="100%" alt="Terminal recording of the quickstart. docker compose up starts ohs and Postgres; ohs seed --demo writes 90 days of synthetic data from six sources. The daily endpoint returns one merged day, one heart rate reading comes back as REST, FHIR R4 and Open mHealth, and tools/list on the MCP endpoint lists the read-only tools.">
</picture>

Open Health Sync (`ohs`) pulls from providers and accepts uploads from phones, keeps every
payload verbatim, maps it to one canonical model with one unit per quantity (UCUM, LOINC), and
serves it back. It is not a dashboard, a coaching app or a medical device.

- **Always current.** Provider webhooks trigger a pull of what changed, a backstop pull catches
  the rest, and `/freshness` says how up to date each source is.
- **Nothing thrown away.** Raw payloads live on disk or S3. Fix a mapper, re-process a user.
- **Standards out.** OpenAPI 3.1, FHIR R4 checked with the HL7 validator, Open mHealth, CSV and
  Parquet, signed webhooks.
- **Yours, and erasable.** No call home. Consent per source and category.
  `DELETE /v1/users/{id}` is a hard delete of rows and raw files.

## Quickstart

You need Docker with Compose v2.24 or later, plus `curl`, `jq` and `openssl`.

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
cp .env.example .env
sed -i.bak "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=$(openssl rand -base64 32)|" .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs seed --demo
```

`ohs seed --demo` loads 90 days of synthetic data from six sources and prints a demo API key and
calls to try ([demo](docs/demo.md)). For your own data, create a tenant with
`ohs admin tenant create --name dev`, then follow the [API guide](docs/api.md). The API listens
on `127.0.0.1`; see [self-hosting](docs/self-hosting.md) to expose it, use S3 or run on a Pi.

<a id="install-the-ohs-binary"></a>**The `ohs` binary.** Linux and macOS builds (amd64, arm64) are on the
[releases page](https://github.com/soma-intelligence/open-health-sync/releases). On macOS, run
`xattr -d com.apple.quarantine` on it first. `ohs import apple-health export.zip` loads a full
Health app export.

## Hosted beta

The same server, run for you: sign in, create a key, and your first call already returns demo
data. Free during the beta, test data only for now. Code moves to your own instance with a new
base URL and key.

[**Get started for free →**](https://app.openhealthsync.com/portal/signup?utm_source=github&utm_medium=readme&utm_campaign=section) &nbsp;&middot;&nbsp; [Watch the 75-second tour](docs/demo/platform-tour.mp4)

## Sources and outputs

**Sources (v0.2.0):** Apple Health, Health Connect (with Samsung Health, Zepp and RingConn),
Withings, Oura, WHOOP, Fitbit, Polar, Strava, Suunto, Ultrahuman, intervals.icu, Dexcom, OMRON,
Garmin, Eight Sleep, Xiaomi, activity files (FIT, GPX, TCX) and Bluetooth health devices.
Planned: Coros, health records through SMART on FHIR. Details per source:
[docs/connectors](docs/connectors/README.md).

**Outputs:** REST ([OpenAPI](api/openapi.yaml)), read-only [MCP](docs/mcp.md),
[FHIR R4](docs/fhir.md), [Open mHealth](docs/open-mhealth.md), [CSV and Parquet](docs/exports.md),
[webhooks](docs/webhooks.md), plus [client SDKs](docs/sdks/README.md) for Swift, Kotlin,
React Native and Flutter and [server SDKs](docs/server-sdks.md) for TypeScript and Python.

## Connect an AI agent (MCP)

Mint a user token with only the `read` scope, then add `ohs mcp` to your agent:

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

Tools, prompts and remote agents: [docs/mcp.md](docs/mcp.md).

## Open Health Sync vs Terra

| | Open source | Starting price | FHIR R4 | Where the data lives |
|---|---|---|---|---|
| **Open Health Sync** | Yes, AGPL-3.0 | Free, you pay for your server | Yes, HL7 validated | Your server |
| [Terra](docs/compare/terra.md) | No | $399 a month billed annually | A FHIR option | Terra's cloud |

Junction, Thryve, Spike, Sahha and ROOK side by side:
[health data aggregators compared](docs/compare/README.md). Common questions: [FAQ](docs/faq.md).

## Documentation

[Self-hosting](docs/self-hosting.md) &middot; [API guide](docs/api.md) &middot;
[Data model](docs/data-model.md) &middot; [Architecture](docs/architecture.md) &middot;
[Security model](docs/security-model.md) &middot; [Hardening](docs/hardening.md) &middot;
[Admin console](docs/console.md) &middot; [Connect page](docs/connect-page.md) &middot;
[Recipes](docs/recipes/README.md) &middot; [Examples](examples/README.md) &middot;
[Changelog](CHANGELOG.md)

## Contributing

Issues and pull requests are welcome. Start with a
[good first issue](https://github.com/soma-intelligence/open-health-sync/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22good%20first%20issue%22),
[record your wearable](docs/connectors/recording.md) or
[write a connector](docs/writing-a-connector.md). Read [CONTRIBUTING.md](CONTRIBUTING.md) first.

```sh
make setup && make build && make test
```

Report vulnerabilities privately per [SECURITY.md](SECURITY.md).

## License

AGPL-3.0-only, server and SDKs. See [LICENSE](LICENSE).
