# Plan: admin console

Spec: [specs/admin-console.md](../specs/admin-console.md).

1. Tests first, for each new piece: the `OHS_CONSOLE` values; the admin flag (first run,
   bootstrap, personal setup, the CLI, the single-tenant migration, `LookupKey`); the provider
   settings store (seal, open, bound to the slug, rekey) and the provider set (environment
   wins, `enabled`, a refused `Configure`, reload on change, device sources always on); every
   new route (tenant key, admin only where it says, tenant isolation, validation, audit action);
   the console's pure helpers with `node --test`.
2. Migration 0038: `ohs_core.tenants.admin`, the promotion of the only tenant, and
   `ohs_core.provider_settings`.
3. `internal/tenant`: `Tenant.Admin`, `SetAdmin`, the flag set by first run, bootstrap and
   personal setup, `LookupKey` returning it. `internal/auth`: `Principal.Admin` and the
   `TenantAdmin` scope. `cmd/ohs`: `tenant create --admin`, `tenant admin`, `tenant list`.
4. `internal/connector`: the `OptionalKeyed` interface, implemented by the connectors with
   optional keys, and `SecretKey`, the list of keys whose value may be shown.
5. `internal/providerset`: the settings store and the set of enabled connectors, swapped
   atomically, reloaded by every process every 30 seconds and at once by the process that
   saved a change. `internal/app` wires it into the api, collect and etl roles in place of the
   list fixed at start. `internal/rekey` learns the sealed column.
6. `internal/api`: `GET /v1/tenant`, the key routes, the pipeline routes (with
   `collect.TenantSyncRuns`), the admin provider routes, the audit actions of the key routes.
   `api/openapi.yaml`, contract tests, `make sdk`.
7. `internal/console`: the mode in the page, the page split into a module of pure helpers
   (`lib.mjs`, tested) and the views (`app.mjs`), the new pages and their styles, light and dark.
8. Docs: `docs/console.md` rewritten as the guide, `docs/api.md`, `docs/self-hosting.md`,
   `docs/setup.md`, `.env.example`, CHANGELOG. Screenshots of the console in light and dark.

New dependency: none.
