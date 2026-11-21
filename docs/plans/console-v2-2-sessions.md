# Console Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The console signs in with a tenant key once and then holds a server session (an
`HttpOnly` cookie and a CSRF token), never the key; the current hand-written console moves to it.

**Architecture:** A new package `internal/consolesession` owns the table of migration 0058 and
the session lifecycle. `internal/auth` learns a second credential, the `ohs_console` cookie,
resolved per request through a function the api server hands the request's `Slot`, so `auth`
stays free of the database. `internal/api` resolves sessions (origin, CSRF, read only, rate
limit, last seen), serves the four `/console/session` routes, and records `actor_tenant_id`, a
new column of `ohs_core.audit_events`. The page's `lib.mjs` drops the key for the cookie and a
CSRF token held in memory.

**Tech Stack:** Go (net/http, pgx v5), Postgres migrations (golang-migrate), plain ES modules
tested with `node --test`.

**Spec:** [specs/console-v2.md](../specs/console-v2.md), section 2, section 4 (session tests),
section 6 item 2. Plan 1 ([console-v2-1-settings.md](console-v2-1-settings.md)) is written in
parallel; plan 3 (React console) consumes the names pinned in Tasks 3 and 5.

## Global Constraints

- Migration number: `0058_console_sessions`. Plan 1 takes 0056 and 0057; this plan does not
  depend on them (the migrations directory already has gaps).
- Cookie: name `ohs_console`, `HttpOnly`, `SameSite=Strict`, `Path=/`, `Secure` unless the public
  URL is `http://localhost` or `127.0.0.1`. No `Max-Age` (a browser session cookie); the server
  enforces the lifetime.
- CSRF header: `X-OHS-CSRF`, needed on every method but `GET`, `HEAD` and `OPTIONS` of a cookie
  request.
- Session token: 32 random bytes; stored as SHA-256 hex (`auth.Hash`). CSRF token: 32 random
  bytes, stored the same way. Both travel base64url without padding (43 characters).
- Lifetime: 12 hours without a request (`IdleTimeout`), 7 days after creation (`MaxAge`), or
  when its API key is revoked or expired, or its tenant deleted. `last_seen_at` written at most
  once a minute (`TouchEvery`). Ended rows deleted by a daily `etl` job.
- Sign in failures count against `OHS_RATE_LIMIT_AUTH_FAILURES` like any bad key (the same
  `authFailures` and the same `auth.failed` audit event).
- `OHS_CONSOLE=read-only` makes every new session read only, enforced by the server. A bearer
  key keeps full rights.
- The session routes exist only when `OHS_CONSOLE` is `true` or `read-only`; with the console
  off the cookie is never read.
- Bearer wins: a request with an `Authorization` header is authenticated by it, unchanged, and
  its cookie is ignored.
- The key is never stored, logged, returned or written to an audit event; the session token and
  CSRF token never appear in a log line or an audit event.
- `go build` stays free of Node. JavaScript tests run with `make test-js` (an explicit file list
  in the Makefile, not a glob: keep it that way unless a new test file is added).
- No new dependency.
- Merge note: plan 1 also adds a task to `internal/etl/automation/automation.go` and edits the
  task list asserted by `TestStartRegistersRetentionOnlyWhenEnabled`; whichever lands second
  rebases that list.

## Review Focus

1. A second tab: `GET /console/session` on a reload rotates the CSRF token, so another open tab
   holds a stale one. Its next write must refresh the token once and succeed, not fail with 403
   (Task 6, `api retries a write once after a stale CSRF token`).
2. A bearer key and a cookie on the same request (a script run from the browser, a proxy that
   forwards cookies): the bearer key must win with no CSRF asked (Task 3,
   `TestBearerWinsOverCookie`).
3. A TLS-terminating proxy with `OHS_PUBLIC_URL=https://...` and plain http to the server: the
   cookie must be `Secure` and the `Origin` compared with the public URL, not with `r.Host`
   (Task 4, `TestSessionOriginAndSecure`).
4. The console over plain http on a host that is not local and without `OHS_PUBLIC_URL`: the
   browser would drop a `Secure` cookie silently and the page would loop on sign in. Sign in
   must refuse with a message saying why (Task 5, `TestConsoleSignInRefusedOverPlainHTTP`).
5. A page loaded after the session ended (idle, revoked key): the page must show sign in, not an
   error toast (Task 6, `loadSession answers null on 401`).

---

### Task 1: Migration 0058 and the session store

**Files:**
- Create: `internal/db/migrations/0058_console_sessions.up.sql`, `0058_console_sessions.down.sql`
- Create: `internal/consolesession/consolesession.go`
- Test: `internal/consolesession/consolesession_integration_test.go`
- Modify: `internal/etl/automation/automation.go` (one task), `automation_integration_test.go`

**Interfaces:**
- Consumes: `auth.Hash(string) string`, `ohs_core.api_keys`, `ohs_core.tenants`.
- Produces:

```go
package consolesession

const (
	IdleTimeout = 12 * time.Hour
	MaxAge      = 7 * 24 * time.Hour
	TouchEvery  = time.Minute
)

var (
	ErrNotFound       = errors.New("consolesession: no such session") // unknown, ended, key revoked
	ErrTenantNotFound = errors.New("consolesession: no such tenant")
)

type Session struct {
	ID, TenantID, APIKeyID, ActingTenantID uuid.UUID
	ReadOnly                               bool
	CSRFHash                               string // SHA-256 hex
	CreatedAt, LastSeenAt                  time.Time
	TenantName                             string
	TenantAdmin                            bool
	ActingTenantName                       string
	ActingTenantAdmin                      bool
	KeyPrefix                              string // auth.KeyPrefixLen characters
	KeyScopes                              []string
}

func (s Session) ExpiresAt() time.Time // CreatedAt + MaxAge

type Store struct{ /* pool */ }

func New(pool *pgxpool.Pool) *Store
func NewSecret() (string, error) // 32 random bytes, base64.RawURLEncoding
func (s *Store) Create(ctx context.Context, tenantID, apiKeyID uuid.UUID, readOnly bool) (token, csrf string, sess Session, err error)
func (s *Store) Lookup(ctx context.Context, tokenHash string) (Session, error)
func (s *Store) RotateCSRF(ctx context.Context, id uuid.UUID) (string, error)
func (s *Store) SetActingTenant(ctx context.Context, id, tenantID uuid.UUID) (Session, error)
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error
func (s *Store) PurgeEnded(ctx context.Context) (int64, error)
```

- `automation.Intervals.ConsoleSessions`, `automation.DefaultConsoleSessionsEvery = 24 * time.Hour`,
  task name `"console_sessions"`, registered right after `"shares"`, always (rows may outlive
  `OHS_CONSOLE=false`).

The table, with a header comment in the style of 0041:

```sql
CREATE TABLE ohs_core.console_sessions (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash       text        NOT NULL,
    tenant_id        uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    api_key_id       uuid        NOT NULL REFERENCES ohs_core.api_keys (id) ON DELETE CASCADE,
    acting_tenant_id uuid        NOT NULL REFERENCES ohs_core.tenants (id) ON DELETE CASCADE,
    read_only        boolean     NOT NULL,
    csrf_hash        text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT console_sessions_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX console_sessions_api_key ON ohs_core.console_sessions (api_key_id);
CREATE INDEX console_sessions_last_seen ON ohs_core.console_sessions (last_seen_at);
ALTER TABLE ohs_core.audit_events ADD COLUMN actor_tenant_id uuid;
```

Down: `ALTER TABLE ohs_core.audit_events DROP COLUMN actor_tenant_id; DROP TABLE ohs_core.console_sessions;`

- [ ] **Step 1: Write the failing tests** (`//go:build integration`, `db.TestDB(t)`, two tenants
  from `tenant.Store.CreateTenant`, the key id from `LookupKey`). Age rows with
  `UPDATE ohs_core.console_sessions SET created_at/last_seen_at = now() - interval '...'`.

```go
func TestCreateAndLookup(t *testing.T)
// token and csrf are 43 chars and differ; Lookup(auth.Hash(token)) returns the tenant,
// the key id, ActingTenantID == TenantID, ReadOnly as given, KeyPrefix of the key,
// TenantAdmin of the tenant; no row holds token or csrf in clear (SELECT * scanned as text).
func TestLookupIdleAndAbsoluteExpiry(t *testing.T)
// last_seen_at 12h01m ago: ErrNotFound; 11h59m ago: found.
// created_at 7d01m ago with last_seen_at now: ErrNotFound.
func TestLookupRefusesRevokedOrExpiredKey(t *testing.T)
// RevokeKeyByID then Lookup: ErrNotFound; api_keys.expires_at in the past: ErrNotFound.
func TestLookupTouchesAtMostOncePerMinute(t *testing.T)
// last_seen_at 30s ago: unchanged after Lookup; 2 min ago: moved to within 1s of now().
func TestRotateCSRF(t *testing.T)
// new token differs; csrf_hash == auth.Hash(new); old hash gone.
func TestSetActingTenant(t *testing.T)
// to tenant B: Lookup shows ActingTenantID B and ActingTenantName "Other";
// unknown tenant id: ErrTenantNotFound, row unchanged; ErrNotFound for an unknown session.
func TestSessionEndsWithItsTenant(t *testing.T)
// DELETE FROM ohs_core.tenants for the acting tenant: the row is gone (cascade).
func TestPurgeEnded(t *testing.T)
// one live, one idle, one past MaxAge, one with a revoked key: PurgeEnded returns 3; the live one stays.
```

  In `automation_integration_test.go`, add `"console_sessions"` after `"shares"` in both lists of
  `TestStartRegistersRetentionOnlyWhenEnabled` and `require.Equal(t, 24*time.Hour, got["console_sessions"])`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -tags integration -count=1 ./internal/consolesession/ ./internal/etl/automation/ -run 'Test(CreateAndLookup|Lookup|RotateCSRF|SetActingTenant|SessionEnds|PurgeEnded|StartRegistersRetention)'`
Expected: FAIL (package and table missing).

- [ ] **Step 3: Write the migration and implement the store.** `Lookup` is one query joining
  `api_keys` (not revoked, not expired) and both tenants, with `last_seen_at > now() - IdleTimeout`
  and `created_at > now() - MaxAge` in SQL, then a best effort `UPDATE ... SET last_seen_at = now()`
  when the read `last_seen_at` is older than `TouchEvery` (as `LookupKey` touches keys).
  `PurgeEnded` deletes idle, too old and revoked or expired key rows in one statement. Register
  the automation task, logging `"console sessions purged", "rows", n` when n > 0.

- [ ] **Step 4: Run to verify they pass**

Run: the command of Step 2, then `go test -count=1 ./internal/db/` (`TestLatestVersion`, `TestMigrationFiles`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/migrations/0058_* internal/consolesession internal/etl/automation
git commit -m "feat(console): console_sessions table and session store"
```

---

### Task 2: `actor_tenant_id` in the audit log

**Files:**
- Modify: `internal/audit/audit.go` (Event, actions, `Buffered`, `Classify`, `eventArgs`, `Record`, `Entry`, `List`)
- Modify: `internal/audit/writer.go` (`insertBatch`)
- Test: `internal/audit/audit_test.go`, `internal/audit/audit_integration_test.go`

**Interfaces:**
- Consumes: the column of Task 1.
- Produces:

```go
// Event
ActorTenant *uuid.UUID // the tenant of the console session behind the request; nil otherwise

// Entry
ActorTenantID *uuid.UUID `json:"actor_tenant_id"` // always present, null when absent

const (
	SessionCreate = "session.create" // sign in; detail has key_prefix
	SessionRead   = "session.read"   // GET /console/session
	SessionDelete = "session.delete" // sign out
	SessionTenant = "session.tenant" // acting tenant changed; detail has from_tenant_id, to_tenant_id
)
```

`eventColumns` becomes 12; `actor_tenant_id` is the last column of `Record` and `insertBatch`.
`Buffered(SessionRead)` is true; the other three are written synchronously.

- [ ] **Step 1: Write the failing tests**

```go
func TestClassifyConsoleSession(t *testing.T)
// Classify("POST", "POST /console/session") == SessionCreate
// Classify("GET", "GET /console/session") == SessionRead
// Classify("DELETE", "DELETE /console/session") == SessionDelete
// Classify("PUT", "PUT /console/session/tenant") == SessionTenant
func TestBufferedSessionActions(t *testing.T)
// Buffered(SessionRead) true; SessionCreate, SessionDelete, SessionTenant false.
func TestActorTenantRecordedAndListed(t *testing.T) // integration
// Record one event with ActorTenant set and one without, and the same pair through
// a Writer flush: List returns ActorTenantID equal to it, and nil for the others.
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./internal/audit/ -run 'TestClassifyConsoleSession|TestBufferedSessionActions'` and
`go test -tags integration -count=1 ./internal/audit/ -run TestActorTenantRecordedAndListed`
Expected: FAIL (undefined constants and field).

- [ ] **Step 3: Implement.** In `Classify`, a `case strings.HasPrefix(path, "/console/session")`
  before the `/v1/` cases. List the four actions in the `AuditEvent.action` description of
  `api/openapi.yaml` in Task 5.

- [ ] **Step 4: Run to verify they pass** (the commands of Step 2, then `go test -count=1 ./internal/audit/`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audit
git commit -m "feat(audit): actor_tenant_id and the console session actions"
```

---

### Task 3: The cookie credential in `internal/auth`

**Files:**
- Modify: `internal/auth/middleware.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (plan 3 reads the two constants and the details from the API answers):

```go
const (
	SessionCookie = "ohs_console"
	CSRFHeader    = "X-OHS-CSRF"
	// Problem details of a refused cookie request, matched verbatim by lib.mjs.
	DetailCSRF     = "this request needs the X-OHS-CSRF token of the console session; reload the page"
	DetailOrigin   = "a console session only accepts requests from this server's own origin"
	DetailReadOnly = "this console session is read-only (OHS_CONSOLE=read-only)"
)

// Principal, new fields
ActorTenantID uuid.UUID // the console session's own tenant; uuid.Nil for a key or token
SessionID     uuid.UUID // the console session; uuid.Nil otherwise

func (p Principal) Actor() uuid.UUID // ActorTenantID when set, else TenantID

// SessionFunc resolves the token of the ohs_console cookie for request r.
// ErrUnauthenticated answers 401, *ForbiddenError 403 with its Detail,
// *LimitedError 429; any other error is handled like a lookup error.
type SessionFunc func(r *http.Request, token string) (Principal, error)

type ForbiddenError struct{ Detail string }
func (e *ForbiddenError) Error() string

func (s *Slot) AcceptSession(fn SessionFunc)
```

In `MiddlewareAny`: when the request has no `Authorization` header, the slot has a
`SessionFunc` and the cookie is present and not empty, call it under `LookupTimeout` and handle
the outcome like a lookup; the principal then goes through the same `wants` and route scope
checks. A cookie request never reaches `OnFailure` (a session token is not guessable and an
ended session is common). Otherwise the bearer path is unchanged.

- [ ] **Step 1: Write the failing tests** (a `Slot` from `WithSlot`, a fake `SessionFunc`
  recording its calls)

```go
func TestCookieAuthenticatesWhenAccepted(t *testing.T)
// no Authorization, cookie ohs_console=tok: fn called once with "tok"; the handler sees
// the principal (From); slot.Principal() set; status 200.
func TestCookieIgnoredWithoutSessionFunc(t *testing.T)
// same request, no AcceptSession: 401 with WWW-Authenticate: Bearer; fn never called.
func TestBearerWinsOverCookie(t *testing.T)
// Authorization: Bearer <valid key> and the cookie: lookup called, fn never called, 200.
func TestSessionFuncErrors(t *testing.T)
// ErrUnauthenticated -> 401 and OnFailure not called; &ForbiddenError{DetailCSRF} -> 403
// with detail DetailCSRF; &LimitedError{RetryAfter: 2s} -> 429 with Retry-After: 2.
func TestCookiePrincipalStillNeedsScope(t *testing.T)
// fn returns KeyScopes ["users:read"], RequireScope answers "webhooks:write": 403,
// MissingScope() == "webhooks:write".
func TestPrincipalActor(t *testing.T)
// Actor() == TenantID when ActorTenantID is Nil, else ActorTenantID.
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./internal/auth/ -run 'TestCookie|TestBearerWinsOverCookie|TestSessionFuncErrors|TestPrincipalActor'`
Expected: FAIL (undefined `AcceptSession`, `SessionCookie`).

- [ ] **Step 3: Implement** in `middleware.go`. `unauthorized` keeps `WWW-Authenticate: Bearer`.

- [ ] **Step 4: Run to verify they pass** (Step 2 command, then `go test -count=1 ./internal/auth/`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): accept a console session cookie beside the bearer key"
```

---

### Task 4: Resolving a session in `internal/api`

**Files:**
- Create: `internal/api/console_session_auth.go`
- Modify: `internal/api/router.go` (`server` fields, `newServer`, `instrument`)
- Modify: `internal/api/handlers_audit.go` (`audit`), `handlers_admin_tenants.go` (`adminEvent`,
  the self check of `revokeAdminTenantKey`), `handlers_admin_tenant_routes.go` (`asTenant`)
- Test: `internal/api/console_session_test.go` (unit), `internal/api/console_session_integration_test.go`

**Interfaces:**
- Consumes: `consolesession.Store` (Task 1), `auth.SessionFunc`, `auth.Principal.Actor`, the
  constants (Task 3), `audit.Event.ActorTenant` (Task 2).
- Produces:

```go
// server, new fields
sessions *consolesession.Store // nil when the console is off
apiLimit *rateLimiter          // the limiter limitedLookup uses; nil when OHS_RATE_LIMIT_API is off

func (s *server) sessionAuth(r *http.Request, token string) (auth.Principal, error)
func sameOrigin(r *http.Request, publicURL string) bool
func cookieSecure(r *http.Request, publicURL string, trusted []netip.Prefix) bool
func plainHTTP(r *http.Request, publicURL string, trusted []netip.Prefix) bool
// sessionExempt are the patterns a read only session may call with a write method.
var sessionExempt = map[string]bool{"DELETE /console/session": true, "PUT /console/session/tenant": true}
```

Rules of `sessionAuth`, in order: `Lookup(auth.Hash(token))` (`ErrNotFound` is
`auth.ErrUnauthenticated`); `!sameOrigin` is `ForbiddenError{DetailOrigin}`; a method other than
GET, HEAD, OPTIONS whose `X-OHS-CSRF` hash differs from `CSRFHash` (`subtle.ConstantTimeCompare`)
is `ForbiddenError{DetailCSRF}`; a read only session with a method other than GET, HEAD and a
pattern not in `sessionExempt` is `ForbiddenError{DetailReadOnly}`; then `s.apiLimit.allow` keyed
like `limitKey` (`"k:" + key id`) or `LimitedError`. The principal: `TenantID` the acting tenant,
`KeyID` the API key id, `Admin` the session tenant's flag, `KeyScopes` the key's,
`ActorTenantID` the session tenant, `SessionID`.

`sameOrigin`: no `Origin` header is same origin; `Origin: null` is not; with `publicURL` set the
origin must equal its scheme and host (lower case, default port dropped); without it the
origin's host must equal `r.Host`. `cookieSecure`: false only for an http public URL on
`localhost`, `127.0.0.1` or `[::1]`, or, without a public URL, for a request that is not TLS (nor
`X-Forwarded-Proto: https` from a trusted proxy) to one of those hosts. `plainHTTP`: no public
URL, not TLS, not forwarded as https by a trusted proxy, host not local.

`instrument` calls `slot.AcceptSession(s.sessionAuth)` only when `s.sessions != nil`. `newServer`
keeps the API limiter in `s.apiLimit` and builds `s.sessions = consolesession.New(d.Pool)` when
`d.Console`. `audit` sets `ev.ActorTenant` when `p.SessionID != uuid.Nil` and records nothing for
the patterns that write their own event (`"POST /console/session"`, `"PUT /console/session/tenant"`,
Task 5). `adminEvent`, `asTenant` and `revokeAdminTenantKey` compare and name the admin by
`p.Actor()`, not `p.TenantID`, and `asTenant`'s second event carries `ActorTenant` too.

- [ ] **Step 1: Write the failing unit tests**

```go
func TestSessionOriginAndSecure(t *testing.T)
// sameOrigin: no Origin -> true; "null" -> false; publicURL "https://ohs.example.com",
// Origin "https://ohs.example.com:443" -> true, "http://ohs.example.com" -> false,
// r.Host "10.0.0.5:8080" ignored; no publicURL, r.Host "box:8080", Origin "http://box:8080" -> true,
// "http://evil:8080" -> false.
// cookieSecure: "http://localhost:8080" -> false; "http://127.0.0.1" -> false;
// "https://ohs.example.com" over plain http -> true; "" with r.Host "localhost:8080" no TLS -> false;
// "" with r.Host "box" and r.TLS set -> true.
// plainHTTP: "" with r.Host "192.168.1.10:8080" no TLS -> true; same with a trusted
// proxy's X-Forwarded-Proto: https -> false; publicURL set -> false.
```

- [ ] **Step 2: Write the failing integration tests** (`newEnv` with `Console: true`; a helper
  `e.session(key string) (cookie, csrf string)` that posts `POST /console/session` once Task 5
  exists; until then create the row with `consolesession.New(e.pool).Create`; `e.do` gains the
  cookie through its `hdr` pairs: `"Cookie", "ohs_console="+cookie`)

```go
func TestConsoleSessionAuthenticatesTenantRoutes(t *testing.T)
// GET /v1/users with the cookie only: 200, the users of tenant A.
func TestConsoleSessionCSRF(t *testing.T)
// POST /v1/users without X-OHS-CSRF: 403 detail auth.DetailCSRF; with a wrong one: 403;
// with the right one: 201. GET needs none.
func TestConsoleSessionForeignOrigin(t *testing.T)
// GET /v1/users with Origin "https://evil.example": 403 detail auth.DetailOrigin;
// with Origin equal to the request's own: 200.
func TestConsoleSessionReadOnly(t *testing.T)
// Deps.ConsoleReadOnly: POST /v1/users with a valid CSRF: 403 detail auth.DetailReadOnly;
// GET /v1/users: 200. The same tenant's bearer key: POST /v1/users 201.
func TestConsoleSessionIdleAndAbsoluteExpiry(t *testing.T)
// last_seen_at aged 12h01m: GET /v1/users 401; a new session with created_at aged 7d01m: 401.
func TestConsoleSessionEndsWithKeyRevocation(t *testing.T)
// a second key of tenant A revokes the session's key (DELETE /v1/keys/{key_id}): the cookie gets 401.
func TestConsoleSessionAuditFields(t *testing.T)
// a GET /v1/users by cookie: its event has Tenant A, ActorType api_key, ActorID the key id,
// ActorTenant A. A bearer request: ActorTenant nil.
func TestConsoleOffIgnoresCookie(t *testing.T)
// Console false, a row created by the store: GET /v1/users with the cookie only: 401.
```

- [ ] **Step 3: Run to verify they fail**

Run: `go test -count=1 ./internal/api/ -run TestSessionOriginAndSecure` and
`go test -tags integration -count=1 ./internal/api/ -run 'TestConsoleSession|TestConsoleOffIgnoresCookie'`
Expected: FAIL.

- [ ] **Step 4: Implement** `console_session_auth.go` and the router changes above.

- [ ] **Step 5: Run to verify they pass** (Step 3 commands, then `go test -count=1 ./internal/api/`
  and `go test -tags integration -count=1 ./internal/api/ -run 'TestAdmin'`, which must stay green
  after the `p.Actor()` change).
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api
git commit -m "feat(api): authenticate console sessions: origin, CSRF, read only, audit"
```

---

### Task 5: The session routes, OpenAPI and tenant switch

**Files:**
- Create: `internal/api/handlers_console_session.go`
- Modify: `internal/api/router.go` (mount beside `console.Mount`), `internal/api/scopes.go`
- Modify: `api/openapi.yaml`, `internal/api/scopes_test.go` (`specOperations`), `internal/sdkgen/model.go` (`skipped`)
- Test: `internal/api/console_session_integration_test.go`, `internal/api/router_test.go`

**Interfaces:**
- Consumes: Tasks 1 to 4; `s.lookup`, `s.refused`, `s.authFailed`, `s.record`, `decodeJSON`,
  `Store.GetTenantSummary`.
- Produces (pinned for plan 3):

| Route | Auth | Body | Answer |
|---|---|---|---|
| `POST /console/session` | none (the key is in the body) | `{"key": "ohs_sk_..."}` | 201 `ConsoleSession` with `csrf_token`, `Set-Cookie: ohs_console=...` |
| `GET /console/session` | cookie | none | 200 `ConsoleSession` with a fresh `csrf_token` (the old one stops working) |
| `DELETE /console/session` | cookie + `X-OHS-CSRF` | none | 204, row deleted, `Set-Cookie: ohs_console=; Max-Age=0` with the same attributes |
| `PUT /console/session/tenant` | cookie + `X-OHS-CSRF`, admin session | `{"tenant_id": "<uuid>"}` | 200 `ConsoleSession` without `csrf_token` |

```go
type consoleSignInJSON struct {
	Key string `json:"key"`
}
type consoleTenantSwitchJSON struct {
	TenantID uuid.UUID `json:"tenant_id"`
}
type consoleTenantJSON struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Admin bool      `json:"admin"`
}
type consoleSessionJSON struct {
	CSRFToken    string            `json:"csrf_token,omitempty"`
	Tenant       consoleTenantJSON `json:"tenant"`        // the session's own tenant
	ActingTenant consoleTenantJSON `json:"acting_tenant"` // whom requests run as
	KeyPrefix    string            `json:"key_prefix"`    // 12 characters, as /v1/keys shows
	KeyScopes    []string          `json:"key_scopes"`
	ReadOnly     bool              `json:"read_only"`
	ExpiresAt    time.Time         `json:"expires_at"`    // created + 7 days; idle ends it sooner
}

func (s *server) mountConsoleSession(mux *http.ServeMux)
func (s *server) signInConsole(w http.ResponseWriter, r *http.Request)
func (s *server) getConsoleSession(w http.ResponseWriter, r *http.Request)
func (s *server) signOutConsole(w http.ResponseWriter, r *http.Request)
func (s *server) switchConsoleTenant(w http.ResponseWriter, r *http.Request)
func setSessionCookie(w http.ResponseWriter, token string, secure bool) // token "" clears it
```

`signInConsole`, in order: `!sameOrigin` 403 `DetailOrigin`; `plainHTTP` 403 "the console signs
in over https, or on localhost; set OHS_PUBLIC_URL to the https address"; `decodeJSON` (400); a
key not starting with `auth.PrefixSecretKey` 403 "the console needs a tenant API key (ohs_sk_), not
a user token"; `s.lookup(ctx, auth.Hash(key))` with `auth.LookupTimeout`: `ErrUnauthenticated`
calls `s.refused(r)` (429 with Retry-After when limited) else `s.authFailed(r, auth.Failure{Hash,
Prefix})` and 401; `LimitedError` 429; then `!p.Can(auth.ScopeTenantRead)` 403 naming the scope;
then `Create(ctx, p.TenantID, keyID, s.d.ConsoleReadOnly)`, the cookie, `Cache-Control: no-store`,
201, and an event `SessionCreate` on the tenant with `ActorTenant` and detail `key_prefix`. The
other three handlers read `auth.From`; a principal with `SessionID == uuid.Nil` (a bearer key)
gets 401 "this route needs a console session". `switchConsoleTenant`: not `p.Admin` 403 "only a
session of the instance admin tenant switches tenant"; unknown tenant 404 via `notFound(w,
"tenant")`; records `SessionTenant` on `p.Actor()` with `from_tenant_id`, `to_tenant_id`.

`routeScopes`: `"GET /console/session"` and `"DELETE /console/session"` need `auth.ScopeTenantRead`,
`"PUT /console/session/tenant"` needs `auth.ScopeAdminRead`. `POST /console/session` is not
listed (it takes no bearer credential).

OpenAPI: tag `console`; security scheme `consoleSession` (`type: apiKey`, `in: cookie`, `name:
ohs_console`); a reusable header parameter `ConsoleCSRF` (`X-OHS-CSRF`); the four operations
(`createConsoleSession` with `security: []`, `getConsoleSession`, `deleteConsoleSession`,
`switchConsoleTenant` with `security: [{consoleSession: []}]` and `x-required-scope`), schemas
`ConsoleSignIn`, `ConsoleTenantSwitch`, `ConsoleTenant`, `ConsoleSession`; `actor_tenant_id`
(`[string, "null"]`, uuid) on `AuditEvent` and in its `required` list. `specOperations` treats a
`consoleSession` alternative as a credential (`o.public = false`). The four operation ids go in
`sdkgen.skipped` ("the console's own sign in, see docs/console.md").

- [ ] **Step 1: Write the failing tests**

```go
func TestConsoleSignIn(t *testing.T)
// POST {"key": keyA}: 201; one Set-Cookie named ohs_console, HttpOnly, SameSite=Strict, Path=/,
// not Secure (Deps.PublicURL "http://localhost:8080");
// body csrf_token 43 chars, tenant {id A, name "Acme", admin true}, acting_tenant == tenant,
// key_prefix == keyA[:12], read_only false; the body does not contain keyA;
// an audit event session.create on A with ActorTenant A and detail key_prefix.
func TestConsoleSignInSecureCookie(t *testing.T)
// PublicURL "https://ohs.example.com": the cookie is Secure.
func TestConsoleSignInFailuresAreThrottled(t *testing.T)
// RateLimitAuthFailures {Rate: 1/min, Burst: 2}: three bad keys from one address: 401, 401, 429
// with Retry-After; one auth.failed event with the key prefix; a good key then still gets 201.
func TestConsoleSignInRefusals(t *testing.T)
// a user token: 403; malformed JSON: 400; Origin "https://evil.example": 403 DetailOrigin;
// a key with scopes ["users:read"]: 403 naming tenant:read; no row created in any case.
func TestConsoleSignInRefusedOverPlainHTTP(t *testing.T)
// PublicURL "", Host "192.168.1.10:8080", no TLS: 403 with the https message, no row.
func TestConsoleSessionGetRotatesCSRF(t *testing.T)
// GET: 200 with a new csrf_token; a write with the old token: 403 DetailCSRF; with the new: passes.
func TestConsoleSignOut(t *testing.T)
// DELETE without X-OHS-CSRF: 403; with it: 204, Set-Cookie ohs_console with Max-Age=0;
// the old cookie then gets 401 on GET /console/session; a read only session signs out too.
func TestConsoleTenantSwitch(t *testing.T)
// admin session of A: PUT {"tenant_id": B}: 200, acting_tenant B, tenant A, no csrf_token;
// GET /v1/users lists B's users; GET /v1/tenant answers B; its audit event has Tenant B and
// ActorTenant A; the session.tenant event is on A with from A, to B. PUT back to A: acting A.
// unknown uuid: 404; "not-a-uuid": 400; a read only admin session may switch.
func TestConsoleTenantSwitchNeedsAdmin(t *testing.T)
// a session of B (not admin): PUT {"tenant_id": A}: 403; acting tenant unchanged.
func TestConsoleSessionRoutesNeedASession(t *testing.T)
// GET /console/session with keyA as bearer: 401 "this route needs a console session".
```

  In `router_test.go`: `TestConsoleSessionRoutesOffWithConsole`: with `Console` false, `POST
  /console/session` answers 404; with it on, `GET /console/app.mjs` is still served.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -tags integration -count=1 ./internal/api/ -run 'TestConsoleSignIn|TestConsoleSessionGet|TestConsoleSignOut|TestConsoleTenantSwitch|TestConsoleSessionRoutes'`
Expected: FAIL (404 on `/console/session`).

- [ ] **Step 3: Implement** the handlers, the mount (`if d.Console { console.Mount(...);
  s.mountConsoleSession(mux) }`: the exact patterns win over `GET /console/{asset...}`), the
  scopes, the OpenAPI entries, `specOperations` and `skipped`. Switch Task 4's test helper
  `e.session` to `POST /console/session`.

- [ ] **Step 4: Run to verify they pass**

Run: the Step 2 command, then `go test -count=1 ./internal/api/ ./internal/sdkgen/`
(`TestEveryAuthenticatedRouteHasAScope`, `TestOpenAPIServed`, the skipped list), `make sdk` and
`git diff --exit-code sdks/` (nothing generated for the console), `make contract`.
Expected: PASS, no SDK diff.

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/sdkgen api/openapi.yaml
git commit -m "feat(api): console session routes and tenant switch"
```

---

### Task 6: The current console signs in with a session

**Files:**
- Modify: `internal/console/static/lib.mjs`, `internal/console/static/app.mjs`,
  `internal/console/static/index.html` (the sign in note), `internal/console/console.go` (package comment)
- Test: `internal/console/app.test.mjs`, `internal/console/console_test.go`

**Interfaces:**
- Consumes: the routes and JSON of Task 5, `auth.CSRFHeader`, `auth.DetailCSRF` (Task 3).
- Produces (`lib.mjs`; `KEY_STORAGE` is removed):

```js
export const SESSION_PATH = "/console/session";
export const CSRF_HEADER = "X-OHS-CSRF";
export const CSRF_REFUSED = "this request needs the X-OHS-CSRF token of the console session; reload the page";
// makeApi(fetchImpl, csrf, { readOnly }): csrf is { get(): string, set(token): void }, held in
// a closure of app.mjs, never in storage. Requests use credentials "same-origin" (or "omit"
// with opts.auth === false) and never an Authorization header. A write carries CSRF_HEADER.
// A write answered 403 with detail CSRF_REFUSED calls GET SESSION_PATH once, stores its
// csrf_token and retries once. In read-only mode POST and DELETE on SESSION_PATH still pass.
export function makeApi(fetchImpl, csrf, { readOnly = false } = {})
export async function signIn(api, csrf, key)   // POST SESSION_PATH, stores csrf_token, returns the session
export async function loadSession(api, csrf)   // GET SESSION_PATH; null on 401
export async function signOut(api, csrf)       // DELETE SESSION_PATH; clears the token whatever the answer
```

`app.mjs`: `const csrf = memoryToken()` (a closure), `makeApi((u, o) => window.fetch(u, o), csrf, { readOnly })`;
on start `loadSession` decides between sign in and `render()`; the form calls `signIn` and keeps
the key in no variable after the call; `failure` on 401 clears the token and shows sign in;
the sign out button calls `signOut`. `render()` tests a `session` variable instead of `getKey()`.
The sign in note of `index.html` reads: "The key goes to this server once. The page then holds a
session cookie it cannot read, never the key."

- [ ] **Step 1: Write the failing node tests** (replace the bearer tests; `fakeFetch` as today,
  with a queue of answers for the retry test)

```js
test("api sends no Authorization header and same-origin credentials")
// init.headers.Authorization undefined; init.credentials "same-origin"; auth:false -> "omit".
test("api sends the CSRF token on writes only")
// POST carries X-OHS-CSRF equal to csrf.get(); GET carries none.
test("api retries a write once after a stale CSRF token")
// answers: 403 {detail: CSRF_REFUSED}, 200 {csrf_token: "new"}, 201 {}: three calls, the second
// GET /console/session, the third with X-OHS-CSRF "new"; a second CSRF_REFUSED is an ApiError 403.
test("api in read-only mode still signs in and out")
// readOnly: POST /console/session and DELETE /console/session sent; POST /v1/users -> ReadOnlyError.
test("signIn posts the key once and keeps only the CSRF token")
// body {"key": "ohs_sk_x"}; csrf.get() === the answer's csrf_token; the returned object has no key.
test("loadSession answers null on 401")
test("signOut clears the token even when the server refuses")
```

  In `console_test.go`, `TestScriptAvoidsUnsafeSinks`: add `"sessionStorage"` and
  `"Authorization"` to the forbidden strings; require `credentials: "same-origin"` instead of
  `"omit"` among the request options, and `makeApi((u, o) => window.fetch(u, o), csrf, { readOnly })`
  in `app.mjs`. New `TestCSRFDetailMatchesServer`: `lib.mjs` contains `auth.DetailCSRF` and
  `auth.CSRFHeader` verbatim.

- [ ] **Step 2: Run to verify they fail**

Run: `make test-js` and `go test -count=1 ./internal/console/`
Expected: FAIL.

- [ ] **Step 3: Implement** the changes above. Remove `getKey`, `setKey`, `storage` and every
  `KEY_STORAGE` use from `app.mjs`.

- [ ] **Step 4: Run to verify they pass** (Step 2 commands), then check by hand: `make build`,
  `OHS_CONSOLE=true bin/ohs serve` on a local database, sign in at `http://localhost:8080/console`,
  reload (still signed in), create a user, sign out (back to sign in, the cookie gone in the
  browser's storage panel, nothing under Session Storage).
Expected: PASS, and the walk through as described.

- [ ] **Step 5: Commit**

```bash
git add internal/console
git commit -m "feat(console): sign in with a server session, CSRF token in memory"
```

---

### Task 7: Docs

**Files:**
- Modify: `docs/console.md` (replace "No new credential and no server-side session" and "The key
  stays in the browser tab" with the session: cookie, CSRF, lifetime, read only enforced by the
  server, tenant switch, plain http only on localhost), `docs/audit-log.md` (`actor_tenant_id`,
  the four `session.*` actions), `docs/admin-api.md` (the tenant switch beside
  `/v1/admin/tenants`), `docs/security-model.md` (the console credential), `CHANGELOG.md`
  (Unreleased: Added, Changed), `docs/specs/admin-console.md` (a pointer to console-v2.md, unless
  plan 1 added it)

**Interfaces:** none.

- [ ] **Step 1: Write the docs.** State the exact names of Global Constraints; no em dash.
- [ ] **Step 2: Verify**

Run: `go test -count=1 ./internal/docscheck/` and `make lint`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add docs CHANGELOG.md
git commit -m "docs(console): console sessions"
```

---

## Self-review

- **Spec coverage.** 2.1 sign in, cookie, body, no key stored: Tasks 1, 5. Sign out, reload with a
  fresh CSRF token: Task 5. Failures counted: Task 5 (`TestConsoleSignInFailuresAreThrottled`).
  2.2 lifetime and daily purge: Task 1; at request time: Task 4. 2.3 cookie in the middleware,
  foreign `Origin`, CSRF, read only, acting tenant, `actor_tenant_id`: Tasks 2, 3, 4. 2.4 tenant
  switch: Task 5. Health values behind Preview and download, recorded as `data.read` and
  `data.export`: already true of the read and export routes (`Classify`), nothing new. Section 4
  session tests: Tasks 1, 4, 5. Section 6 item 2 and its docs: Tasks 6, 7.
- **Spec gaps decided here** (to fold back into the spec):
  1. `GET /console/session` rotates the CSRF token, which breaks other open tabs; `lib.mjs`
     refreshes once and retries (Task 6).
  2. Read only sessions may still sign out and switch tenant (`sessionExempt`); a read only admin
     needs the switch to look at other tenants.
  3. The `Secure` rule says nothing of an empty `OHS_PUBLIC_URL` or `[::1]`; decided in
     `cookieSecure`, and sign in over plain http to a host that is not local is refused rather
     than set a cookie the browser drops.
  4. Sign in checks `Origin` too (login CSRF), not only cookie requests.
  5. `actor_tenant_id` is a column of `audit_events` added by migration 0058, not a detail
     member, so `GET /v1/audit` returns it and the console can filter on it.
  6. The admin routes name the admin by the session's tenant (`Principal.Actor`), not by the
     acting tenant.
  7. Deleting the acting tenant ends the session (cascade) rather than falling back.
  8. Sign in needs the key scope `tenant:read`; the switch needs `admin:read`.
- **Type consistency.** `consolesession.Session` fields feed `consoleSessionJSON`; `auth.SessionFunc`
  matches `(*server).sessionAuth`; the detail strings live in `auth` and are checked verbatim in
  `lib.mjs` by `TestCSRFDetailMatchesServer`.
- **Proportion.** Signatures, SQL of the one table, test names and assertions; no handler bodies.
