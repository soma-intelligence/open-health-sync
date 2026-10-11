# Changelog

All notable changes to Open Health Sync are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the version is 0.x, any
release may change the API, the configuration or the database schema.

## [Unreleased]

### Added

- `GET /openapi.yaml` serves the OpenAPI description of the running version, readable from
  any origin, to import into Postman, Swagger UI or Redoc.
- `X-Request-Id` on every API response (the caller's own is kept when well formed), repeated
  as `request_id` in problem bodies and on every log line about the request.
- `ohs help`, `ohs help <command>`, `-h`/`--help` on every command and `ohs --version`.

### Changed

- `ohs mcp` says what a refused handshake means (401: the key; 403: the scope; 404 or 405:
  the URL) instead of `cannot reach`.
- MCP server instructions and the `get_samples` description steer agents to `get_daily` for
  trends and to narrow windows for raw samples.
- A known API path called with the wrong method answers 405 with an `Allow` header instead of
  404 `route not found`.
- A 403 names the credential the endpoint takes (a tenant API key, or a user token with a
  given scope).
- A command line error (unknown command, wrong arguments) exits 2; other failures still exit 1.
- `ohs serve` and `ohs doctor` refuse an `OHS_PUBLIC_URL` that is not an absolute http(s) URL
  (or has a query or fragment) and an unknown `OHS_LOG_LEVEL` at load, and an
  `OHS_ENCRYPTION_KEY` error says how to generate a key.
- `ohs serve` checks at start that the raw store accepts writes (a probe object outside `raw/`,
  deleted at once) and stops with a hint instead of failing every upload; `ohs doctor` runs
  the same check (not `--quick`). A database it cannot reach, or a listen address already in
  use, now names the variable to check.

## [0.0.1] - 2026-10-11

First public release, an early preview. See the
[release notes](docs/release-notes/v0.0.1.md) for known limitations.

### Added

- `ohs`, one Go binary with three roles (`api`, `collect`, `etl`) that run in one process or
  apart, on Postgres with TimescaleDB.
- Apple Health ingest: an iOS app (yours; no SDK ships yet) uploads HealthKit data as gzip
  NDJSON parts, covering the quantity, category, sleep, workout, route, ECG, heartbeat series
  and nutrition types.
- Withings connector: bring your own developer app, OAuth connection, scheduled pulls and push
  notifications for measurements, activity, sleep, workouts and ECG.
- One canonical model with one unit per quantity, deduplication, source and device
  attribution, validation, and daily summaries per local day.
- Raw payloads kept as the source of truth, in a local folder or any S3 compatible store, so a
  user can be re-processed after a mapper fix.
- Multi-tenancy: tenant API keys (`ohs_sk_`), short-lived scoped user tokens (`ohs_ut_`),
  OAuth tokens encrypted with AES-256-GCM.
- REST API described by `api/openapi.yaml`, and a read-only MCP server (`POST /mcp` and
  `ohs mcp` over stdio).
- Hard deletion of a user's raw files and rows, run by a worker that survives restarts, with
  `GET /v1/deletions/{job_id}` to follow it.
- Coverage per user and day, with the jobs still pending and the lines the ETL rejected
  (`rejected_records`).
- Reliability: crash-safe ingest, retries with backoff, parking after 5 attempts, an orphan
  scanner, a drainer for recoverable failures, provider rate limit handling, retention.
- `ohs doctor` configuration checks, `/healthz`, `/readyz` and Prometheus `/metrics`.
- ECG recordings and heartbeat series are stored; their read routes are planned.
- Docker Compose deployment with overlays for a Raspberry Pi 5 and a local S3 store, and an
  opt-in overlay to build from source.
- Release artefacts: a multi-arch image (`linux/amd64`, `linux/arm64`) at
  `ghcr.io/soma-intelligence/open-health-sync`, and static binaries for Linux and macOS on
  amd64 and arm64 with SHA-256 checksums.

[Unreleased]: https://github.com/soma-intelligence/open-health-sync/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/soma-intelligence/open-health-sync/releases/tag/v0.0.1
