# Contributing

Thanks for helping. Open Health Sync is a utility: it stops at clean, traceable health data.
Scores, coaching, AI and product dashboards are out of scope and belong in the projects built on
top of it.

Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md). Report security problems
privately, as described in [SECURITY.md](SECURITY.md).

## Set up

You need Go 1.26 and, for integration tests, Docker. `make lint` needs golangci-lint v2.14.0
(`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`), the version CI
runs.

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git && cd open-health-sync
make setup        # installs the pre-commit hook
make build        # bin/ohs
make test         # unit tests, no Docker
make lint         # golangci-lint with .golangci.yml, integration files included
make integration  # tests tagged integration, need Docker (testcontainers)
```

Without Docker, point `OHS_TEST_DATABASE_URL` at a running TimescaleDB server (a user allowed to
create databases) and the integration tests use it instead of a container.

Other targets: `make e2e` (the quickstart against a real stack), `make reliability` (crash
recovery) and `make upgrade-test` (the last release, with data in it, migrated to your checkout;
a change to `internal/db/migrations` should pass it). `make guard` runs the repository checks
described below.

## Layout

```
cmd/ohs/            the binary: serve, up, migrate, admin, doctor, mcp, import, seed, version
internal/           the core packages (app, api, auth, tenant, collect, etl, model, queue, ...)
connectors/         one directory per provider, plus connectors/all that links them
api/openapi.yaml    the public API, the source of truth
docs/               user documentation
docs-site/          the documentation website, built from docs/ and the OpenAPI file
deploy/             Dockerfile and compose files
```

Read [docs/architecture.md](docs/architecture.md) first. To add a provider, read
[docs/writing-a-connector.md](docs/writing-a-connector.md): a connector is a package under
`connectors/` and needs no change to the core.

## Rules

- **Tests first.** Every change comes with a test that failed before it. Integration tests use
  the `integration` build tag; connector tests use fixtures and never call a real provider.
- **The OpenAPI file leads.** Change `api/openapi.yaml` together with the handler; contract
  tests validate responses against it.
- **Generated docs.** `docs/data-model.md` is generated. After touching `internal/model`, run
  `go run ./internal/model/cmd/gendocs` and commit the result; a test fails otherwise.
- **Package comments.** Every Go package has one; a test lists any that lack it.
- **Licence header.** Every Go file starts with `// SPDX-License-Identifier: AGPL-3.0-only`.
- **Tenant isolation.** Every query on `ohs_sync` and `ohs_data` filters on `tenant_id` and
  `user_id`. A new table has both columns.
- **No secrets, no health values in logs.** Tokens, keys and measurements never reach a log
  line, an error message or a test fixture. Fixtures use invented values.
- **Migrations.** Add a numbered pair of `.up.sql` and `.down.sql` files under
  `internal/db/migrations`. Use hypertables. No continuous aggregate beyond `quantity_hourly` (migration 0035) without
  discussing it first; compression only as migration 0015 sets it up.
- **Standard library first.** The router is `http.ServeMux`; a new dependency needs a reason.
- **Repository checks.** `make guard` scans the tracked files and the branch's commit messages
  for names that must not appear in this repository. The list is kept private by the maintainers
  and CI (point `OHS_TERMS_FILE` at a copy, or set `OHS_FORBIDDEN_TERMS`). Without it the
  script prints a warning and skips on your machine, and fails in CI. Pull requests from forks
  skip this check (CI secrets are not available to them); it runs again after the merge. CI also
  runs a secret scan.

## Contribute a recording of your account

The most useful contribution needs no code. If you have an account with one of the sources (or an
iPhone with Apple Health), record what its API answers for you:

```sh
make record PROVIDER=withings                                        # a cloud source
OHS_RECORD_EXPORT=~/Downloads/export.zip make record PROVIDER=apple_health   # a Health app export
```

The recorder anonymizes everything in memory on your machine and refuses to write a file if a
token or an original identifier survives. Commit the result under `test/recorded/testdata/<slug>/`
and open a pull request: CI replays every committed recording on every change. Steps per source,
including the redirect address your developer app needs:
[docs/connectors/recording.md](docs/connectors/recording.md).

## Commits and pull requests

- Conventional commit messages (`feat(withings): ...`, `fix(etl): ...`, `docs: ...`).
- One logical change per pull request, with the tests and docs that go with it.
- Say what changed and why in the description, and how you tested it.

By contributing you agree that your work is released under the repository's licence,
AGPL-3.0-only.

## Reporting a vulnerability

Do not open a public issue. See [SECURITY.md](SECURITY.md).
