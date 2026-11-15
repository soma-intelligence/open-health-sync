# Plan: console v2, part 1, settings

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every server setting declared once in a registry, saved from the API into Postgres, applied live (a chosen set) or at the next start (the rest), with each process reporting what it runs.

**Architecture:** `internal/config/settings.go` holds the registry and the stacked lookup; `config.Load` keeps its signature and reads through it. A new package `internal/settings` stores values (sealed for secrets), resolves the configuration at start (skipping invalid stored values), reloads the `live` ones every 30 seconds in the pattern of `internal/providerset`, and writes process heartbeats. `internal/api` serves four admin routes on top of it. The current console is untouched.

**Tech Stack:** Go 1.x as in `go.mod`, pgx v5, Postgres migrations in `internal/db/migrations`, testify in the integration suites, plain `testing` in `internal/config`.

**Spec:** [specs/console-v2.md](../specs/console-v2.md), sections 1, 4 (Go unit, Go integration, Contract) and 6 item 1.

## Global Constraints

- Precedence, per setting: environment variable (or its `_FILE` twin) when set and not empty, then the stored value, then `Default`.
- `bootstrap` settings are never stored and never editable: `OHS_DATABASE_URL`, `OHS_ENCRYPTION_KEY`, `OHS_ENCRYPTION_KEY_FILE`, `OHS_PREVIOUS_ENCRYPTION_KEYS`, every `OHS_VAULT_*`, `OHS_RAW_STORE`, `OHS_LISTEN`, `OHS_OPS_LISTEN`, `OHS_METRICS_LISTEN`, `OHS_ROLES`, `OHS_CONSOLE`, `OHS_SETUP_TOKEN`, `OHS_SETUP_TOKEN_FILE`, `OHS_BOOTSTRAP_TENANT`.
- `live` settings: `OHS_LOG_LEVEL`, `OHS_RETENTION_DAYS`, `OHS_AUDIT_RETENTION_DAYS`, `OHS_TRACKING_RETENTION_DAYS`, `OHS_CONSENT_REQUIRED`, `OHS_BACKFILL_DAYS`, `OHS_WEBHOOK_PAUSE_AFTER`, `OHS_WEBHOOK_ROTATION_GRACE_HOURS`, `OHS_RATE_LIMIT_API`, `OHS_RATE_LIMIT_AUTH_FAILURES`, `OHS_RATE_LIMIT_INBOUND`, `OHS_RATE_LIMIT_INBOUND_PROVIDER`, `OHS_RATE_LIMIT_OAUTH`. Every other registry entry is `restart`.
- Out of the registry: `OHS_<PROVIDER>_*`, `OHS_URL`, `OHS_TOKEN`, `OHS_MCP_URL`, `OHS_MCP_KEY`, `OHS_VERSION`, `OHS_HTTP_BIND`, `OHS_HTTP_PORT`, `OTEL_*`.
- Sections, in this order: Server, Storage, Security, Console, Metrics, Retention, Consent, Ingest, Sync, Processing, Webhooks, Event stream, Rate limits, Vault.
- Types: `bool`, `int` (with bounds), `duration`, `enum` (with values), `url`, `list`, `rate` (`rate/burst`), `string`.
- Reload interval 30 s; heartbeat every 30 s; heartbeats hidden by the API after 2 min, deleted by the daily cleanup after 5 min; stale after 60 s.
- A secret's value never appears in an answer, a log line, an audit event or the export. The audit event of a `PATCH` lists the changed names in `detail`, never values.
- Every write to `instance_settings` runs in one transaction that also increments the revision.
- `config.Load(env func(string) (string, bool), environ []string) (Config, error)` keeps its signature.
- The environment alone stays a valid configuration: `serve` loads it first, as today.
- `go build` and `go install` need no Node. New dependency: none.
- Copy rules: no em dash anywhere (code, docs, commit messages); no AI attribution in commits.

## Review Focus

1. A client reads `GET /v1/admin/settings` with `Accept-Encoding: gzip`: the compression middleware (`internal/api/compress.go`) turns `ETag: "5"` into `W/"5"`. The client echoes `If-Match: W/"5"` and expects its `PATCH` to pass. Test in Task 9.
2. Docker Compose passes every line of `.env`, so `OHS_RETENTION_DAYS=` arrives as an empty variable. An empty variable is not set: the setting stays editable and the stored value applies. Tests in Tasks 1 and 6.
3. An admin lowers `OHS_WEBHOOK_CONCURRENCY` to 2 while a stored `OHS_WEBHOOK_PER_TENANT` is 4. They expect a 422 that names `OHS_WEBHOOK_PER_TENANT` and nothing stored; at start, a stored pair that became invalid is skipped with a warning, never a refused start. Tests in Tasks 4 and 6.
4. Two admins apply with the same `If-Match` at the same moment: exactly one gets 200, the other 412, and the revision moves by one. Test in Task 6.
5. `OHS_METRICS_TOKEN` saved with a value Load refuses (too short): the 422 message, the startup warning and the audit event must not carry the value. Tests in Tasks 4, 6 and 9.

## Spec gaps settled here

- **Behind and stale need more than one revision number.** `instance_settings_revision` gets two more columns in migration 0056: `restart_revision bigint` (the revision of the last change to a `restart` setting) and `changed_at timestamptz` (when `revision` last moved). Behind is `start_revision < restart_revision`; stale is `live_revision < revision AND now() - changed_at > 60 s`.
- **Cross-field checks.** Several checks span two settings (`OHS_INGEST_TENANT_CONCURRENCY`, `OHS_WEBHOOK_PER_ENDPOINT`, `OHS_WEBHOOK_PER_TENANT`, `OHS_STREAM_MAX_PER_TENANT`, `OHS_METRICS_LISTEN`). `Setting.Validate` checks one value; the API then dry runs `config.Load` on the proposed state. Every Load error starts with the name of the variable it refuses (a test pins it), which is how an error is attributed to a setting.
- **Derived defaults.** Some defaults depend on another setting or on the roles (`OHS_INGEST_TENANT_CONCURRENCY`, `OHS_WEBHOOK_PER_ENDPOINT`, `OHS_WEBHOOK_PER_TENANT`, `OHS_STREAM_MAX_PER_TENANT`, `OHS_OPS_LISTEN`, `OHS_BACKFILL_DAYS`). Their `Default` is `""` and `DefaultNote` says how it is derived. `OHS_TRACKING_RETENTION_DAYS` gets the constant `config.DefaultTrackingRetentionDays = 90` so its `Default` is `"90"`, as `.env.example` shows.
- **`.env.example`** lacks `OHS_INGEST_TENANT_CONCURRENCY` and `OHS_INGEST_MAX_DECOMPRESSED_MIB`; Task 1 adds them.
- **`OHS_WEBHOOK_ROTATION_GRACE_HOURS`** is read by the webhook store in the api role, not by the dispatcher; the getter goes there.
- **Unknown stored rows** (a setting removed by a later release) can be removed with `PATCH {"OHS_OLD": null}`; any other value for an unknown name is a 422. `ohs doctor` says so.
- **Missing `If-Match`** answers 428; a malformed one 400. `If-Match: *` is malformed.
- **An empty string** in a `PATCH` is a 422 ("empty: send null to go back to the default"), so empty never means two things.
- **Column name `sealed`** is already in `notSealed` of `internal/rekey/rekey_test.go` (the vault). The bytea test passes either way; Task 3 adds a comment there so a reader knows the name now means both.

## JSON contract (consumed by plans 2 and 3)

`GET /v1/admin/settings`, 200, header `ETag: "<revision>"`, `Cache-Control: no-store`:

```json
{
  "revision": 5,
  "data": [{
    "name": "OHS_WEBHOOK_PAUSE_AFTER", "label": "Pause an endpoint after", "section": "Webhooks",
    "help": "...", "type": "int", "min": 0, "max": 1000, "values": null,
    "default": "5", "default_note": null, "secret": false, "apply": "live",
    "source": "console", "value": "10", "stored_value": null, "invalid": null
  }]
}
```

- `type`: one of the eight types. `min`, `max`: integers for `int`, else null. `values`: array for `enum`, else null.
- `default`: string, `""` when derived; `default_note`: string or null.
- `apply`: `live`, `restart` or `bootstrap`. `source`: `env`, `console` or `default`.
- `value`: the effective value as a string; for a secret `"set"` when set and null when not.
- `stored_value`: the stored value when `source` is `env` and a value is also stored (`"set"` for a secret), else null.
- `invalid`: the message when the stored value was skipped, else null.
- `data` is in registry order.

`PATCH /v1/admin/settings`, body `{"OHS_X": "value", "OHS_Y": null}` (strings or null only), header `If-Match: "<revision>"` (`W/"<revision>"` accepted):

- 200 `{"revision": 6, "changed": ["OHS_X", "OHS_Y"], "restart_required": ["OHS_X"]}` with `ETag: "6"`. `changed` and `restart_required` are sorted; a body that changes no row answers the unchanged revision and two empty arrays.
- 400 malformed body, empty object or malformed `If-Match`; 428 no `If-Match`.
- 412 revision moved: problem, with `ETag: "<current>"`.
- 409 `{"type","title","status","detail","request_id","settings":[{"name":"OHS_LISTEN","message":"bootstrap: set it in the environment"}]}`; message is `bootstrap: ...` or `env: ...`.
- 422 same shape, one entry per invalid setting.

`GET /v1/admin/settings/export`, 200, `Content-Type: text/plain; charset=utf-8`, `Content-Disposition: attachment; filename="ohs.env"`, `Cache-Control: no-store`. First line `# Open Health Sync settings, revision <n>`; then per section a line `# <Section>` and per setting `# <Label> (source <source>, applies <apply>)` followed by `OHS_X=<value>`. A set secret gets `# set, value not exported` and `OHS_X=`; an unset secret `OHS_X=`. A value with whitespace, `#`, `"` or `'` is double quoted with `\"` and `\\` escaped.

`GET /v1/admin/processes`, 200, `Cache-Control: no-store`:

```json
{
  "revision": 6,
  "data": [{
    "id": "uuid", "host": "ohs-7f9c", "roles": ["api", "etl"], "version": "0.4.0",
    "started_at": "2026-11-14T10:00:00Z", "seen_at": "2026-11-14T10:05:30Z",
    "start_revision": 4, "live_revision": 6, "skipped": ["OHS_X"], "reload_error": null,
    "behind": true, "stale": false
  }]
}
```

Rows with `seen_at` older than 2 minutes are left out. Order: `started_at`, then `id`.

---

### Task 1: The registry and the stacked lookup

**Files:**
- Create: `internal/config/settings.go`
- Modify: `internal/config/config.go` (replace the `reserved` map with the registry plus `notServer`; add `DefaultTrackingRetentionDays`; move single-value checks into helpers the registry reuses)
- Modify: `.env.example` (add `OHS_INGEST_TENANT_CONCURRENCY`, `OHS_INGEST_MAX_DECOMPRESSED_MIB`)
- Test: `internal/config/settings_test.go`

**Interfaces:**
- Produces:
  ```go
  type Type string   // TypeBool "bool", TypeInt "int", TypeDuration "duration", TypeEnum "enum",
                     // TypeURL "url", TypeList "list", TypeRate "rate", TypeString "string"
  type Apply string  // ApplyLive "live", ApplyRestart "restart", ApplyBootstrap "bootstrap"
  type Setting struct {
      Name, Label, Section, Help string
      Type        Type
      Min, Max    *int64   // TypeInt only
      Values      []string // TypeEnum only
      Default     string   // what Load uses when unset; "" when derived
      DefaultNote string   // how a derived default is computed, else ""
      Secret      bool
      Apply       Apply
      Validate    func(v string) error // the single-value check of Load; error starts with Name
  }
  var Sections []string                 // the 14 sections in spec order
  func Registry() []Setting             // declaration order, grouped by section
  func LookupSetting(name string) (Setting, bool)
  func EnvSet(env func(string) (string, bool), name string) bool // name or name+"_FILE" set and not empty
  func Stacked(env func(string) (string, bool), stored map[string]string) func(string) (string, bool)
  ```

- [ ] **Step 1: Write the failing tests** in `internal/config/settings_test.go`:
  - `TestRegistryMatchesLoad`: parse the non-test `.go` files of `internal/config` with `go/parser`, collect every string literal matching `^OHS_[A-Z0-9_]+$` plus `name+"_FILE"` for each literal passed to `secret(`; assert the set equals the names of `Registry()` (both directions).
  - `TestRegistryShape`: names unique; `Section` in `Sections`; `Apply` of every name of the bootstrap list (25 with the 12 `OHS_VAULT_*` names Load reads) is `ApplyBootstrap`, of the 13 live names `ApplyLive`, everything else `ApplyRestart`; `Min`/`Max` set only for `TypeInt`, `Values` only for `TypeEnum`; `Secret` true exactly for `OHS_DATABASE_URL`, `OHS_ENCRYPTION_KEY`, `OHS_PREVIOUS_ENCRYPTION_KEYS`, `OHS_VAULT_ROOT_KEY`, `OHS_VAULT_ROOT_KEY_PREVIOUS`, `OHS_SETUP_TOKEN`, `OHS_METRICS_TOKEN`.
  - `TestValidateAgreesWithLoad`: for each non-bootstrap entry and each sample in a table per type (`"0"`, `"-1"`, `"1"`, `"abc"`, `"30/120"`, `"0.5/1"`, `"true"`, `"maybe"`, `"10m"`, `"https://x.example"`, `"10.0.0.0/8"`, `" 5"`, its `Max+1`), `Validate(v) != nil` iff `load(t, base()+{name: v})` fails with an error starting with the name.
  - `TestLoadErrorsStartWithTheName`: for every case of the table above where Load fails, `strings.HasPrefix(err.Error(), name)`.
  - `TestStackedPrecedence`: env `"7"` beats stored `"9"`; env `""` (present, empty) falls to stored `"9"`; env absent and stored absent returns `("", false)`; `X_FILE` set in env hides stored `X`; names outside the registry pass straight to env.
  - `TestEnvExampleNamesTheRegistry`: read `../../.env.example`; collect names on `OHS_X=` and `# OHS_X=` lines; assert every registry name is there, every `OHS_*` name there is in the registry or `notServer` or a provider key (`splitProviderKey` ok and not registry); for each entry with `Default != ""` and a non-empty example value, the value equals `Default`; when the comment block right above names one variable and holds `(default X)`, `X == Default`.
  - `TestDefaultsAreWhatLoadUses`: for each entry with `Default != ""`, `load(base())` and `load(base()+{name: Default})` give equal `Config` (compare with `reflect.DeepEqual` after zeroing `EncryptionKeySource`).

- [ ] **Step 2: Run the tests to verify they fail**

  Run: `go test -count=1 -run 'TestRegistry|TestValidate|TestLoadErrors|TestStacked|TestEnvExample|TestDefaults' ./internal/config/`
  Expected: FAIL, `undefined: Registry`.

- [ ] **Step 3: Implement** the types and functions above in `internal/config/settings.go`. Validators are built from shared helpers that `Load` also calls (`intIn(name, lo, hi)`, `boolValue(name)`, `rateValue(name)`, `enumValue(name, values...)`, `CheckPublicURL`, `proxyList(name)`), so messages are identical. In `config.go`: `reserved[rest]` becomes `LookupSetting("OHS_"+rest)` or `notServer["OHS_"+rest]`, with `notServer` holding the seven names of the out-of-registry list; add `DefaultTrackingRetentionDays = 90` and use it as the `loadWorkers` default. Add the two missing lines to `.env.example` with their defaults in the house comment style.

- [ ] **Step 4: Run the tests to verify they pass**

  Run: `go test -count=1 ./internal/config/`
  Expected: PASS (the existing `TestLoad*` too).

- [ ] **Step 5: Commit**

  ```bash
  git add internal/config .env.example
  git commit -m "feat(config): one registry for every server setting"
  ```

### Task 2: Migrations 0056 and 0057

**Files:**
- Create: `internal/db/migrations/0056_instance_settings.up.sql`, `0056_instance_settings.down.sql`, `0057_process_heartbeats.up.sql`, `0057_process_heartbeats.down.sql`
- Test: `internal/db/migrate_integration_test.go` (one new test)

**Interfaces:**
- Produces: tables `ohs_core.instance_settings` (spec 1.4, verbatim), `ohs_core.instance_settings_revision` (spec 1.4 plus `restart_revision bigint NOT NULL DEFAULT 0`, `changed_at timestamptz NOT NULL DEFAULT now()`, and its single row inserted with `INSERT ... VALUES (true, 0, 0, now())`), `ohs_core.process_heartbeats`:
  ```sql
  process_id uuid PRIMARY KEY, host text NOT NULL, roles text[] NOT NULL, version text NOT NULL,
  started_at timestamptz NOT NULL, start_revision bigint NOT NULL, live_revision bigint NOT NULL,
  skipped text[] NOT NULL DEFAULT '{}', reload_error text, seen_at timestamptz NOT NULL DEFAULT now()
  ```
  with `CREATE INDEX process_heartbeats_seen_at ON ohs_core.process_heartbeats (seen_at)`.

- [ ] **Step 1: Write the failing test** `TestInstanceSettingsSchema`: after `db.Migrate` up on `db.TestDB(t)`, the revision table has exactly one row `(true, 0, 0)`; inserting a second row fails; inserting into `instance_settings` with both `value` and `sealed` null fails the CHECK, and with both set fails too; migrating down to 55 drops the three tables and up again restores them.
- [ ] **Step 2: Run it to verify it fails**

  Run: `go test -tags integration -count=1 -run TestInstanceSettingsSchema ./internal/db/`
  Expected: FAIL, relation does not exist.
- [ ] **Step 3: Write the four SQL files** with the SPDX header and a comment block in the style of 0038 (what each column holds, that names are checked against the registry in Go, that `sealed` is bound to the name).
- [ ] **Step 4: Run the db tests**

  Run: `go test -count=1 ./internal/db/ && go test -tags integration -count=1 -run 'TestInstanceSettingsSchema|TestMigrat' ./internal/db/`
  Expected: PASS, `TestLatestVersion` sees 57.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/db/migrations/0056_* internal/db/migrations/0057_* internal/db/migrate_integration_test.go
  git commit -m "feat(db): instance settings, their revision and process heartbeats"
  ```

### Task 3: The settings store, sealed, and `ohs admin rekey`

**Files:**
- Create: `internal/settings/doc.go` (package comment, needed by `TestEveryPackageHasADocComment`), `internal/settings/store.go`
- Modify: `internal/rekey/rekey.go` (one `tables` entry), `internal/rekey/rekey_test.go` (comment on `"sealed"`)
- Test: `internal/settings/store_integration_test.go`, `internal/rekey/rekey_integration_test.go`

**Interfaces:**
- Produces:
  ```go
  func SealAAD(name string) []byte // []byte("ohs_core.instance_settings|" + name)
  type Row struct {
      Name, Value string     // Value opened when sealed
      Sealed      bool
      UpdatedAt   time.Time
      UpdatedBy   uuid.UUID
      OpenErr     error      // a sealed value no configured key opens; Value is ""
  }
  type State struct {
      Revision, RestartRevision int64
      ChangedAt                 time.Time
      Rows                      map[string]Row
  }
  type Change struct {
      Put     map[string]string // name to value
      Delete  []string
      Restart bool              // a restart setting is among the names: restart_revision moves too
      By      uuid.UUID
  }
  var ErrRevisionMoved = errors.New("settings: the revision moved")
  type Store interface {
      Revision(ctx context.Context) (int64, error)
      Load(ctx context.Context) (State, error) // revision and rows in one REPEATABLE READ transaction
      Apply(ctx context.Context, ifRevision int64, ch Change) (int64, error)
  }
  type PG struct{ /* pool, box */ }
  func NewPG(pool *pgxpool.Pool, box *crypto.Box) *PG
  ```
  `Apply` locks the revision row (`SELECT ... FOR UPDATE`), answers `ErrRevisionMoved` when it differs from `ifRevision`, upserts and deletes, and when at least one row changed sets `revision = revision + 1`, `changed_at = now()` and, with `Restart`, `restart_revision` to the new revision. It returns the revision after the call. A value is sealed with `box.SealFor(value, SealAAD(name))` when `config.LookupSetting(name)` is `Secret`, else stored clear.

- [ ] **Step 1: Write the failing tests** in `store_integration_test.go`:
  - `TestPGStoreRoundTrip`: `Apply(ctx, 0, {Put: {"OHS_LOG_LEVEL": "debug", "OHS_METRICS_TOKEN": "0123456789abcdefXYZ"}})` returns 1; `Load` gives revision 1, both rows, `OHS_METRICS_TOKEN` with `Sealed` true and its value opened; the raw `sealed` column does not contain the token bytes; `value` is NULL for it.
  - `TestPGStoreRevision`: `Apply` with `ifRevision` 0 after revision 1 returns `ErrRevisionMoved` and changes nothing; a delete of an absent name returns the unchanged revision; `Restart: true` sets `restart_revision` to the new revision, `Restart: false` leaves it.
  - `TestPGStoreConcurrentApply`: two goroutines `Apply(ctx, 1, ...)` at once: one returns 2, the other `ErrRevisionMoved`.
  - `TestPGStoreBoundToName`: copy the `sealed` bytes of one row into another name; `Load` gives that row an `OpenErr`, never a value.
  In `rekey_integration_test.go`, extend `TestRunRekeysEverySealedColumn` with an `instance_settings` secret row: after `Run` under a new key it opens with the new key alone.
- [ ] **Step 2: Run them to verify they fail**

  Run: `go test -tags integration -count=1 ./internal/settings/ ./internal/rekey/`
  Expected: FAIL, package `internal/settings` has no `NewPG`.
- [ ] **Step 3: Implement** `store.go` and the rekey entry `{name: "ohs_core.instance_settings", key: {"name"}, types: {"text"}, sealed: {"sealed"}, aad: settings.SealAAD(k[0])}`.
- [ ] **Step 4: Run the tests**

  Run: `go test -count=1 ./internal/rekey/ ./internal/docscheck/ && go test -tags integration -count=1 ./internal/settings/ ./internal/rekey/`
  Expected: PASS.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/settings internal/rekey
  git commit -m "feat(settings): store instance settings, secrets sealed and re-sealed by rekey"
  ```

### Task 4: Resolve at start, invalid stored values skipped

**Files:**
- Create: `internal/settings/resolve.go`, `internal/settings/boot.go`
- Modify: `cmd/ohs/serve.go`, `internal/app/app.go` (`Run` signature, `options`)
- Test: `internal/settings/resolve_test.go`, `internal/settings/boot_integration_test.go`

**Interfaces:**
- Consumes: `config.Stacked`, `config.LookupSetting`, `Store.Load` (Task 3).
- Produces:
  ```go
  type Invalid struct{ Name, Message string }
  // Resolve runs config.Load on env, then stored, then defaults. Stored names
  // not in the registry go to unknown; bootstrap names and names the
  // environment sets are left out; a value failing Validate, or named first by
  // a Load error (the variable the error starts with when stored, else the
  // first stored name the message contains), is dropped into invalid and Load
  // runs again. err is a Load error no stored value explains.
  func Resolve(env func(string) (string, bool), environ []string, stored map[string]string) (cfg config.Config, invalid []Invalid, unknown []string, err error)
  func SafeMessage(s config.Setting, value string, err error) string // "<Name>: invalid value" for a Secret whose message holds value
  type Start struct {
      Revision int64
      Invalid  []Invalid
      Unknown  []string
  }
  func Boot(ctx context.Context, envCfg config.Config, env func(string) (string, bool), environ []string) (config.Config, Start, error)
  ```
  `Boot` opens a pool on `envCfg.DatabaseURL` (closed before it returns), a box from `crypto.NewKeyring(envCfg.EncryptionKey, envCfg.PreviousEncryptionKeys...)`, reads `Store.Load`, treats rows with `OpenErr` as invalid with message `"the stored value does not open with OHS_ENCRYPTION_KEY or OHS_PREVIOUS_ENCRYPTION_KEYS: save it again"`, logs each invalid at `warn` with `setting` and `error` (the safe message) and each unknown at `warn`. A read failure is returned (the process does not start).
  `app.Run(ctx context.Context, cfg config.Config, start settings.Start) error`; `options` gains `start settings.Start`, `lookup func(string) (string, bool)`, `environ []string` (`Run` sets `os.LookupEnv`, `os.Environ()`; tests may leave them nil, which means the same).

- [ ] **Step 1: Write the failing tests** in `resolve_test.go` (unit, no database):
  - `TestResolveStoredApplies`: stored `{"OHS_WEBHOOK_PAUSE_AFTER": "9"}` gives `cfg.WebhookPauseAfter == 9`, no invalid.
  - `TestResolveEnvWins`: env `"3"`, stored `"9"` gives 3; env `""` gives 9.
  - `TestResolveSkipsInvalid`: stored `"abc"` gives the default 5 and one `Invalid{Name: "OHS_WEBHOOK_PAUSE_AFTER"}`.
  - `TestResolveCrossField`: stored `OHS_WEBHOOK_CONCURRENCY=2`, `OHS_WEBHOOK_PER_TENANT=4` gives `WebhookConcurrency == 2`, per tenant at its derived default 1, invalid names `OHS_WEBHOOK_PER_TENANT`; env `OHS_WEBHOOK_PER_TENANT=4` with stored concurrency 2 drops the stored concurrency instead.
  - `TestResolveIgnoresBootstrapAndUnknown`: stored `OHS_LISTEN=":9"` leaves `Listen` at the env value; stored `OHS_GONE=1` comes back in `unknown`.
  - `TestResolveSecretMessage`: stored `OHS_METRICS_TOKEN="short"` gives an invalid message without `"short"`.
  In `boot_integration_test.go`: `TestBootAppliesStoredSettings` (rows written with `PG.Apply`, `Boot` returns the stacked config and the revision) and `TestBootSkipsInvalidAndUnknown` (a log handler captures one `warn` per invalid and unknown name, none holds a secret value; `Boot` returns no error).
- [ ] **Step 2: Run them to verify they fail**

  Run: `go test -count=1 -run TestResolve ./internal/settings/`
  Expected: FAIL, `undefined: Resolve`.
- [ ] **Step 3: Implement** `Resolve`, `SafeMessage`, `Boot`. In `serve.go`, after `checkSchema`: `cfg, start, err := settings.Boot(ctx, cfg, os.LookupEnv, os.Environ())`, then `app.Run(ctx, cfg, start)`. In `app.go` thread `start` through `options`; `run` and existing tests pass a zero `Start`.
- [ ] **Step 4: Run the tests**

  Run: `go test -count=1 ./internal/settings/ ./cmd/ohs/ ./internal/app/ && go test -tags integration -count=1 -run TestBoot ./internal/settings/`
  Expected: PASS.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/settings cmd/ohs/serve.go internal/app/app.go
  git commit -m "feat(serve): start on the environment, then the stored settings"
  ```

### Task 5: `settings.Live`, the live snapshot and its reload

**Files:**
- Create: `internal/settings/live.go`
- Test: `internal/settings/live_test.go`, `internal/settings/live_integration_test.go`

**Interfaces:**
- Consumes: `Resolve`, `Start` (Task 4), `Store` (Task 3).
- Produces:
  ```go
  const ReloadEvery = 30 * time.Second
  type RateLimits struct{ API, AuthFailures, Inbound, InboundProvider, OAuth config.RateLimit } // zero is off
  type Snapshot struct {
      Revision                                             int64
      LogLevel                                             slog.Level
      RetentionDays, AuditRetentionDays, TrackingRetentionDays int
      ConsentRequired                                      bool
      BackfillDays                                         int
      WebhookPauseAfter                                    int
      WebhookRotationGrace                                 time.Duration
      RateLimits                                           RateLimits
  }
  func SnapshotOf(cfg config.Config, revision int64) Snapshot
  type Status struct {
      StartRevision, LiveRevision int64
      Skipped                     []string
      ReloadError                 string
  }
  type LiveOptions struct {
      Store   Store
      Env     func(string) (string, bool) // nil: os.LookupEnv
      Environ []string                    // nil: os.Environ()
      Config  config.Config               // the resolved start configuration
      Start   Start
  }
  type Live struct{ /* atomic.Pointer[Snapshot], mutex, subscribers, status */ }
  func NewLive(o LiveOptions) *Live
  func (l *Live) Snapshot() Snapshot
  func (l *Live) Status() Status
  func (l *Live) Subscribe(fn func(Snapshot)) // called after each swap, in the goroutine that swapped
  func (l *Live) Reload(ctx context.Context) error // reads Revision; when it moved, Load, Resolve, swap
  func (l *Live) Run(ctx context.Context, every time.Duration) error // ReloadEvery when 0; failures logged at warn, last good snapshot kept
  // Getters for consumers:
  func (l *Live) LogLevel() slog.Level
  func (l *Live) RetentionDays() int
  func (l *Live) AuditRetentionDays() int
  func (l *Live) TrackingRetentionDays() int
  func (l *Live) ConsentRequired() bool
  func (l *Live) BackfillDays() int
  func (l *Live) WebhookPauseAfter() int
  func (l *Live) WebhookRotationGrace() time.Duration
  func (l *Live) RateLimits() RateLimits
  ```
  `liveFields map[string]func(Snapshot) any` maps each `ApplyLive` name to its field. A reload logs at `info` the names whose live value changed, never values.

- [ ] **Step 1: Write the failing tests**:
  - `TestEveryLiveSettingIsInTheSnapshot` (unit): the keys of `liveFields` equal the names with `ApplyLive`.
  - `TestLiveReloadSwaps` (unit, fake `Store`): revision 0 to 1 with stored `OHS_WEBHOOK_PAUSE_AFTER=9` gives `WebhookPauseAfter() == 9`, `Status().LiveRevision == 1`, one call of each subscriber; same revision again does not call `Load`.
  - `TestLiveReloadFailureKeepsLastGood` (unit): `Load` error leaves the snapshot and sets `Status().ReloadError`; the next good reload clears it.
  - `TestLiveIgnoresRestartSettings` (unit): a stored `OHS_INGEST_CONCURRENCY` change bumps `LiveRevision` but no snapshot field.
  - `TestLiveSecondProcessWithinOneInterval` (integration): two `Live` on one database, `Run` with `every = 100ms`; `PG.Apply` through the first; the second's `WebhookPauseAfter()` changes within 1 s.
- [ ] **Step 2: Run them to verify they fail**

  Run: `go test -count=1 -run 'TestEveryLive|TestLive' ./internal/settings/`
  Expected: FAIL, `undefined: NewLive`.
- [ ] **Step 3: Implement** `live.go`, copying the structure of `providerset.Set.Reload` and `Run` (mutex serializes reloads, `atomic.Pointer` for readers).
- [ ] **Step 4: Run the tests**

  Run: `go test -count=1 ./internal/settings/ && go test -tags integration -count=1 -run TestLive ./internal/settings/`
  Expected: PASS.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/settings
  git commit -m "feat(settings): reload the live settings every 30 seconds"
  ```

### Task 6: View, Patch and Export

**Files:**
- Create: `internal/settings/manage.go`
- Test: `internal/settings/manage_test.go`, `internal/settings/manage_integration_test.go`

**Interfaces:**
- Consumes: `Live` (Task 5), `Store` (Task 3), `Resolve`, `SafeMessage` (Task 4).
- Produces:
  ```go
  type Source string // SourceEnv "env", SourceConsole "console", SourceDefault "default"
  type Entry struct {
      Setting     config.Setting
      Source      Source
      Value       *string // effective; "set" or nil for a secret
      StoredValue *string // when Source is env and a value is stored; "set" for a secret
      Invalid     string  // message, "" when valid
  }
  type View struct {
      Revision int64
      Entries  []Entry // registry order
  }
  type SettingError struct{ Name, Message string }
  type LockedError struct{ Settings []SettingError }  // 409
  type InvalidError struct{ Settings []SettingError } // 422
  type PatchResult struct {
      Revision                 int64
      Changed, RestartRequired []string // sorted
  }
  func (l *Live) View(ctx context.Context) (View, error) // fresh Store.Load, not the snapshot
  func (l *Live) Patch(ctx context.Context, ifRevision int64, changes map[string]*string, by uuid.UUID) (PatchResult, error)
  func (l *Live) Export(ctx context.Context) ([]byte, error) // the .env text of the JSON contract
  ```
  `Patch` order: unknown name with a value, empty string, value over 4096 bytes or with a control character: `InvalidError`; bootstrap or `config.EnvSet`: `LockedError` (checked before validation, and both errors are collected over the whole body before answering); `Validate` on each value; dry run `Resolve` on the current rows with the changes applied, and every name it skips that the current rows do not skip goes to `InvalidError`; then `Store.Apply` (`ErrRevisionMoved` passes through); then `l.Reload(ctx)` at once. A no-op body still checks `ifRevision`. Every message goes through `SafeMessage`.

- [ ] **Step 1: Write the failing tests**:
  - `TestViewSources` (unit, fake store, env map): env `OHS_LOG_LEVEL=debug` with stored `warn` gives source `env`, value `debug`, stored_value `warn`; env `OHS_RETENTION_DAYS=""` with stored `30` gives source `console`; nothing set gives `default` and `Default`; `OHS_METRICS_TOKEN` stored gives value `"set"`, unset gives nil; stored `abc` for an int gives source `default` and `Invalid` set.
  - `TestPatchLocked`: `{"OHS_LISTEN": ":9", "OHS_LOG_LEVEL": "debug"}` with env `OHS_LOG_LEVEL=info` gives `LockedError` naming both, messages starting `bootstrap:` and `env:`, and `Store.Apply` never called.
  - `TestPatchInvalid`: `{"OHS_WEBHOOK_PAUSE_AFTER": "abc", "OHS_GONE": "1", "OHS_RETENTION_DAYS": ""}` gives three entries; `{"OHS_WEBHOOK_CONCURRENCY": "2"}` with stored per tenant 4 gives one entry named `OHS_WEBHOOK_PER_TENANT`; `{"OHS_METRICS_TOKEN": "short"}` gives a message without `short`.
  - `TestPatchUnknownRowRemoval`: stored `OHS_GONE`, body `{"OHS_GONE": null}` succeeds and deletes it.
  - `TestPatchRestartRequired`: `{"OHS_LOG_LEVEL": "debug", "OHS_INGEST_CONCURRENCY": "4"}` gives `Changed` both, `RestartRequired == ["OHS_INGEST_CONCURRENCY"]`, `Change.Restart` true, and `l.LogLevel()` is debug right after.
  - `TestExportFormat`: golden comparison of a small registry state: header line, section lines, quoting of `a b#c`, a set secret as `# set, value not exported` then `OHS_METRICS_TOKEN=`, and no secret value anywhere in the bytes.
  - `TestPatchAtomic` (integration): a body with one valid and one invalid value stores nothing and leaves the revision; `TestPatch412` (integration): stale `ifRevision` gives `ErrRevisionMoved`; `TestPatchConcurrent` (integration): two `Patch` at once with the same revision, one result and one `ErrRevisionMoved`, revision moved by one.
- [ ] **Step 2: Run them to verify they fail**

  Run: `go test -count=1 -run 'TestView|TestPatch|TestExport' ./internal/settings/`
  Expected: FAIL, `undefined: (*Live).View`.
- [ ] **Step 3: Implement** `manage.go`.
- [ ] **Step 4: Run the tests**

  Run: `go test -count=1 ./internal/settings/ && go test -tags integration -count=1 -run TestPatch ./internal/settings/`
  Expected: PASS.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/settings
  git commit -m "feat(settings): view, patch all or nothing, and export"
  ```

### Task 7: Process heartbeats

**Files:**
- Create: `internal/settings/heartbeat.go`
- Modify: `internal/etl/automation/automation.go` (a `heartbeats` task), `internal/app/app.go` (start the writer)
- Test: `internal/settings/heartbeat_integration_test.go`

**Interfaces:**
- Consumes: `Live.Status`, `Live.Subscribe` (Task 5).
- Produces:
  ```go
  const (
      HeartbeatEvery       = 30 * time.Second
      HeartbeatHideAfter   = 2 * time.Minute
      HeartbeatDeleteAfter = 5 * time.Minute
      StaleAfter           = 60 * time.Second
  )
  type Heartbeat struct {
      ID        uuid.UUID // random at start
      Host      string
      Roles     []string  // sorted
      Version   string
      StartedAt time.Time
  }
  type HeartbeatWriter struct{ /* pool, live, hb */ }
  func NewHeartbeatWriter(pool *pgxpool.Pool, live *Live, hb Heartbeat) *HeartbeatWriter
  func (w *HeartbeatWriter) Beat(ctx context.Context) error // upsert with Status(); seen_at = now()
  func (w *HeartbeatWriter) Run(ctx context.Context, every time.Duration) error // beat at once, on every swap, every interval; deletes its row on stop (best effort, 2 s)
  type Process struct {
      Heartbeat
      SeenAt                      time.Time
      StartRevision, LiveRevision int64
      Skipped                     []string
      ReloadError                 string
      Behind, Stale               bool
  }
  func ListProcesses(ctx context.Context, db *pgxpool.Pool) ([]Process, int64, error) // rows seen within HeartbeatHideAfter, and the current revision
  func PruneHeartbeats(ctx context.Context, db *pgxpool.Pool) (int64, error) // seen_at older than HeartbeatDeleteAfter
  func (l *Live) Processes(ctx context.Context) ([]Process, int64, error)
  ```
  Behind and stale are computed in SQL against the revision row (see Spec gaps). `automation.Deps` gains `PruneHeartbeats func(context.Context) (int64, error)`, run as task `heartbeats` every `DefaultHeartbeatsEvery = 24 * time.Hour` when set.

- [ ] **Step 1: Write the failing tests**:
  - `TestHeartbeatUpsert`: two `Beat` calls leave one row; `skipped` and `reload_error` follow `Status()`.
  - `TestProcessesBehindAndStale`: a process that started at revision 1, then a restart change to revision 2: `Behind` true; a live change with `changed_at` set 61 s back and `live_revision` 1: `Stale` true; at 59 s: false.
  - `TestProcessesHideAndPrune`: a row with `seen_at` 3 min back is not listed; `PruneHeartbeats` deletes a row 6 min back and keeps the 3 min one.
  - `TestHeartbeatGoneOnStop`: cancel `Run`; the row is deleted.
- [ ] **Step 2: Run them to verify they fail**

  Run: `go test -tags integration -count=1 -run 'TestHeartbeat|TestProcesses' ./internal/settings/`
  Expected: FAIL, `undefined: NewHeartbeatWriter`.
- [ ] **Step 3: Implement** `heartbeat.go`, the automation task, and in `runWith` `goRole("heartbeat", ...)` with `Host` from `os.Hostname()`, `Version` from `app.Version`, `StartedAt` the start time.
- [ ] **Step 4: Run the tests**

  Run: `go test -count=1 ./internal/etl/automation/ ./internal/app/ && go test -tags integration -count=1 -run 'TestHeartbeat|TestProcesses' ./internal/settings/`
  Expected: PASS.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/settings internal/etl/automation internal/app
  git commit -m "feat(settings): every process reports the settings it runs"
  ```

### Task 8: Live consumers outside the API

**Files:**
- Modify: `internal/app/app.go`, `internal/app/wiring.go`, `internal/etl/worker.go`, `internal/etl/automation/automation.go`, `internal/collect/connections.go`, `internal/collect/scheduler.go`, `internal/webhook/worker.go`, `internal/webhook/endpoints.go`
- Test: `internal/etl/automation/scheduler_test.go`, `internal/collect/windows_test.go`, `internal/webhook/worker_test.go` (or the existing unit file for the worker), `internal/app/app_integration_test.go`

**Interfaces:**
- Consumes: the getters of `Live` (Task 5).
- Produces (each static field stays for callers that do not set the getter):
  ```go
  func etl.WithConsentRequiredFunc(f func() bool) WorkerOption
  // automation.Deps
  RetentionDaysFunc, AuditRetentionDaysFunc, TrackingRetentionDaysFunc func() int
  // collect.Deps
  BackfillWindowFunc func() time.Duration // wins over BackfillWindow when set
  // webhook.WorkerConfig
  PauseAfterFunc func() int // read per claim; same meaning as PauseAfter (0 default, negative off)
  func webhook.WithRotationGraceFunc(f func() time.Duration) StoreOption // read per rotation; 0 means RotationGrace
  func newLogger(w io.Writer, level slog.Leveler) *slog.Logger // app: a *slog.LevelVar set by Live.Subscribe
  // app options: reloadEvery time.Duration (0: settings.ReloadEvery), so the app test can shorten it
  ```
  With a `...Func` set, `automation.Start` registers the retention, audit and tracking tasks always; each run reads the getter and does nothing when it says off.

- [ ] **Step 1: Write the failing tests**:
  - `TestRetentionTasksReadTheGetter` (automation): with `RetentionDaysFunc` returning 0 then 30, the first tick prunes nothing, the second prunes with 30 days (fake `now`, counted calls).
  - `TestBackfillWindowFunc` (collect): the getter wins over the static field and is read per new connection.
  - `TestPauseAfterFunc` (webhook): the claim and the failure count use the getter's current value; changing it from 5 to 2 pauses an endpoint at its second failure.
  - `TestRotationGraceFunc` (webhook, existing store tests' style): a rotation after the getter changes uses the new grace.
  - `TestLiveSettingsApplyWithoutRestart` (app integration): start `runWith` with `options.lookup` set to a map, save `OHS_LOG_LEVEL=debug` with `PG.Apply`, wait for a debug line in the captured logs within two reload intervals (`ReloadEvery` shortened through `options`).
- [ ] **Step 2: Run them to verify they fail**

  Run: `go test -count=1 -run 'TestRetentionTasksReadTheGetter|TestBackfillWindowFunc|TestPauseAfterFunc|TestRotationGraceFunc' ./internal/etl/automation/ ./internal/collect/ ./internal/webhook/`
  Expected: FAIL, undefined fields.
- [ ] **Step 3: Implement** the options and fields, then wire them in `runWith`: `live := settings.NewLive(...)`, `goRole("settings", func() error { return live.Run(ctx, opts.reloadEvery) })`, `live.Subscribe` sets the `LevelVar`, and each consumer above gets its getter (`BackfillWindowFunc` converts days with `max(days, 0) * 24h`).
- [ ] **Step 4: Run the tests**

  Run: `go test -count=1 ./internal/... && go test -tags integration -count=1 -run TestLiveSettingsApplyWithoutRestart ./internal/app/`
  Expected: PASS.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/app internal/etl internal/collect internal/webhook
  git commit -m "feat: live settings reach their consumers without a restart"
  ```

### Task 9: Live rate limits in the API

**Files:**
- Modify: `internal/api/router.go`, `internal/api/ratelimit.go`, `internal/api/guard.go`, `internal/api/handlers_collect.go`, `internal/api/handlers_setup.go`, `internal/api/handlers_shares.go`, `internal/app/wiring.go`
- Test: `internal/api/ratelimit_test.go`

**Interfaces:**
- Consumes: `Live.RateLimits` (Task 5).
- Produces:
  ```go
  type RateLimits struct{ API, AuthFailures, Inbound, InboundProvider, OAuth RateLimit } // exact values, zero is off
  // Deps
  RateLimits func() RateLimits // when set, wins over the static rate limit fields and is read per request
  type liveLimiter struct{ /* get func() (perAddr, global RateLimit); cur atomic.Pointer to {perAddr, global, *rateLimiter} */ }
  func newLiveLimiter(get func() (perAddr, global RateLimit)) *liveLimiter
  func (l *liveLimiter) current() *rateLimiter // rebuilds (counters full) when the pair changed
  ```
  `s.rateLimit` takes a `*liveLimiter` and checks `on()` per request, so a limit turned on after mount applies. `limitedLookup` and `authFailures` read through `liveLimiter` too. Without `Deps.RateLimits`, a fixed getter built from the static fields keeps today's behavior, so existing tests stand. `app` sets `d.RateLimits` from `live.RateLimits()` converting each `config.RateLimit`.

- [ ] **Step 1: Write the failing tests** in `ratelimit_test.go`:
  - `TestLiveLimiterRebuildsOnChange`: getter at `1/1`, two requests from one address: second is 429; getter changes to `100/100`: next request passes (counters restarted full).
  - `TestLiveLimiterOffThenOn`: getter starts at zero (off), 10 requests pass; switches to `1/1`: the second request after the switch is 429.
  - `TestStaticLimitsUnchanged`: a `Deps` without `RateLimits` limits exactly as before (reuse an existing table case).
- [ ] **Step 2: Run them to verify they fail**

  Run: `go test -count=1 -run 'TestLiveLimiter|TestStaticLimits' ./internal/api/`
  Expected: FAIL, `undefined: newLiveLimiter`.
- [ ] **Step 3: Implement** as above.
- [ ] **Step 4: Run the tests**

  Run: `go test -count=1 ./internal/api/ ./internal/app/`
  Expected: PASS.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/api internal/app
  git commit -m "feat(api): rate limits follow the live settings"
  ```

### Task 10: The four routes, audit detail and OpenAPI

**Files:**
- Create: `internal/api/handlers_admin_settings.go`
- Modify: `internal/api/handlers_admin.go` (`mountAdmin`), `internal/api/router.go` (`Deps.Settings`), `internal/api/handlers_audit.go` (notes merged into the event), `internal/api/problem.go` (`settingsProblem`), `internal/app/app.go` (`deps.Settings = live`), `api/openapi.yaml`, generated files of `make sdk`
- Test: `internal/api/admin_settings_integration_test.go`, `internal/api/scopes_test.go` (`everyRoute` sets `d.Settings`)

**Interfaces:**
- Consumes: `Live.View`, `Live.Patch`, `Live.Export`, `Live.Processes`, `LockedError`, `InvalidError`, `ErrRevisionMoved` (Tasks 6 and 7).
- Produces:
  ```go
  // Deps
  Settings *settings.Live // nil: the four routes are not mounted
  func (s *server) listAdminSettings(w http.ResponseWriter, r *http.Request)   // GET /v1/admin/settings
  func (s *server) patchAdminSettings(w http.ResponseWriter, r *http.Request)  // PATCH /v1/admin/settings
  func (s *server) exportAdminSettings(w http.ResponseWriter, r *http.Request) // GET /v1/admin/settings/export
  func (s *server) listAdminProcesses(w http.ResponseWriter, r *http.Request)  // GET /v1/admin/processes
  func parseIfMatch(h string) (revision int64, ok bool) // `"5"` or `W/"5"`
  func auditNote(r *http.Request, key string, value any) // detail added to the request's own audit event
  type adminSettingJSON, adminSettingsPatchJSON, adminProcessJSON // the JSON contract above
  type settingsProblem struct{ Type, Title string; Status int; Detail, RequestID string; Settings []settingErrorJSON } // json: type, title, status, detail, request_id, settings
  ```
  `instrument` puts an empty notes map in the context; `s.audit` merges it into `ev.Detail`. `patchAdminSettings` calls `auditNote(r, "settings", result.Changed)` and `auditNote(r, "revision", result.Revision)` on 200. Actions stay `config.read` and `config.write` (`audit.Classify` default). OpenAPI: four operations under tag `admin`, `x-required-scope` `admin:read` for the three GETs and `admin:write` for the PATCH, schemas `AdminSetting`, `AdminSettings`, `AdminSettingsPatch`, `AdminSettingsProblem` (`allOf` Problem plus `settings`), `AdminProcess`, `AdminProcesses`, responses 400, 401, 403, 409, 412, 422, 428 on the PATCH.

- [ ] **Step 1: Write the failing tests** in `admin_settings_integration_test.go` (harness `newEnv(t, func(d *api.Deps) { d.Settings = ... })`, `e.do(method, path, key, body, hdr...)`):
  - `TestAdminSettingsGet`: 200, `ETag` equals `"<revision>"`, `Cache-Control: no-store`, `data` holds every registry name in order with the fields of the contract; tenant B (not admin) gets 403.
  - `TestAdminSettingsPatch`: with the ETag, `{"OHS_WEBHOOK_PAUSE_AFTER": "9"}` gives 200, `revision` +1, `changed` and `restart_required: []`; a second `PATCH` with the old ETag gives 412 with the current `ETag`; no `If-Match` gives 428; `If-Match: *` gives 400; `W/"<revision>"` passes; with `Accept-Encoding: gzip` on the GET, the weak ETag it returns passes.
  - `TestAdminSettingsConflictAndInvalid`: `{"OHS_LISTEN": ":1"}` gives 409 with `settings[0].name == "OHS_LISTEN"`; `{"OHS_WEBHOOK_PAUSE_AFTER": "x", "OHS_RETENTION_DAYS": "-2"}` gives 422 with two entries and the revision unchanged; `{"OHS_X": 5}` gives 400.
  - `TestAdminSettingsSecretsNeverServed`: save `OHS_METRICS_TOKEN`; the GET body, the export body, `GET /v1/audit?action=config.write` and the captured logs never contain it; the audit event's `detail.settings` is `["OHS_METRICS_TOKEN"]`.
  - `TestAdminSettingsExport`: 200, the headers of the contract, a `OHS_WEBHOOK_PAUSE_AFTER=9` line after the save.
  - `TestAdminProcesses`: with a `HeartbeatWriter` beating, 200 with one entry, `behind` false; after a restart setting is saved, `behind` true.
  Unit: `TestParseIfMatch` (`"5"`, `W/"5"`, `5`, `*`, `"x"`, `""`).
- [ ] **Step 2: Run them to verify they fail**

  Run: `go test -count=1 -run TestParseIfMatch ./internal/api/ && go test -tags integration -count=1 -run TestAdminSettings ./internal/api/`
  Expected: FAIL, 404 on `/v1/admin/settings`.
- [ ] **Step 3: Implement** the handlers, the audit notes, the OpenAPI entries; then run `make sdk` to regenerate the SDKs and the collection.
- [ ] **Step 4: Run the tests**

  Run: `go test -count=1 ./internal/api/ ./internal/sdkgen/ ./internal/docscheck/ && go test -tags integration -count=1 -run 'TestAdminSettings|TestAdminProcesses' ./internal/api/ && make contract`
  Expected: PASS, including `TestEveryAuthenticatedRouteHasAScope` with the four new operations.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/api internal/app api/openapi.yaml sdks api/collections
  git commit -m "feat(api): settings and processes routes for the instance admin"
  ```

### Task 11: `ohs doctor` and the docs

**Files:**
- Modify: `internal/app/doctor.go`, `docs/console.md`, `docs/self-hosting.md` (Configuration), `docs/api.md` (admin table), `docs/audit-log.md` (`config.write` detail), `.env.example` (header line), `docs/specs/admin-console.md` (pointer), `CHANGELOG.md` (Unreleased)
- Test: `internal/app/doctor_integration_test.go` (or the existing doctor test file)

**Interfaces:**
- Consumes: `settings.Resolve` unknown list, `Store.Load` (Tasks 3 and 4).
- Produces: a doctor line `warn  settings: stored values no release setting reads any more: OHS_X, OHS_Y (remove them with PATCH /v1/admin/settings {"OHS_X": null})` and `warn  settings: stored value skipped: OHS_X: <safe message>` per invalid one; `skip  settings: ...` before migration 0056.

- [ ] **Step 1: Write the failing test** `TestDoctorListsStoredSettings`: rows `OHS_GONE=1` and `OHS_WEBHOOK_PAUSE_AFTER=abc`; the doctor output has both warn lines and the process exit stays ok.
- [ ] **Step 2: Run it to verify it fails**

  Run: `go test -tags integration -count=1 -run TestDoctorListsStoredSettings ./internal/app/`
  Expected: FAIL, no `settings:` line.
- [ ] **Step 3: Implement** the doctor check, then the docs: `console.md` (what Settings shows, sources, apply modes, the four routes), `self-hosting.md` (precedence and the three apply modes with their lists), `api.md` (four rows in the admin table and a `PATCH` example with `If-Match`), `audit-log.md`, `.env.example` (after the first line: `# Most settings can also be saved from the console (docs/console.md); a variable set here wins.`), `admin-console.md` (a line pointing at console-v2.md), `CHANGELOG.md` (migrations 0056 and 0057, the routes).
- [ ] **Step 4: Run the checks**

  Run: `go test -count=1 ./... && go test -tags integration -count=1 -run TestDoctor ./internal/app/ && make lint && scripts/guard.sh`
  Expected: PASS; `TestEnvExampleNamesTheRegistry` still passes with the new header line.
- [ ] **Step 5: Commit**

  ```bash
  git add internal/app docs .env.example CHANGELOG.md
  git commit -m "docs: runtime settings, precedence and apply modes"
  ```

## Self-review

- Spec coverage: 1.1 registry, `.env.example` test, source scan (Task 1); 1.2 precedence (Tasks 1, 4, 6); 1.3 apply modes and live consumers (Tasks 1, 5, 8, 9); 1.4 storage, sealing, rekey, doctor listing (Tasks 2, 3, 11); 1.5 start (Task 4); 1.6 live reload (Task 5); 1.7 heartbeats, behind, stale (Tasks 2, 7); 1.8 routes and audit (Task 10); section 4 Go unit, Go integration and contract lines for settings (Tasks 1 to 10); 6.1 docs (Task 11). The session tests of section 4 belong to plan 2.
- Types: `config.Setting`, `config.Apply`, `config.Stacked`, `settings.Live`, `settings.Snapshot`, `settings.RateLimits`, `api.RateLimits`, `settings.HeartbeatWriter`, `settings.Process` are defined once and used with the same names in later tasks.
- Final run before the branch is reviewed: `make test && make integration && make lint`.
