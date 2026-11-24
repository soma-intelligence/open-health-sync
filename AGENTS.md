# AGENTS.md

Notes for coding agents working in this repository. [CONTRIBUTING.md](CONTRIBUTING.md) is the
source of truth for the rules; this file is the short version, with the commands.

## What this is

Open Health Sync: a self-hosted health data aggregator. One Go binary (`ohs`) plus PostgreSQL with
TimescaleDB. Connectors pull or receive data from wearables and health apps, the ETL maps it to one
canonical model, and the API serves it as REST, FHIR R4, Open mHealth, webhooks, CSV, Parquet and
MCP. Read [docs/architecture.md](docs/architecture.md) before changing the core.

## Build and test

Go 1.26. Docker for integration tests.

```sh
make setup        # installs the pre-commit hook (.githooks: forbidden-terms guard, gitleaks)
make build        # bin/ohs
make test         # unit tests, no Docker (replays the recordings in test/recorded)
make lint         # golangci-lint v2.14.0 with .golangci.yml
make integration  # tests tagged integration (testcontainers)
make test-js      # the console's and setup page's JavaScript helpers (Node 22+)
make guard        # forbidden-terms guard over tracked files and branch commit messages
```

Heavier suites: `make contract`, `make e2e`, `make backup-test`, `make reliability`,
`make upgrade-test`, `make fhir-validate` (Java 17+). SDKs: `make sdk-test`.

Documentation site (`docs-site/`, Fumadocs on Next.js, Node 22):

```sh
cd docs-site && npm ci && npm run lint && npm run build && npm run check:links
```

## Layout

| Path | What |
|---|---|
| `cmd/ohs/` | the binary: `up`, `serve`, `migrate`, `doctor`, `admin`, `mcp`, `import`, `seed`, `version` |
| `internal/` | core packages (api, auth, tenant, collect, etl, model, queue, fhir, ...) |
| `connectors/` | one package per provider, linked by `connectors/all` |
| `api/openapi.yaml` | the public API contract |
| `internal/db/migrations/` | numbered `.up.sql` / `.down.sql` pairs |
| `bridge/` | `ohs-bridge`, the Bluetooth desktop bridge (Rust) |
| `sdks/` | Swift, Kotlin, React Native, Flutter clients; `sdks/server` TypeScript and Python |
| `recipes/` | small Go programs on top of the API |
| `deploy/` | Dockerfile, compose files, Caddyfile, Helm chart, platform configs |
| `docs/` | user documentation; `docs-site/` builds the website from it |

## Rules that bite

- Tests first: every change comes with a test that failed before it. Connector tests use fixtures
  with invented values and never call a real provider.
- `api/openapi.yaml` leads: change it together with the handler; contract tests check responses.
- Generated files: `docs/data-model.md` (`go run ./internal/model/cmd/gendocs`), the data types
  table in `docs/connectors/README.md` (`go run ./internal/connector/cmd/gendatatypes`), the server
  SDKs and collections (`make sdk`), `skills/open-health-sync/SKILL.md`
  (`go run ./internal/prompts/cmd/genprompts`). Regenerate, never edit by hand.
- Every Go file starts with `// SPDX-License-Identifier: AGPL-3.0-only`; every package has a
  package comment.
- Every query on `ohs_sync` and `ohs_data` filters on `tenant_id` and `user_id`.
- No secrets and no health values in logs, error messages or fixtures.
- Logs are zerolog through `internal/logging` (a synced copy, never edited by hand): no `log/slog`,
  no `log`. Event names are snake_case English with no variable inside, fields are typed snake_case
  keys with the unit in the key. Details in [CONTRIBUTING.md](CONTRIBUTING.md).
- Standard library first (`http.ServeMux`); a new dependency needs a reason.
- Conventional commit messages (`feat(withings): ...`, `fix(etl): ...`, `docs: ...`); user-facing
  changes get a line in `CHANGELOG.md` under Unreleased.
- No AI attribution in commits, pull requests or branch names (a CI job rejects it).

## Docs conventions

- `docs/how-to/*.md`: one task per page, H1 is the task, the first paragraph answers it. New files
  there, in `docs/compare/`, `docs/connectors/`, `docs/recipes/` and `docs/learn/` are picked up
  by `docs-site/scripts/sync-docs.mts` automatically; other pages need an entry in its `sources` map.
- Relative links between docs are rewritten to site URLs; keep them relative and pointing at files
  that exist.
- Say what ships and what is planned. Wellness wording: readings, trends, share with a clinician.
  Open Health Sync is not a medical device.
