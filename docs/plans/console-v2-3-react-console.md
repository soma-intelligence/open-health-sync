# Plan: console v2, part 3, the React console

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hand-written page at `/console` with a React, TypeScript and Vite console
typed against `api/openapi.yaml`, signed in through a server session, laid out as navigation,
content and inspector, with every page of spec section 3.4, then delete the old files.

**Architecture:** Source in `web/console/`, built by Vite into `internal/console/dist/`, which
is committed and embedded with `go:embed`, so `go build` and `go install` never need Node. The
Go handler serves a hashed asset when one matches and `index.html` for every other path under
`/console/`. The page talks to the API through one client that sends the `ohs_console` cookie
and the `X-OHS-CSRF` header and never holds a key past the sign-in form.

**Tech Stack:** Node 22, React 19, TypeScript (strict), Vite, React Router, TanStack Query,
openapi-typescript, Vitest with Testing Library and jsdom, Playwright, ESLint (flat config).
Go 1.26 for the handler.

**Spec:** [specs/console-v2.md](../specs/console-v2.md), sections 3, 4 (front unit, end to end,
contract), 5, and 6 item 3. The action list and safety rules it keeps are sections 1 and 6 of
[specs/admin-console.md](../specs/admin-console.md). Visual reference: layout C of the approved
mockups (`c-workspace-inspector.html`: top bar, navigation, content, inspector) with the tokens
of `a-codex-split.html`, as fixed by spec 3.2.

Plans 1 (settings) and 2 (sessions) are merged before this one starts:
[console-v2-1-settings.md](console-v2-1-settings.md) (its "JSON contract" section) and
[console-v2-2-sessions.md](console-v2-2-sessions.md) (Task 5 and Task 6 Interfaces). This plan
uses their field names verbatim and reads every request and answer type from the generated
`schema.ts`, so a drift fails `tsc`, not the browser. The names it relies on:

- `ConsoleSession` (`POST`, `GET /console/session`, `PUT /console/session/tenant`):
  `csrf_token` (absent from the `PUT` answer), `tenant {id, name, admin}` (the session's own
  tenant), `acting_tenant {id, name, admin}`, `key_prefix` (12 characters, as `/v1/keys`
  shows), `key_scopes`, `read_only`, `expires_at`. `GET /console/session` rotates the CSRF
  token. A stale token is a 403 whose `detail` is exactly
  `this request needs the X-OHS-CSRF token of the console session; reload the page`. Sign in
  over plain http to a host that is not local is a 403 with a message to show. A read only
  session may still sign in, sign out and switch tenant. `/v1/admin/*` authorizes on the
  session tenant's `admin`, not the acting tenant's.
- `AuditEvent.actor_tenant_id` (uuid or null): the session tenant behind an event.
- `GET /v1/admin/settings`: `{revision, data: [{name, label, section, help, type, min, max,
  values, default, default_note, secret, apply, source, value, stored_value, invalid}]}`, with
  `ETag: "<revision>"`; a secret's `value` is `"set"` or null; `default` may be `""` with a
  `default_note` to show instead.
- `PATCH /v1/admin/settings`: strings or null only (`""` is a 422; null reverts to default),
  `If-Match: "<revision>"` (428 without it, 412 when the revision moved); 200
  `{revision, changed, restart_required}`; 409 and 422 are problems with
  `settings: [{name, message}]`, and a 422 from a cross-setting check names one setting.
- `GET /v1/admin/settings/export`: `text/plain`, `filename="ohs.env"`.
- `GET /v1/admin/processes`: `{revision, data: [{id, host, roles, version, started_at,
  seen_at, start_revision, live_revision, skipped, reload_error, behind, stale}]}`.

## Global Constraints

- Node 22 (`web/console/.nvmrc` is `22`, `engines.node` is `>=22 <23`). Go jobs install no Node.
- Every dependency pinned exactly (no `^` or `~`, `web/console/.npmrc` holds `save-exact=true`),
  `package-lock.json` committed. Pins, all dev dependencies except the four runtime ones:

  | Package | Version | | Package | Version |
  |---|---|---|---|---|
  | react, react-dom (runtime) | 19.2.0 | | vitest | 3.2.4 |
  | react-router (runtime) | 7.9.4 | | jsdom | 27.0.0 |
  | @tanstack/react-query (runtime) | 5.90.2 | | @testing-library/react | 16.3.0 |
  | @types/react, @types/react-dom | 19.2.2 | | @testing-library/user-event | 14.6.1 |
  | vite | 7.1.9 | | @testing-library/jest-dom | 6.9.1 |
  | @vitejs/plugin-react | 5.0.4 | | @playwright/test | 1.56.0 |
  | typescript | 5.9.3 | | eslint, @eslint/js | 9.37.0 |
  | openapi-typescript | 7.9.1 | | typescript-eslint | 8.46.0 |
  | @fontsource-variable/inter | 5.2.8 | | eslint-plugin-react-hooks | 7.0.0 |
  | @fontsource/jetbrains-mono | 5.2.8 | | globals | 16.4.0 |

  TypeScript is 5.9, not the 7.0 of `sdks/server/typescript`: typescript-eslint and
  openapi-typescript need the TypeScript 5 compiler API.
- No component library, no CSS framework, no runtime dependency beyond the four above. Fonts
  (Inter, JetBrains Mono) are bundled; nothing loads from another origin.
- The CSP of `internal/console/console.go` is unchanged: `default-src 'self'; script-src 'self';
  style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri
  'none'; form-action 'none'`. No inline script, no inline style, no `style` prop, no
  `dangerouslySetInnerHTML`.
- `go:embed` path: `internal/console/dist`. `index.html` and every file outside `assets/` are
  served with `Cache-Control: no-cache`; `assets/*` (content hashed) with
  `Cache-Control: public, max-age=31536000, immutable`.
- The page never stores a key or a CSRF token in `localStorage`, `sessionStorage`, a cookie it
  writes or a URL. `localStorage` holds three preferences only: theme, inspector open,
  inspector language.
- Tokens (spec 3.2), light / dark: page `#f6faf7` / `#070a09`; panel `#ffffff` / `#0e1311`,
  raised `#121916`; ink `#0b1510` / `#eef4f0`; muted `#4b5a53` / `#8fa39a`; line
  `rgb(11 21 16 / 8%)` / `rgb(238 244 240 / 8%)`; accent `#17a865` / `#3ddc8e`; on accent
  `#04140c` / `#04140c`. Code, keys, ids and setting names in JetBrains Mono.
- Breakpoints: under 1024 px the navigation folds into a menu; under 768 px the inspector is a
  bottom sheet.
- Safety rules of admin-console.md section 6: confirm every write; type the name for deleting a
  user, deleting a webhook endpoint, revoking a key and forgetting a provider's keys; show a
  one-time secret once, with a copy button, and drop it when the dialog closes; health values
  only behind Preview and the downloads.
- No restart button anywhere (spec non-goal). Secrets are never revealed (write only).
- Copy is English, short, no em dash character. Page titles and buttons as named in the tasks.

## Review Focus

1. A session that ends while Settings holds unsaved edits: the 401 sends the operator to sign in
   and back, and the edits are still pending when they land (Task 14 test
   `pending changes survive a 401 round trip through sign in`).
2. A tenant switch while a page of the old tenant is open: the console lands on Overview and no
   data of the old tenant stays on screen or in the query cache (Task 3 test
   `switching tenant clears every cached query and opens the overview`).
3. A page reload or a second tab rotates the CSRF token (`GET /console/session`): the next
   write from the other tab gets the stale-token 403; the client fetches the session once,
   keeps the new token and retries once, and a second refusal is shown, never looped; any
   other 403 (read only, a missing scope) is shown and never retried (Task 2 tests
   `a stale CSRF token is refreshed once and the write retried once` and
   `other 403 answers are not retried`).
4. An emptied input on a typed setting (`int`, `duration`, `rate`, `enum`, `bool`): never sent
   as `""` (the environment rule treats empty as unset, the store would not); the field says to
   enter a value or revert to default, and Review stays disabled (Task 14 test
   `an emptied typed field blocks Review and is never sent`).
5. A secret setting typed into the form: its value never appears in the review diff, the
   inspector JSON, the Copy as .env text or a 422 message, and the input is empty after Apply
   (Task 14 test `a typed secret is never echoed`).

---

## File map

```
web/console/
  package.json  package-lock.json  .npmrc  .nvmrc
  index.html                    Vite entry, no inline code
  vite.config.ts                build, dev proxy, Vitest
  tsconfig.json                 strict, one config for src, e2e and configs
  eslint.config.js
  playwright.config.ts
  public/favicon.svg
  e2e/  global-setup.ts  global-teardown.ts  console.spec.ts  settings.spec.ts  read-only.spec.ts
  src/
    main.tsx  App.tsx  routes.tsx
    api/      schema.ts (generated)  types.ts  client.ts  paths.ts  download.ts
    session/  SessionProvider.tsx  SignInPage.tsx  RequireSession.tsx  returnPath.ts
    shell/    AppShell.tsx  TopBar.tsx  NavPane.tsx  TenantSwitcher.tsx  ThemeControl.tsx
              PageBoundary.tsx  theme.ts  useMediaQuery.ts
    inspector/ InspectorProvider.tsx  InspectorPane.tsx  snippets.ts  related-settings.json
    palette/  CommandPalette.tsx  search.ts
    ui/       Button.tsx  Pill.tsx  Card.tsx  DataTable.tsx  Tabs.tsx  Pager.tsx  Dialog.tsx
              ConfirmDialog.tsx  SecretOnce.tsx  EmptyState.tsx  ErrorPanel.tsx  Toast.tsx
              CopyButton.tsx  Icon.tsx  Mono.tsx
    lib/      format.ts  classify.ts  pipeline.ts  confirm.ts  prefs.ts  providers.ts
    pages/    overview/  users/  user/  providers/  jobs/  webhooks/  keys/  audit/
              settings/  setup/  NotFoundPage.tsx
    styles/   tokens.css  base.css  shell.css  components.css
    test/     setup.ts  render.tsx  fake-api.ts  fixtures/
internal/console/
  console.go  console_test.go  dist/ (generated, committed)
```

Each page folder holds its page component, its smaller components, an `api.ts` with its
TanStack Query hooks, and its tests next to them.

---

### Task 1: Toolchain, build and Go serving

**Files:**
- Create: `web/console/{package.json,.npmrc,.nvmrc,index.html,vite.config.ts,tsconfig.json,eslint.config.js,public/favicon.svg}`
- Create: `web/console/src/{main.tsx,App.tsx}`, `web/console/src/api/schema.ts` (generated),
  `web/console/src/test/setup.ts`, `web/console/src/App.test.tsx`
- Create: `internal/console/dist/` (generated), `.gitattributes`
- Modify: `internal/console/console.go`, `internal/console/console_test.go`,
  `internal/api/router.go:252`, `Makefile`, `.gitignore`, `.github/workflows/ci.yml`

**Interfaces:**
- Produces: `console.Mount(mux *http.ServeMux)` (the `readOnly` parameter goes: read only is a
  property of the session since plan 2, `d.ConsoleReadOnly` stays for the session code);
  `npm run schema|dev|build|typecheck|lint|test|e2e`; `make console`, `make console-dev`,
  `make console-e2e`; the `console` CI job.

- [ ] **Step 1: Pin and scaffold.** Write `package.json` with the Global Constraints pins and
  scripts: `schema` = `openapi-typescript ../../api/openapi.yaml -o src/api/schema.ts`,
  `dev` = `vite`, `build` = `vite build`, `typecheck` = `tsc --noEmit`, `lint` = `eslint .`,
  `test` = `vitest run`, `e2e` = `playwright test`. Run `npm view <name>@<version> version`
  for each pin; a pin that does not resolve takes the newest patch of the same minor, written
  back into the table of this plan in the same commit. `npm install`, commit the lock file.
- [ ] **Step 2: Configure.** `tsconfig.json`: `strict`, `noUncheckedIndexedAccess`,
  `exactOptionalPropertyTypes`, `noImplicitOverride`, `verbatimModuleSyntax`,
  `resolveJsonModule`, `jsx: react-jsx`, `moduleResolution: bundler`, `target: ES2022`, types
  `vite/client`, `vitest/globals`, `@testing-library/jest-dom`.
  `vite.config.ts`: `base: "/console/"`, `build.outDir: "../../internal/console/dist"`,
  `build.emptyOutDir: true`, `build.assetsInlineLimit: 0` (no `data:` font, which
  `default-src 'self'` would block), `build.modulePreload: { polyfill: false }`,
  `build.sourcemap: false`, output names `assets/[name]-[hash][extname]`; `server.port: 5173`,
  `strictPort: true`, proxy for `/v1`, `/console/session`, `/healthz`, `/readyz` to
  `process.env.OHS_DEV_TARGET ?? "http://localhost:8080"` with `changeOrigin: true` and a
  `proxyReq` hook that sets `Origin` to the target origin (the session middleware refuses a
  foreign `Origin`); `test: { environment: "jsdom", globals: true, setupFiles: ["src/test/setup.ts"], include: ["src/**/*.test.{ts,tsx}"] }`.
  `eslint.config.js`: `@eslint/js` recommended, `typescript-eslint` `strictTypeChecked`,
  `react-hooks` recommended, and these `no-restricted-syntax` / `no-restricted-globals` rules,
  each with the message naming the reason: JSX attribute `style`, JSX attribute
  `dangerouslySetInnerHTML`, member `innerHTML` / `outerHTML` / `insertAdjacentHTML`,
  `eval`, `Function`, `XMLHttpRequest`, `console`; global `fetch` everywhere but
  `src/api/client.ts` and `e2e/`; `localStorage` and `sessionStorage` everywhere but
  `src/lib/prefs.ts`. Ignores `src/api/schema.ts` and `../../internal/console/dist`.
- [ ] **Step 3: Write the failing tests.**
  `src/App.test.tsx`: `it("renders the console root")`: `render(<App />)` shows the text
  `Open Health Sync`. In `internal/console/console_test.go`, replace the old tests with:
  - `TestIndexServedForConsoleRoutes`: for `/console`, `/console/`, `/console/overview`,
    `/console/users/0b6c/connections`, `/console/settings/webhooks/OHS_WEBHOOK_PAUSE_AFTER`:
    200, `Content-Type: text/html; charset=utf-8`, `Cache-Control: no-cache`, body equals
    `dist/index.html`, security headers (keep `assertSecurityHeaders` as it is).
  - `TestHashedAssetsImmutable`: every file under `dist/assets/` matches
    `^assets/[A-Za-z0-9_.-]+-[A-Za-z0-9_-]{8}\.(js|css|woff2)$`, is served 200 with
    `Cache-Control: public, max-age=31536000, immutable` and the type of its extension
    (`text/javascript; charset=utf-8`, `text/css; charset=utf-8`, `font/woff2`).
  - `TestMissingAssetIs404`: `/console/assets/nope-12345678.js` and `/console/assets/` are
    404 with `Cache-Control: no-store`, never the index (a stale page must not run HTML as
    script).
  - `TestTraversalNeverLeavesDist`: `/console/%2e%2e/console.go`, `/console/../console.go`,
    `/console/dist/index.html` never answer a Go file or a 5xx.
  - `TestDistHasNoInlineCode`: the regular expressions of the old `TestPageHasNoInlineCode`
    applied to `dist/index.html`, plus: every `<script>` has `type="module"` and a `src`
    under `/console/assets/`.
  - `TestDistLoadsNothingFromElsewhere`: no `http://`, `https://` or `//` URL in
    `dist/index.html` or any `dist/assets/*.css`.
  - Keep `TestHeadFollowsGet`, `TestOnlyGetIsServed`, `TestSetupPageServed`,
    `TestSetupScriptAvoidsUnsafeSinks` (the old setup page is still served until Task 16).
  In `internal/api` router tests: `TestConsoleSessionNotShadowedBySPA`: with the console on,
  `GET /console/session` without a cookie answers the session route's 401 problem, not
  `index.html`.
- [ ] **Step 4: Run them to see them fail.**
  Run: `go test ./internal/console/ ./internal/api/ -run 'Index|Hashed|Missing|Traversal|Dist|SessionNotShadowed'`
  Expected: FAIL (no `dist`, old handler).
- [ ] **Step 5: Build the page and the handler.** `index.html`: `lang="en"`, charset,
  viewport, `<meta name="referrer" content="no-referrer">`, title `Open Health Sync console`,
  icon `/console/favicon.svg`, `<div id="root">`, one `<script type="module" src="/src/main.tsx">`.
  `main.tsx` creates the root; `App.tsx` renders a placeholder heading `Open Health Sync`
  (replaced in Task 2). Run `npm run schema && npm run build`.
  `console.go`: `//go:embed all:dist` plus the two old setup files; `Mount(mux)` registers
  `GET /console` and `GET /console/{path...}`; `serve(w http.ResponseWriter, name string)`
  rejects `!fs.ValidPath`, serves the old setup page for `setup` and `setup.mjs` (temporary,
  removed in Task 16), a dist file when one exists (not a directory) with the cache rule of
  the Global Constraints and a type from an explicit extension map (`.js`, `.css`, `.woff2`,
  `.svg`, `.html`; any other extension is 404), 404 for any other `assets/` path, else
  `index.html`. Keep `CSP` and the four security headers. Update the package comment.
  `router.go:252`: `console.Mount(mux)`.
- [ ] **Step 6: Make, ignore, attributes, CI.**
  `Makefile`: `console` = `cd web/console && npm ci && npm run schema && npm run build`;
  `console-dev` = `cd web/console && npm run dev`; `console-e2e` =
  `cd web/console && npx playwright test` (needs `OHS_E2E_DATABASE_URL`, see Task 5); add them
  to `.PHONY` with a comment each in the file's style.
  `.gitignore`: `web/console/node_modules/`, `web/console/test-results/`,
  `web/console/playwright-report/`.
  `.gitattributes`: `internal/console/dist/** linguist-generated=true` and
  `web/console/src/api/schema.ts linguist-generated=true`.
  `ci.yml`, new job `console` (`runs-on: ubuntu-latest`, `timeout-minutes: 20`,
  `defaults.run.working-directory: web/console`): checkout and setup-node 22 at the SHAs the
  file already pins, `cache: npm`, `cache-dependency-path: web/console/package-lock.json`;
  steps `npm ci`, `npm run schema`, `npm run lint`, `npm run typecheck`, `npm test`,
  `npm run build`, then from the repository root:
  `git diff --exit-code -- internal/console/dist web/console/src/api/schema.ts` and
  `test -z "$(git status --porcelain -- internal/console/dist web/console/src/api/schema.ts)"`
  (the second catches a new hashed file, which `git diff` does not see). Playwright joins the
  job in Task 5.
- [ ] **Step 7: Run everything.**
  Run: `cd web/console && npm run lint && npm run typecheck && npm test && npm run build && cd ../.. && go test ./internal/console/ ./internal/api/ && go build ./cmd/ohs`
  Expected: PASS, and `git status` shows no change after the second build.
- [ ] **Step 8: Commit.** `git add web/console internal/console internal/api/router.go Makefile .gitignore .gitattributes .github/workflows/ci.yml`,
  `git commit -m "feat(console): React console toolchain, embedded dist and SPA fallback"`.

---

### Task 2: API client and sign in

**Files:**
- Create: `src/api/{types.ts,client.ts,paths.ts,download.ts}`, `src/session/{SessionProvider.tsx,SignInPage.tsx,RequireSession.tsx,returnPath.ts}`,
  `src/routes.tsx`, `src/test/{render.tsx,fake-api.ts,fixtures/session.json,fixtures/session-read-only.json}`
- Test: `src/api/client.test.ts`, `src/api/paths.test.ts`, `src/session/session.test.tsx`, `src/session/returnPath.test.ts`
- Modify: `src/App.tsx`

**Interfaces:**
- Consumes: `paths`, `components` from `src/api/schema.ts`.
- Produces:
  - `types.ts`: `type JsonOf<P extends keyof paths, M extends keyof paths[P], S extends number>`
    (the `application/json` body of that answer) and the aliases later tasks use:
    `ConsoleSession`, `ConsoleTenant`, `Problem`, `Tenant`, `AdminTenant`, `User`,
    `Pipeline`, `Provider`, `AdminProvider`, `Webhook`, `Delivery`, `APIKey`, `CreatedAPIKey`,
    `KeyScope`, `AuditEvent`, and for plan 1's schemas `Setting` (`AdminSetting`),
    `SettingsList` (`AdminSettings`), `SettingsProblem` (`AdminSettingsProblem`),
    `ProcessHeartbeat` (`AdminProcess`), `ProcessList` (`AdminProcesses`).
  - `client.ts`: `type Method = "GET" | "HEAD" | "POST" | "PUT" | "PATCH" | "DELETE"`;
    `interface RequestOptions { body?: unknown; query?: Query; headers?: Record<string, string>; as?: "json" | "text" | "blob"; anonymous?: boolean }`;
    `interface ApiResponse<T> { data: T; status: number; headers: Headers }`;
    `interface ApiClient { request<T>(method: Method, path: string, opts?: RequestOptions): Promise<ApiResponse<T>>; get<T>(path: string, query?: Query): Promise<T>; send<T>(method: Method, path: string, body?: unknown): Promise<T> }`;
    `createClient(deps: { fetch: typeof fetch; csrf: () => string | null; refreshCsrf: () => Promise<string | null>; readOnly: () => boolean; onUnauthorized: () => void }): ApiClient`;
    `class ApiError extends Error { status: number; detail: string; requestId: string | null; body: unknown }`;
    `class ReadOnlyError extends Error`; `const SESSION_PATH = "/console/session"`;
    `const CSRF_HEADER = "X-OHS-CSRF"`;
    `const CSRF_REFUSED = "this request needs the X-OHS-CSRF token of the console session; reload the page"`.
  - `paths.ts`: `type Query = Record<string, string | number | boolean | null | undefined>`;
    `apiPath(segments: readonly string[], query?: Query): string` (port of `lib.mjs`);
    `filenameFrom(header: string | null): string | null` (port).
  - `download.ts`: `downloadFile(api: ApiClient, path: string, fallbackName: string): Promise<void>`.
  - `SessionProvider` and `useSession(): { session: ConsoleSession | null; status: "loading" | "signed-in" | "signed-out"; signIn(key: string): Promise<void>; signOut(): Promise<void>; switchTenant(tenantId: string): Promise<void> }`;
    `useApi(): ApiClient`; `useCanWrite(): boolean` (signed in and not `read_only`);
    `useIsAdmin(): boolean` (`session.tenant.admin`).
  - `safeReturnPath(from: unknown): string`.
  - Test helpers: `renderWithApp(ui: ReactNode, opts?: { session?: ConsoleSession | null; route?: string; api?: FakeApi })`;
    `fakeApi(routes: Record<string, FakeHandler>): FakeApi` keyed `"METHOD /path"`, recording
    every call (method, path, headers, body).

- [ ] **Step 1: Write the failing tests.**
  `client.test.ts`:
  - `sends cookies on this origin and never an Authorization header`: init has
    `credentials: "same-origin"`, `cache: "no-store"`, `redirect: "error"`,
    `referrerPolicy: "no-referrer"`; headers lack `Authorization`.
  - `adds X-OHS-CSRF to every method but GET and HEAD`: POST, PUT, PATCH, DELETE carry
    `X-OHS-CSRF: <token>`; GET and HEAD do not.
  - `refuses paths off this origin before any request`: `//evil.example/x`, `https://x/y`,
    `\\x`, `v1/users` throw `ReadOnlyError`; fetch not called.
  - `read only refuses writes but lets the session in, out and across tenants`: with
    `readOnly() === true`, `POST /v1/users` throws `ReadOnlyError` unsent;
    `POST /console/session`, `DELETE /console/session` and `PUT /console/session/tenant` are sent.
  - `turns a problem answer into an ApiError`: 422 `{"title":"Unprocessable","detail":"bad","request_id":"r1"}`
    gives `status 422`, `detail "bad"`, `requestId "r1"`, `body` the parsed object.
  - `calls onUnauthorized on a 401 except for the sign in itself`: 401 on `GET /v1/users`
    calls it once; 401 on `POST /console/session` does not.
  - `a stale CSRF token is refreshed once and the write retried once`: answers 403
    `{detail: CSRF_REFUSED}`, then `refreshCsrf` gives `"new"`, then 201: two fetches of the
    write, the second with `X-OHS-CSRF: new`; answers 403 `CSRF_REFUSED` twice: `ApiError(403)`
    after exactly two fetches.
  - `other 403 answers are not retried`: 403 with the read only detail: one fetch,
    `refreshCsrf` not called.
  - `anonymous requests send no cookie and no CSRF`: `credentials: "omit"`, no `X-OHS-CSRF`.
  - `reads 204 as null, text as text, blob with its file name`.
  `paths.test.ts`: the old `apiPath escapes ids and drops empty query values` and `filenameFrom`
  cases, verbatim.
  `returnPath.test.ts`: `/console/users/u1` kept; `/console/sign-in`, `/v1/users`,
  `//evil.example`, `https://evil.example/console/x`, `42`, `undefined` give `/console/overview`.
  `session.test.tsx`:
  - `signs in, holds no key, and lands where it was sent from`: start at `/console/users`
    signed out; the page is `/console/sign-in` with heading `Sign in`; type a key matching
    `^ohs_sk_[A-Za-z0-9_-]{16,}$`, press `Sign in`; fake API saw `POST /console/session` with
    body `{"key": ...}`; the route is `/console/users`; `localStorage`, `sessionStorage` and
    `document.body.textContent` do not contain the key.
  - `refuses a malformed key before any request`: `abc` shows `That is not a tenant API key.`;
    no call.
  - `shows the API message on a refused key`: 401 with detail `invalid API key` shows it.
  - `shows why sign in over plain http is refused`: a 403 on `POST /console/session` shows
    its `detail` verbatim and does not call `onUnauthorized`.
  - `links to setup when the instance has no tenant`: `GET /v1/setup` `{"needed": true}` shows
    a link `First run: create the first tenant` to `/console/setup`.
  - `a reload restores the session from the cookie`: `GET /console/session` 200 renders the
    signed-in route without the form.
  - `sign out deletes the session and returns to sign in`: `DELETE /console/session` sent,
    query cache empty, route `/console/sign-in`.
  - `a 401 mid-visit sends to sign in and back`: on `/console/users/u1` a query answers 401;
    route becomes `/console/sign-in`; after signing in, `/console/users/u1`.
- [ ] **Step 2: Run them to see them fail.**
  Run: `cd web/console && npx vitest run src/api src/session`
  Expected: FAIL, modules missing.
- [ ] **Step 3: Implement.** `createClient` per the Interfaces; the 401 rule excludes only
  `POST SESSION_PATH` (a 403 there is shown, never retried); `refreshCsrf` is `SessionProvider` refetching `GET /console/session` and returning its
  `csrf_token`; the retry happens only for a 403 whose `detail` equals `CSRF_REFUSED`, once.
  `SessionProvider` keeps the session in the query `["session"]` (a 401 there means signed
  out, not an error), builds the client with `csrf: () => session?.csrf_token ?? null`, and
  exposes the hooks. `signIn` posts, stores the answer as `["session"]`; the form clears its input state
  in a `finally`. `signOut` deletes, then `queryClient.clear()`. `RequireSession` redirects
  signed-out visits to `/console/sign-in` with `state: { from: location.pathname }`.
  `SignInPage`: heading `Sign in`, one `<input type="password" autocomplete="off" spellcheck={false}>`
  labelled `Tenant API key`, button `Sign in`, note `The key is checked once. The console then
  holds a session, never the key.` `routes.tsx` exports `router = createBrowserRouter(routes, { basename: "/console" })`;
  `App.tsx` renders `QueryClientProvider` (retry 1, no retry on 4xx), `SessionProvider`,
  `RouterProvider`.
- [ ] **Step 4: Run them to see them pass.**
  Run: `npx vitest run src/api src/session && npm run typecheck && npm run lint`
  Expected: PASS.
- [ ] **Step 5: Build and commit.** `npm run build`, `go test ./internal/console/`,
  `git commit -m "feat(console): session client and sign in"` with `web/console` and
  `internal/console/dist`.

---

### Task 3: Shell, theme and tenant switch

**Files:**
- Create: `src/shell/{AppShell.tsx,TopBar.tsx,NavPane.tsx,TenantSwitcher.tsx,ThemeControl.tsx,PageBoundary.tsx,theme.ts,useMediaQuery.ts}`,
  `src/lib/prefs.ts`, `src/ui/{Button.tsx,Pill.tsx,Card.tsx,Icon.tsx,Mono.tsx,Toast.tsx,ErrorPanel.tsx,EmptyState.tsx,Dialog.tsx}`,
  `src/styles/{tokens.css,base.css,shell.css,components.css}`, `src/pages/NotFoundPage.tsx`
- Test: `src/shell/shell.test.tsx`, `src/shell/theme.test.ts`, `src/lib/prefs.test.ts`
- Modify: `src/routes.tsx`, `src/main.tsx`

**Interfaces:**
- Consumes: `useSession`, `useApi`, `useIsAdmin`, `useCanWrite` (Task 2).
- Produces: `AppShell` (route element wrapping every signed-in page in `PageBoundary`);
  `type PrefName = "theme" | "inspector" | "inspector-lang"`;
  `readPref(name: PrefName): string | null`, `writePref(name: PrefName, value: string): void`
  (keys `ohs.console.<name>`, every access in `try`/`catch`);
  `type ThemeChoice = "light" | "dark" | "auto"`; `applyTheme(choice: ThemeChoice, root?: HTMLElement): void`;
  `useTheme(): [ThemeChoice, (c: ThemeChoice) => void]`; `NAV_GROUPS` (below); the `ui/`
  components with props named in their files: `Button({ kind?: "primary" | "ghost" | "danger", size?: "sm" })`,
  `Pill({ tone: "ok" | "warn" | "bad" | "off" })`, `ErrorPanel({ error: unknown })`,
  `Dialog({ title, open, onClose, children, footer })` over a native `<dialog>`;
  `useToast(): (message: string, tone?: "ok" | "bad") => void`.

- [ ] **Step 1: Write the failing tests.**
  `prefs.test.ts`: `stores only the three preferences under ohs.console.`; `survives a storage
  that throws` (a `localStorage` stub throwing on every call: `readPref` returns null,
  `writePref` does not throw).
  `theme.test.ts`: `auto removes data-theme`; `light and dark set it`; `the choice persists`.
  `shell.test.tsx`:
  - `shows the tenant, the pages and sign out`: nav shows the tenant name, groups
    `Workspace` (Overview, Users, Providers, Jobs), `Developers` (Webhooks, API keys,
    Audit log), `Instance` (Settings), a `Light`, `Dark`, `Auto` control and `Sign out`.
  - `hides Settings from a tenant that is not an admin`.
  - `marks a read only session`: badge `Read only` in the nav.
  - `lists tenants for an admin and switches`: the switcher fetches `GET /v1/admin/tenants`,
    choosing `Clinic B` sends `PUT /console/session/tenant` with `{"tenant_id": "<id>"}`.
  - `switching tenant clears every cached query and opens the overview`: seed the cache with
    `["users"]` data, switch; the cache holds only `["session"]`, the route is
    `/console/overview`, the nav shows the acting tenant name.
  - `has no switcher for a tenant that is not an admin`.
  - `a read only admin session can still switch tenant`.
  - `the PUT answer keeps the current CSRF token`: after the switch the session holds the
    acting tenant of the `PUT` answer and the `csrf_token` it had before (the `PUT` answer has
    none).
  - `an exception in a page shows an error panel for that page only`: a route element that
    throws renders `This page failed` with a `Reload page` button; the nav still renders;
    navigating away clears the panel.
  - `folds the navigation into a menu under 1024 px`: with `matchMedia("(max-width: 1023px)")`
    true, a `Menu` button toggles the nav; Escape closes it.
  - `unknown console path shows Not found inside the shell`.
- [ ] **Step 2: Run them to see them fail.** Run: `npx vitest run src/shell src/lib/prefs.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement.** Layout from mockup C: `TopBar` (menu button under 1024 px, brand
  mark and `Open Health Sync`, `Search or jump to` button with `⌘K`, inspector toggle);
  three-column grid `NavPane` / content / `InspectorPane` slot (filled in Task 4). `NavPane`:
  tenant block (avatar initial, name, `admin` or `tenant` role, acting tenant when it differs
  from the session tenant: `Acting as <acting_tenant.name>`), `TenantSwitcher` (admin only,
  lists `GET /v1/admin/tenants`, on choice calls `switchTenant`, which PUTs, stores the answer
  merged with the current `csrf_token`, `queryClient.removeQueries({ predicate: q => q.queryKey[0] !== "session" })`,
  navigates to `/console/overview`), the groups with `NavLink`s, `ThemeControl` (segmented
  `Light` `Dark` `Auto`), `Sign out`. `PageBoundary` is a class error boundary keyed by
  `location.pathname`. `tokens.css` holds the Global Constraints tokens on `:root`, the dark
  set under `@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) }` and again
  under `:root[data-theme="dark"]`; `base.css` imports the two fontsource packages and sets
  `body` background `var(--page)`. `main.tsx` calls `applyTheme(readPref("theme") ?? "auto")`
  before the first render. Icons are React SVG components drawn from the mockup's symbols.
- [ ] **Step 4: Run them to see them pass.** Run: `npx vitest run && npm run typecheck && npm run lint`. Expected: PASS.
- [ ] **Step 5: Build and commit.** `npm run build`,
  `git commit -m "feat(console): shell, theme and tenant switch"`.

---

### Task 4: Inspector

**Files:**
- Create: `src/inspector/{InspectorProvider.tsx,InspectorPane.tsx,snippets.ts,related-settings.json}`, `src/ui/CopyButton.tsx`
- Test: `src/inspector/snippets.test.ts`, `src/inspector/inspector.test.tsx`,
  `internal/config/console_related_test.go`
- Modify: `src/shell/AppShell.tsx`

**Interfaces:**
- Consumes: `Method`, `ApiError` (Task 2); `readPref`/`writePref` (Task 3); the settings
  registry of plan 1 in `internal/config/settings.go`.
- Produces:
  `interface InspectorRequest { method: Method; path: string; query?: Record<string, string>; body?: unknown }`;
  `type InspectorKind = "pipeline" | "users" | "user" | "providers" | "provider" | "jobs" | "webhooks" | "webhook" | "keys" | "audit" | "audit-event" | "settings" | "setting"`;
  `interface InspectorEntry { kind: InspectorKind; title: string; id?: string; request: InspectorRequest; response: unknown; error?: ApiError }`;
  `useInspect(entry: InspectorEntry | null): void` (set while mounted, cleared on unmount);
  `type SnippetLang = "curl" | "ts" | "python"`;
  `snippet(req: InspectorRequest, lang: SnippetLang, origin: string): string`;
  `relatedSettings(kind: InspectorKind): readonly string[]` from `related-settings.json`.

- [ ] **Step 1: Write the failing tests.**
  `snippets.test.ts`, origin `https://ohs.example.com`:
  - `curl GET`: `snippet({method:"GET", path:"/v1/users", query:{limit:"50"}}, "curl", o)` equals
    ```
    curl "https://ohs.example.com/v1/users?limit=50" \
      -H "Authorization: Bearer $OHS_API_KEY"
    ```
  - `curl POST with a body`: starts `curl -X POST "https://ohs.example.com/v1/webhooks" \`,
    has `-H "Content-Type: application/json"` and `-d '` followed by the two-space JSON; a `'`
    inside the body is written `'\''`.
  - `TypeScript`: contains ``Authorization: `Bearer ${process.env.OHS_API_KEY}` ``,
    `method: "POST"`, `body: JSON.stringify(`, ends `const data = await res.json();`.
  - `Python`: `import os, requests`, `requests.get(`, `params={"limit": "50"}`,
    `f"Bearer {os.environ['OHS_API_KEY']}"`, a body as `json=` with `True`, `False`, `None`,
    ends `data = res.json()`.
  - `never contains a key or the CSRF token`: no `ohs_sk_` and no `X-OHS-CSRF` in any output.
  `inspector.test.tsx`:
  - `shows the call, the JSON and the related settings`: an entry of kind `webhooks` renders
    method `GET`, path `/v1/webhooks`, tabs `cURL`, `TypeScript`, `Python`, the JSON answer
    pretty printed as text, and under `Configuration` the names of
    `relatedSettings("webhooks")`, each a link to `/console/settings/webhooks/<name>` with its
    effective value and source for an admin session.
  - `names related settings without values for a tenant that is not an admin`: names shown,
    no link, note `Settings need an instance admin session.`
  - `shows the raw answer of a failed call`: an entry with `error` shows its status and body.
  - `remembers open or closed and the language per browser`.
  - `opens as a bottom sheet under 768 px`: an `Inspector` button opens it; Escape closes it.
  `console_related_test.go`: `TestConsoleRelatedSettingsAreRegistered` reads
  `../../web/console/src/inspector/related-settings.json` (an object of kind to names) and
  fails for a name that is not a registry entry.
- [ ] **Step 2: Run them to see them fail.**
  Run: `npx vitest run src/inspector && go test ./internal/config/ -run ConsoleRelated`. Expected: FAIL.
- [ ] **Step 3: Implement.** `related-settings.json`: `pipeline` and `overview` content:
  `["OHS_RETENTION_DAYS","OHS_CONSENT_REQUIRED"]`; `users`, `user`:
  `["OHS_CONSENT_REQUIRED","OHS_RETENTION_DAYS","OHS_BACKFILL_DAYS"]`; `providers`, `provider`:
  `["OHS_PUBLIC_URL","OHS_RATE_LIMIT_OAUTH","OHS_RATE_LIMIT_INBOUND","OHS_RATE_LIMIT_INBOUND_PROVIDER"]`;
  `jobs`: `["OHS_BACKFILL_DAYS"]`; `webhooks`, `webhook`:
  `["OHS_WEBHOOK_PAUSE_AFTER","OHS_WEBHOOK_ROTATION_GRACE_HOURS"]`; `keys`:
  `["OHS_RATE_LIMIT_API","OHS_RATE_LIMIT_AUTH_FAILURES"]`; `audit`, `audit-event`:
  `["OHS_AUDIT_RETENTION_DAYS"]`; `settings`, `setting`: `[]`. Values come from the settings
  query of Task 14's `useSettings()` (defined here as `useSettingsQuery()` in
  `src/pages/settings/api.ts`, `enabled` only for admins). JSON is rendered with
  `JSON.stringify(value, null, 2)` as text in a `<pre>`; no highlighting library. Header:
  `INSPECTOR` and the kind, title, id in mono.
- [ ] **Step 4: Run them to see them pass.** Run the commands of Step 2. Expected: PASS.
- [ ] **Step 5: Build and commit.** `git commit -m "feat(console): inspector with request snippets and related settings"`.

---

### Task 5: Overview, shared helpers and the end to end harness

**Files:**
- Create: `src/lib/{format.ts,classify.ts,pipeline.ts,confirm.ts,providers.ts}`, `src/pages/overview/{OverviewPage.tsx,PipelineBand.tsx,StageCard.tsx,GettingStarted.tsx,AttentionList.tsx,api.ts}`,
  `e2e/{global-setup.ts,global-teardown.ts,console.spec.ts}`, `playwright.config.ts`
- Test: `src/lib/{format,classify,pipeline,confirm,providers}.test.ts`, `src/pages/overview/overview.test.tsx`
- Modify: `.github/workflows/ci.yml` (job `console`), `src/routes.tsx`

**Interfaces:**
- Produces (ports of `lib.mjs`, same behavior, typed): `format.ts`: `formatAgo`, `formatTime`,
  `absTime`, `relTime`, `formatWindow`, `splitFigure`, `formatCount`, `formatBytes`,
  `humanize`, `initials`, `providerLabel`; `classify.ts`: `classifyFreshness`,
  `classifyConnection`, `classifyWebhook`, `webhookLabel`, `classifyDelivery`, `classifyUser`,
  `classifyProvider`, `providerStatusLabel`, `classifyRun`, `isCloud`, each returning
  `Tone = "ok" | "warn" | "bad" | "off"`; `pipeline.ts`: `STAGE_STATES`,
  `pipelineStages(p: Pipeline): Stage[]`, `attention(p: Pipeline): AttentionItem[]`, and new
  `gettingStarted(p: Pipeline): ChecklistItem[]`; `confirm.ts`: `confirmMatches`;
  `providers.ts`: `keysPatch`, `syncSchedule`. Health chart helpers are not ported (spec
  non-goal). `interface ChecklistItem { id: "provider" | "user" | "connection" | "data" | "webhook"; title: string; done: boolean; to: string }`.
  E2E: `process.env.OHS_E2E_URL`, `OHS_E2E_RO_URL`, `OHS_E2E_ADMIN_KEY`,
  `OHS_E2E_OTHER_TENANT`, set by `global-setup.ts`.

- [ ] **Step 1: Write the failing tests.** Port every test of `internal/console/app.test.mjs`
  for the helpers above to Vitest with the same names and the same assertions. New:
  - `gettingStarted`: on an empty pipeline every item is not done with `to` values
    `/console/providers`, `/console/users`, `/console/users`, `/console/users`,
    `/console/webhooks`; `provider` done when a source is `enabled` and cloud, `user` when
    `users.active > 0`, `connection` when any source has a connection, `data` when
    `sync.succeeded_24h > 0 || storage.raw_objects_24h > 0`, `webhook` when
    `delivery.endpoints > 0`.
  `overview.test.tsx`:
  - `opens on the black Pipeline, live band with five stages`: heading `Overview`, a band
    labelled `Pipeline, live`, stage cards `Sources`, `Sync`, `Processing`, `Storage`,
    `Delivery` with their counts from a fixture `GET /v1/pipeline`.
  - `lists what needs attention, each linking to the page that fixes it`.
  - `shows the getting started checklist until every item is done`.
  - `shows healthz and readyz`.
  - `puts the pipeline call in the inspector`: kind `pipeline`, `GET /v1/pipeline`.
  - `shows the API status and message when the pipeline call fails`.
  `e2e/console.spec.ts`: `signs in, opens the overview and signs out`: visit
  `/console/users`, land on sign in, sign in with `OHS_E2E_ADMIN_KEY`, land on
  `/console/users`, open Overview, see `Pipeline, live`, sign out, land on sign in, then
  `/console/overview` sends back to sign in.
- [ ] **Step 2: Run them to see them fail.** Run: `npx vitest run src/lib src/pages/overview`. Expected: FAIL.
- [ ] **Step 3: Implement the page.** Copy from mockup C: subtitle `Tenant <name>`; the band
  is black in both themes; `Refresh` button; `AttentionList` card `Needs attention`;
  `GettingStarted` card `Get started` with `n of 5 done`. Pipeline refetches every 30 s.
- [ ] **Step 4: Implement the harness.** `playwright.config.ts`: one project, Chromium,
  headless, `globalSetup`, `globalTeardown`, `use.baseURL` from `OHS_E2E_URL`,
  `expect.timeout` 45 s, reporter `list` plus `html` (open `never`), `retries` 1 in CI.
  `global-setup.ts`: fails unless `OHS_E2E_DATABASE_URL` is set; `go build -o <tmp>/ohs ./cmd/ohs`
  from the repository root; `ohs migrate up`; refuses a database that already has a tenant
  (`ohs admin tenant list` prints a row) with `OHS_E2E_DATABASE_URL must point at an empty
  database`; `ohs admin tenant create --name e2e-admin` (first tenant, so admin) and
  `--name e2e-other`, reading the id from `(id <uuid>` and the key from the line matching
  `^ohs_sk_\S+$`; `ohs seed --demo --tenant <admin id>`; starts `ohs serve` twice with
  `OHS_DATABASE_URL`, a fresh random 32-byte base64 `OHS_ENCRYPTION_KEY`,
  `OHS_RAW_STORE=file://<tmp>/raw`, `OHS_METRICS=false`: one on `127.0.0.1:18090` with
  `OHS_CONSOLE=true`, `OHS_PUBLIC_URL=http://127.0.0.1:18090`, `OHS_ROLES=api,collect,etl`;
  one on `127.0.0.1:18091` with `OHS_CONSOLE=read-only`, `OHS_PUBLIC_URL=http://127.0.0.1:18091`,
  `OHS_ROLES=api`; waits for `/readyz`; sets the `OHS_E2E_*` variables. `global-teardown.ts`
  stops both processes and removes the temporary directory.
  `ci.yml` job `console`: add a `services.postgres` with the image and digest of
  `deploy/docker-compose.yml`, `POSTGRES_USER/PASSWORD/DB` `ohs`, port 5432, a `pg_isready`
  health check; add setup-go 1.26 (pinned SHA of the file); after the diff check:
  `npx playwright install --with-deps chromium`, then `npm run e2e` with
  `OHS_E2E_DATABASE_URL: postgres://ohs:ohs@localhost:5432/ohs?sslmode=disable`; on failure
  upload `web/console/playwright-report` with the `upload-artifact` SHA the repository pins.
- [ ] **Step 5: Run everything.**
  Run: `npx vitest run && npm run build && OHS_E2E_DATABASE_URL=<scratch db> npm run e2e`
  Expected: PASS.
- [ ] **Step 6: Commit.** `git commit -m "feat(console): overview page and the Playwright harness"`.

---

### Task 6: List filters the pages need (Go)

Spec 3.4 asks for Users searched by external id and Audit filtered by actor; neither route has
the parameter today.

**Files:**
- Modify: `internal/tenant/store.go:484` (`ListUsers`), the users and audit handlers in
  `internal/api`, the audit store query, `api/openapi.yaml` (`listUsers`, `GET /v1/audit`),
  regenerated SDKs (`make sdk`), `docs/api.md`
- Test: `internal/tenant/store_integration_test.go`, `internal/api/api_integration_test.go`,
  the audit integration test next to its store

**Interfaces:**
- Produces: `GET /v1/users?external_ref=<exact>` (an exact match in the tenant, the same page
  shape, at most one row); `GET /v1/audit?actor_type=<api_key|user_token|admin|system|share_link>&actor_id=<id>`
  (both optional, `actor_id` needs `actor_type`, 400 otherwise).
  `func (s *Store) ListUsers(ctx context.Context, tenantID uuid.UUID, f UserFilter, cursor string, limit int) ([]User, string, error)`
  with `type UserFilter struct { ExternalRef string }`.

- [ ] **Step 1: Write the failing tests.** `TestListUsersByExternalRef`: two users `a`, `b`;
  `external_ref=b` gives only `b`; `external_ref=zz` gives an empty page with
  `next_cursor: null`; another tenant's `b` never shows. `TestAuditFilterByActor`: events of
  two keys; `actor_type=api_key&actor_id=<k1>` gives only k1's; `actor_id` alone is 400 naming
  `actor_type`. The OpenAPI route test covers both parameters.
- [ ] **Step 2: Run them to see them fail.** Run: `go test -tags integration ./internal/tenant/ ./internal/api/ -run 'ExternalRef|FilterByActor'`. Expected: FAIL.
- [ ] **Step 3: Implement** the filters, the parameters in `api/openapi.yaml`, `make sdk`,
  one line each in `docs/api.md`; `npm run schema` in `web/console`.
- [ ] **Step 4: Run them to see them pass.** Run the Step 2 command, `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat(api): filter users by external_ref and audit events by actor"`.

---

### Task 7: Users

**Files:**
- Create: `src/pages/users/{UsersPage.tsx,UsersTable.tsx,UserSearch.tsx,CreateUserDialog.tsx,api.ts}`,
  `src/ui/{DataTable.tsx,Pager.tsx,ConfirmDialog.tsx}`, `e2e/read-only.spec.ts`
- Test: `src/pages/users/users.test.tsx`, `src/ui/ui.test.tsx`

**Interfaces:**
- Consumes: Task 6 `external_ref`; `useInspect`; `useCanWrite`.
- Produces: `DataTable<T>({ columns: Column<T>[]; rows: readonly T[]; rowKey: (r: T) => string; onSelect?: (r: T) => void; selectedKey?: string; to?: (r: T) => string; empty: ReactNode })`
  (rows reachable by keyboard: arrow keys move the selection, Enter opens `to`);
  `Pager({ cursor stack })` with `usePager(): { cursor: string | undefined; next(c: string | null): void; prev(): void; page: number }`;
  `ConfirmDialog({ title; body; confirmLabel; danger?: boolean; typed?: string; onConfirm: () => Promise<unknown> })`.

- [ ] **Step 1: Write the failing tests.**
  `ui.test.tsx`: `ConfirmDialog with typed keeps the button disabled until the text matches`
  (uses `confirmMatches`); `ConfirmDialog shows the API error and stays open`;
  `DataTable moves the selection with the arrow keys and opens with Enter`.
  `users.test.tsx`:
  - `lists 50 users per page with a pager`: `GET /v1/users?limit=50`; `Next` sends the
    `next_cursor`; `Previous` returns.
  - `searches by external id`: typing `demo-user` and Enter sends
    `GET /v1/users?external_ref=demo-user&limit=50`; no match shows `No user with external id demo-user`.
  - `creates a user after a confirmation`: `New user`, external id `p-17`, `Create user`
    sends `POST /v1/users` `{"external_ref":"p-17"}` and opens `/console/users/<id>`; an empty
    external id sends `{}`.
  - `a read only session sees no New user button`.
  - `selecting a row inspects GET /v1/users/{id}`.
  `e2e/read-only.spec.ts`: `a read only session cannot write`: sign in on `OHS_E2E_RO_URL`,
  Users has no `New user`; `page.evaluate` reads the CSRF token from
  `GET /console/session` and posts `/v1/users` with it; the answer is 403.
- [ ] **Step 2: Run them to see them fail.** Run: `npx vitest run src/pages/users src/ui`. Expected: FAIL.
- [ ] **Step 3: Implement.** Page title `Users`, subtitle `People whose devices and accounts
  feed this tenant.`; columns User (external id, id in mono), Status (pill), Created. Query key
  `["users", { cursor, externalRef }]`.
- [ ] **Step 4: Run them to see them pass**, including `npm run e2e`. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat(console): users page"`.

---

### Task 8: User

**Files:**
- Create: `src/pages/user/{UserPage.tsx,UserHeader.tsx,DataTab.tsx,CoveragePanel.tsx,PreviewPanel.tsx,DownloadsPanel.tsx,ConnectionsTab.tsx,ConsentsTab.tsx,SyncRunsTab.tsx,SharesTab.tsx,ConnectLinkDialog.tsx,api.ts}`, `src/ui/Tabs.tsx`
- Test: `src/pages/user/user.test.tsx`

**Interfaces:**
- Consumes: `downloadFile`, `apiPath`, `ConfirmDialog`, `classifyConnection`, `classifyFreshness`, `classifyRun`.
- Produces: routes `/console/users/:userId` and `/console/users/:userId/:tab` with
  `type UserTab = "data" | "connections" | "consents" | "sync-runs" | "shares"` (default `data`).

- [ ] **Step 1: Write the failing tests.**
  - `shows the tabs Data, Connections, Consents, Sync runs, Shares and keeps the tab in the address`.
  - `Data loads coverage only; values wait for Preview`: on open, `GET .../coverage` only; `Preview`
    on `Daily summaries`, `Sleep`, `Workouts` sends `GET .../daily?limit=10` (and the others)
    and shows the rows.
  - `downloads build the file in the page`: `FHIR R4 Bundle` fetches
    `.../fhir/Patient/<id>/$everything?limit=1000` as a blob and saves `fhir-<id>.json`;
    `Open mHealth` `omh-<id>.json`; `Full archive (zip)` `archive-<id>.zip`; one CSV per
    dataset `samples`, `category-samples`, `sleep`, `workouts`, `nutrition`, `daily`, `ecg`
    named `<dataset>-<id>.csv`; no URL with a key or token is ever created (an object URL only).
  - `Connection link creates a connect session and shows the link once with Copy`:
    `POST .../connect-sessions` `{}`.
  - `Sync now on a connection`: `POST .../connections/<provider>/sync` after a confirmation.
  - `Disconnect asks, and can delete the data`: `DELETE .../connections/<provider>` and with the
    `Also delete its data` box `?delete_data=true`.
  - `Delete user needs the external id typed`: `DELETE /v1/users/<id>` sent only after typing
    `external_ref` (or the id when there is none); then `/console/users`.
  - `Consents shows current consents and the history, read only`: `GET .../consents` and
    `.../consents/history`.
  - `Sync runs lists the user's runs`: `GET .../sync-runs?limit=50`.
  - `Shares lists the share links, read only`: `GET .../shares`; empty state `No shares yet`.
  - `a read only session sees no action buttons`.
  - `404 shows Not found with a link back to Users`.
- [ ] **Step 2: Run them to see them fail.** Run: `npx vitest run src/pages/user`. Expected: FAIL.
- [ ] **Step 3: Implement.** Header from mockup C: initials avatar, external id, status pill,
  id in mono, created; header actions `Connection link`, `Delete user`. Connections columns:
  provider, status, freshness, last sync, actions. Inspector entry per tab: the tab's GET.
- [ ] **Step 4: Run them to see them pass.** Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat(console): user page with data, connections, consents, sync runs and shares"`.

---

### Task 9: Providers

**Files:**
- Create: `src/pages/providers/{ProvidersPage.tsx,ProvidersTable.tsx,ProviderPage.tsx,ProviderKeysForm.tsx,ProviderSwitch.tsx,TenantProviderSwitch.tsx,ForgetProviderCard.tsx,RegisterUrls.tsx,api.ts}`
- Test: `src/pages/providers/providers.test.tsx`

**Interfaces:**
- Consumes: `keysPatch`, `syncSchedule`, `classifyProvider`, `providerStatusLabel`, `isCloud`, `useIsAdmin`, the acting tenant id from the session.

- [ ] **Step 1: Write the failing tests.**
  - `an admin sees every connector from GET /v1/admin/providers, grouped`: groups
    `Cloud providers` and `App uploads`, columns Provider, Status, Keys from (Environment,
    Console, Built-in), This tenant.
  - `a tenant that is not an admin sees GET /v1/providers, read only`.
  - `turns a provider on and off after a confirmation`: `PATCH /v1/admin/providers/<slug>` `{"enabled": false}`.
  - `turns a provider off for the acting tenant only`:
    `PATCH /v1/admin/tenants/<acting id>/providers/<slug>` `{"enabled": false}`; the column
    reads from `GET /v1/admin/tenants/<acting id>/providers`.
  - `saves keys, sending only what changed`: the PATCH body is `keysPatch(fields)`; a secret
    shows `Set` or `Not set` and is never filled with a value.
  - `keys set by the environment are locked with the reason`: inputs disabled, note
    `Set by the environment. Unset its OHS_ variables and restart to edit them here.`;
    a 409 shows the API message.
  - `a refused Configure shows the connector's message and changes nothing` (422 or 400 detail).
  - `forget needs the slug typed`: `DELETE /v1/admin/providers/<slug>`.
  - `shows the URLs to register, each with Copy`.
- [ ] **Step 2: Run them to see them fail.** Expected: FAIL.
- [ ] **Step 3: Implement.** Title `Providers`, subtitle `<n> of <m> on. Keys saved here are
  sealed with the instance encryption key.`. Detail page `/console/providers/:slug`: facts,
  `Sync schedule`, `RegisterUrls`, `ProviderKeysForm` (`Save keys`), `ProviderSwitch`,
  `TenantProviderSwitch`, `ForgetProviderCard` (`Forget saved settings`). No test connection
  card (not in the spec).
- [ ] **Step 4: Run them to see them pass.** Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat(console): providers pages"`.

---

### Task 10: Jobs

**Files:**
- Create: `src/pages/jobs/{JobsPage.tsx,FailedRunsTable.tsx,ParkedJobsTable.tsx,api.ts}`
- Test: `src/pages/jobs/jobs.test.tsx`

- [ ] **Step 1: Write the failing tests.**
  - `lists failed sync runs first, then parked jobs`: `GET /v1/pipeline/sync-runs?status=failed&limit=50`,
    `GET /v1/pipeline/etl-jobs?status=parked&limit=100`; cards `Failed sync runs`,
    `Parked processing jobs`.
  - `retries one parked job`: `POST /v1/pipeline/etl-jobs/<id>/retry` after a confirmation; a
    409 shows `This job is no longer parked.` with the API detail.
  - `retries every parked job`: `Retry all` confirms `Retry every parked job?` and sends
    `POST /v1/pipeline/etl-jobs/retry` `{}`; the toast gives the count the answer returns.
  - `empty state says nothing is stuck`.
  - `a read only session sees no Retry button`.
- [ ] **Step 2: Run, fail. Step 3: implement.** Subtitle `Work that stopped and waits for you.
  Everything else retries on its own.`
- [ ] **Step 4: Run, pass. Step 5: Commit.** `git commit -m "feat(console): jobs page"`.

---

### Task 11: Webhooks

**Files:**
- Create: `src/pages/webhooks/{WebhooksPage.tsx,EndpointsTable.tsx,WebhookPage.tsx,EndpointForm.tsx,DeliveriesTable.tsx,events.ts,api.ts}`, `src/ui/SecretOnce.tsx`
- Test: `src/pages/webhooks/webhooks.test.tsx`, `src/ui/secret-once.test.tsx`

**Interfaces:**
- Produces: `SecretOnce({ title: string; intro: string; value: string; onDone: () => void })`;
  `EVENT_TYPES` in `events.ts`, the list of `app.mjs`; when the schema types the event as an
  enum, a type-level check makes `tsc` fail if the two differ.

- [ ] **Step 1: Write the failing tests.**
  `secret-once.test.tsx`: `shows the secret once with Copy and drops it on Done`: after
  `Done` the value is in no element and in no React state the test can reach (re-render the
  parent: absent).
  `webhooks.test.tsx`:
  - `lists endpoints with events and status`: `GET /v1/webhooks`.
  - `creates an endpoint and shows its signing secret once`: `Add endpoint`, URL and event
    checkboxes, `POST /v1/webhooks`; the 201 secret appears in `SecretOnce`.
  - `edits URL and events`: `PATCH /v1/webhooks/<id>` with only the changed fields.
  - `pauses and resumes`: `PATCH` `{"enabled": false}` then `{"enabled": true}`.
  - `sends a test`: `POST .../test` `{}`.
  - `rotates the secret after a confirmation and shows the new one once`: `POST .../rotate-secret`.
  - `delete needs the URL typed`: `DELETE /v1/webhooks/<id>`, then `/console/webhooks`.
  - `retries and replays a delivery`: `POST .../deliveries/<d>/retry` and `.../replay`.
  - `a read only session sees no action`.
- [ ] **Step 2: Run, fail. Step 3: implement.** Detail route `/console/webhooks/:webhookId`,
  deliveries `GET .../deliveries?limit=50`, columns Event, Response, Duration, Attempt, Time.
- [ ] **Step 4: Run, pass. Step 5: Commit.** `git commit -m "feat(console): webhooks pages"`.

---

### Task 12: API keys

**Files:**
- Create: `src/pages/keys/{KeysPage.tsx,KeysTable.tsx,CreateKeyDialog.tsx,ScopePicker.tsx,scopes.ts,api.ts}`
- Test: `src/pages/keys/keys.test.tsx`, `src/pages/keys/scopes.test.ts`

**Interfaces:**
- Produces: `KEY_SCOPES: readonly KeyScope[]` with a type-level exhaustiveness check against
  `KeyScope` from the schema (a scope added to `api/openapi.yaml` and not here fails `tsc`);
  `groupScopes(scopes: readonly KeyScope[]): { resource: string; scopes: KeyScope[] }[]`
  (grouped by the part before `:`, `*` alone first).

- [ ] **Step 1: Write the failing tests.**
  `scopes.test.ts`: `groups by resource with * first`; `a :write scope implies its :read in the picker`.
  `keys.test.tsx`:
  - `lists keys with prefix, scopes, created, last used, expires, revoked`; the key of this
    session (its `prefix` equals `session.key_prefix`) shows `This session` and no Revoke.
  - `creates a key with chosen scopes and an optional expiry, shown once`: `POST /v1/keys`
    `{"scopes":["users:read","data:read"],"expires_at":"2027-01-01T00:00:00Z"}`; no scope
    chosen sends `{}`; the 201 `key` appears in `SecretOnce` only.
  - `a 403 naming a missing scope is shown`.
  - `revoke needs the prefix typed`: `DELETE /v1/keys/<id>`.
  - `a read only session sees no action`.
- [ ] **Step 2: Run, fail. Step 3: implement.** Title `API keys`, subtitle `Keys of <tenant>.
  A key created here can do at most what this session's key does.`; the scope picker
  disables scopes outside `session.key_scopes` (unless it holds `*`)
- [ ] **Step 4: Run, pass. Step 5: Commit.** `git commit -m "feat(console): API keys page with a scope picker"`.

---

### Task 13: Audit

**Files:**
- Create: `src/pages/audit/{AuditPage.tsx,AuditTable.tsx,AuditFilters.tsx,actions.ts,api.ts}`
- Test: `src/pages/audit/audit.test.tsx`

- [ ] **Step 1: Write the failing tests.**
  - `lists events newest first, 50 per page`: `GET /v1/audit?limit=50`, columns Time, Action,
    Route, Actor, Status; pager by cursor.
  - `filters by action`: choosing `key.create` sends `action=key.create`.
  - `filters by actor`: choosing a key from `GET /v1/keys` (shown by prefix) sends
    `actor_type=api_key&actor_id=<id>`; `Any actor` clears both.
  - `selecting an event inspects it`: kind `audit-event`, the event JSON.
  - `names the console session behind an event`: an event whose `actor_tenant_id` differs
    from the acting tenant shows `via console session of <tenant name>` under the actor
    (the name from `GET /v1/admin/tenants` for an admin, the id otherwise); null shows nothing.
  - `has no action button`.
- [ ] **Step 2: Run, fail. Step 3: implement.** Subtitle `Who read, exported, changed or deleted
  what, with which key. Kept <OHS_AUDIT_RETENTION_DAYS> days.` (value from the settings query
  for an admin, the sentence without it otherwise). Action options: the list of
  `AuditEvent.action` in the schema description, kept in `actions.ts`.
- [ ] **Step 4: Run, pass. Step 5: Commit.** `git commit -m "feat(console): audit page"`.

---

### Task 14: Settings

**Files:**
- Create: `src/pages/settings/{SettingsPage.tsx,SettingsSubnav.tsx,SettingsSection.tsx,SettingField.tsx,FieldControl.tsx,FieldMeta.tsx,SaveBar.tsx,ReviewDialog.tsx,ProcessesBanner.tsx,RestartNotice.tsx,DraftProvider.tsx,model.ts}`
- Modify: `src/pages/settings/api.ts` (from Task 4), `src/App.tsx` (wrap the router in `DraftProvider`), `src/routes.tsx`
- Test: `src/pages/settings/model.test.ts`, `src/pages/settings/settings.test.tsx`, `e2e/settings.spec.ts`

**Interfaces:**
- Consumes: `Setting`, `SettingsList`, `ProcessHeartbeat` (plan 1 types); `ApiError`.
- Produces:
  - `type Pending = Readonly<Record<string, string | null>>` (`null` reverts to default).
  - `interface FieldState { editable: boolean; locked: "env" | "bootstrap" | null; changed: boolean; invalidStored: string | null; inputError: string | null }`;
    `fieldState(s: Setting, pending: Pending): FieldState`.
  - `interface DiffRow { name: string; label: string; apply: "live" | "restart"; before: string; after: string; secret: boolean }`;
    `diffRows(settings: readonly Setting[], pending: Pending): DiffRow[]`;
    `restartCount(rows: readonly DiffRow[]): number`.
  - `toPatch(pending: Pending): Record<string, string | null>`.
  - `prunePending(settings: readonly Setting[], pending: Pending): { pending: Pending; dropped: string[] }`.
  - `sectionSlug(section: string): string`; `settingPath(s: Setting): string`.
  - `type ApplyOutcome = { kind: "ok"; revision: number; restart: string[] } | { kind: "moved" } | { kind: "conflict"; names: string[] } | { kind: "invalid"; errors: Record<string, string> }`;
    `applyOutcome(result: ApiResponse<unknown> | ApiError): ApplyOutcome`.
  - Hooks: `useSettingsQuery()` (`GET /v1/admin/settings`), `useProcessesQuery()`
    (`GET /v1/admin/processes`, every 30 s), `useApplySettings()` (`PATCH /v1/admin/settings`
    with `If-Match: "<revision>"`, the `revision` of the list the diff was built on), `useDraft(): { pending: Pending; set(name: string, v: string | null): void; discard(): void }`.

- [ ] **Step 1: Write the failing model tests** (`model.test.ts`):
  - `sectionSlug`: `Event stream` gives `event-stream`, `Rate limits` gives `rate-limits`,
    `Server` gives `server`.
  - `fieldState`: source `env` gives `locked: "env"`; apply `bootstrap` gives
    `locked: "bootstrap"`; both are not editable; an `invalid` message gives `invalidStored`.
  - `an emptied typed field blocks Review and is never sent`: pending `""` on `int`,
    `duration`, `rate`, `enum`, `bool` gives `inputError` `Enter a value, or revert to default.`;
    `toPatch` throws on it; on `string` and `list`, `""` is a value.
  - `diffRows`: before is `value` (null reads `not set`), after the pending one; `null` after
    reads `default (<default>)`, or `default (<default_note>)` when `default` is `""`; a secret reads `set` or `not set` before and `new value` after,
    never the typed text; rows sorted by section then name.
  - `restartCount` counts `restart` rows.
  - `prunePending after a reload`: drops names now set by the environment or bootstrap, and
    values equal to the new stored value; returns the dropped names.
  - `applyOutcome`: 200 `{revision: 6, changed, restart_required: ["OHS_X"]}` gives
    `{kind: "ok", revision: 6, restart: ["OHS_X"]}`; 412 `moved`; 409 `conflict` with
    `settings[].name`; 422 `invalid` with `settings[]` folded into name to message (a
    cross-setting 422 names one setting and lands on that field).
- [ ] **Step 2: Write the failing page tests** (`settings.test.tsx`):
  - `lists every section with a sub-navigation`: subnav entries for the sections present;
    `/console/settings/webhooks/OHS_WEBHOOK_PAUSE_AFTER` scrolls to and focuses that field.
  - `each field shows label, OHS_ name, source, apply mode and default`.
  - `a derived default shows its note`: `default: ""` with `default_note` shows the note.
  - `locked fields say why`: env: `Set by OHS_X in the environment. Unset it and restart to
    edit it here.`; bootstrap: `Bootstrap setting. Set OHS_X in the environment.`; the secret
    of a locked field reads `set`.
  - `a field skipped as invalid shows its message`.
  - `the save bar counts pending changes and restarts`: two edits, one restart: `2 unsaved
    changes`, `1 needs a restart`; `Discard` clears.
  - `Review opens the diff, Apply sends one PATCH with If-Match`: rows name, old, new, apply
    mode; `Apply` sends `PATCH /v1/admin/settings` with `If-Match: "41"` and the body
    `toPatch(pending)` (strings or null only, never `""`); on 200 the draft is empty and a notice lists the restart names with
    `They apply when each process next starts. Restart with your own tooling (Docker, systemd,
    Kubernetes).` (names from `restart_required`). No restart button.
  - `revert to default sends null`: `Revert to default` on a console-sourced field shows
    `default (<Default>)` in the diff and sends `{"OHS_X": null}`.
  - `412 reloads and reopens the diff against the new values`: first PATCH 412; the page
    refetches `GET /v1/admin/settings` (revision 42), reopens the dialog with the new old
    values; the second PATCH sends `If-Match: "42"`.
  - `422 marks each invalid field and saves nothing`: messages under the named fields, draft kept.
  - `409 names the locked settings`.
  - `a typed secret is never echoed`: type `s3cr3t-value` into a secret field; it is absent
    from the dialog, from the inspector, from the `Copy as .env` text (the export route's
    answer) and from a 422 message rendering; after Apply the input is empty.
  - `pending changes survive a 401 round trip through sign in`: edit, a query answers 401, sign
    in again, back on Settings with the edit pending.
  - `the banner lists processes that are behind or stale`: from `GET /v1/admin/processes`,
    one row per process with `behind` or `stale` true: `host`, `roles`, `version`, `behind`
    or `stale`, its `skipped` names and `reload_error` when present; none: no banner.
  - `Copy as .env copies GET /v1/admin/settings/export; Export downloads it as ohs.env`.
  - `a tenant that is not an admin gets an explanation, not an empty page`: heading `Settings`,
    text `Settings belong to the instance. Sign in with a key of an instance admin tenant.`;
    no request to `/v1/admin/settings`.
  - `a read only session can read but not edit`: controls disabled, no save bar.
- [ ] **Step 3: Run them to see them fail.** Run: `npx vitest run src/pages/settings`. Expected: FAIL.
- [ ] **Step 4: Implement.** Controls by type: `bool` toggle; `int` number input with the
  bounds; `duration`, `url`, `string` text (mono for names and values); `enum` select of its
  values; `list` text, comma separated; `rate` two numbers `req/s` and `burst` joined `r/b`;
  secret: write-only input with `Replace`, never prefilled. The precedence strip of mockup C
  (`Environment` > `Console` > `Default`). Sub-navigation follows the scroll with an
  `IntersectionObserver`. The draft lives in `DraftProvider` above the router, so it survives
  the sign-in round trip; it is not persisted to storage.
- [ ] **Step 5: Write the end to end test** (`e2e/settings.spec.ts`):
  `changes a live and a restart setting through Review and Apply`: read
  `GET /v1/admin/settings` in the page; pick the first `int` entry with apply `live` and
  source `default`, and the first `int` with apply `restart` and source `default` (the e2e
  servers set neither in their environment); set each to its default plus one within bounds;
  Review shows two rows; Apply; the live field reads source `console`; the notice names the
  restart setting; the banner lists the two e2e processes as behind. Also: `OHS_LISTEN` reads
  as a locked bootstrap field.
- [ ] **Step 6: Run everything.** Run: `npx vitest run && npm run build && npm run e2e`. Expected: PASS.
- [ ] **Step 7: Commit.** `git commit -m "feat(console): settings page with review, apply and process banner"`.

---

### Task 15: Command palette

**Files:**
- Create: `src/palette/{CommandPalette.tsx,search.ts}`
- Modify: `src/shell/AppShell.tsx`, `src/shell/TopBar.tsx`, pages that open a create dialog from `location.state`
- Test: `src/palette/search.test.ts`, `src/palette/palette.test.tsx`

**Interfaces:**
- Produces:
  `interface PaletteItem { group: "Go to" | "Actions" | "Settings"; title: string; hint?: string; to?: string; open?: "create"; run?: "theme" | "sign-out" }`;
  `paletteItems(ctx: { admin: boolean; canWrite: boolean; settings: readonly Setting[] }): PaletteItem[]`;
  `searchPalette(items: readonly PaletteItem[], query: string, limit?: number): PaletteItem[]` (default 14).
  Pages open their create dialog when `location.state?.open === "create"`.

- [ ] **Step 1: Write the failing tests.**
  `search.test.ts`:
  - `empty query lists the pages, then the actions`.
  - `ranks a prefix of the title or hint before a word prefix before a substring, case
    insensitive, keeping order on ties`.
  - `finds a setting by name or by label`: `OHS_LOG` finds `OHS_LOG_LEVEL`; `pause after`
    finds `OHS_WEBHOOK_PAUSE_AFTER` with `to` `/console/settings/webhooks/OHS_WEBHOOK_PAUSE_AFTER`.
  - `webhook lists the Webhooks page first`.
  - `no Settings items for a tenant that is not an admin; no write actions when read only`.
  Actions: `Create user` (`/console/users`), `Create API key` (`/console/keys`),
  `Add webhook endpoint` (`/console/webhooks`), each `open: "create"`; `Switch theme`,
  `Sign out`.
  `palette.test.tsx`: `opens with Ctrl+K and with Meta+K, closes with Escape`;
  `arrows move, Enter opens the address`; `Create user opens the New user dialog`;
  `focus returns to where it was on close`.
- [ ] **Step 2: Run, fail. Step 3: implement.** Input placeholder `Search pages, settings and
  actions`; footer keys `↑ ↓ move`, `↵ open`, `esc close`.
- [ ] **Step 4: Run, pass. Step 5: Commit.** `git commit -m "feat(console): command palette"`.

---

### Task 16: Setup page

**Files:**
- Create: `src/pages/setup/{SetupPage.tsx,setup.ts}`
- Modify: `src/routes.tsx` (public route `/console/setup`), `internal/console/console.go`
  (drop the old setup files from the embed and `serve`), `internal/console/console_test.go`
- Test: `src/pages/setup/setup.test.ts`, `src/pages/setup/setup-page.test.tsx`

**Interfaces:**
- Produces (ports of `setup.mjs`): `SETUP_PATH = "/v1/setup"`, `MAX_TENANT_NAME = 100`,
  `class SetupError`, `setupMessage(status: number, detail: string): string`,
  `buildSetupRequest(token: string, name: string): { setup_token: string; tenant_name?: string }`,
  `isApiKey(v: unknown): v is string` (`^ohs_sk_[A-Za-z0-9_-]{16,}$`, moved here from Task 2's
  sign-in, which imports it), `setupState(status: number, body: unknown): "needed" | "done" | "off"`.

- [ ] **Step 1: Write the failing tests.** Port every test of `internal/console/setup.test.mjs`
  but `the key lands where the console reads it` (the key is no longer stored). New:
  - `setup calls are anonymous`: `GET` and `POST /v1/setup` go with `anonymous: true` (no
    cookie, no CSRF).
  - `shows the key once and offers to sign in with it`: after `POST /v1/setup` 201 the page
    shows the tenant id and the key with Copy; `Sign in with this key` posts
    `/console/session` and opens `/console/overview`; the key is then in no element and in no
    storage.
  - `closed setup says so and links to sign in` (`needed: false`), `off` (404) says
    `First-run setup is off on this instance.`
  Go: `TestSetupRouteServesTheApp`: `/console/setup` is `index.html` with `no-cache`;
  `/console/setup.mjs` is 404 (an asset-like path missing from dist: add `.mjs` to the rule
  that answers 404 for a missing file with an extension). Remove `TestSetupPageServed` and
  `TestSetupScriptAvoidsUnsafeSinks`.
- [ ] **Step 2: Run, fail. Step 3: implement.** Copy from the old `setup.html`: heading,
  token field `Setup token` (`type="password"`), `Tenant name` (optional, at most 100),
  button `Create the first tenant`.
- [ ] **Step 4: Run, pass**: `npx vitest run`, `npm run build`, `go test ./internal/console/`.
- [ ] **Step 5: Commit.** `git commit -m "feat(console): first-run setup page in the React console"`.

---

### Task 17: Parity check, old console removed, docs

**Files:**
- Delete: `internal/console/static/{index.html,app.mjs,lib.mjs,console.css,setup.html,setup.mjs}`,
  `internal/console/app.test.mjs`, `internal/console/setup.test.mjs`
- Modify: `Makefile` (`test-js` keeps `internal/shl/viewer/view.test.mjs` only),
  `.github/workflows/ci.yml` (that step's name), `docs/console.md`, `docs/specs/admin-console.md`
  (status line points to console-v2 for the page design, if plan 1 or 2 has not done it),
  `CONTRIBUTING.md` (Node 22 only for console work, `make console`), `CHANGELOG.md`
- Test: `e2e/console.spec.ts` (extended)

- [ ] **Step 1: Extend the end to end test.** `opens every page`: after signing in, visit
  Overview, Users, the demo user (each tab), Providers, one provider, Jobs, Webhooks, API keys,
  Audit log, Settings; each shows its heading and no `This page failed`. `switches tenant and
  back`: switch to `e2e-other`, the nav reads `Acting as e2e-other`, Users is empty, switch back.
- [ ] **Step 2: Run the parity check.** With `npm run e2e` green, walk this list in the new
  console against a seeded instance and tick each line in the pull request description; any
  line that fails blocks Step 3.

  | admin-console.md | New console | Test |
  |---|---|---|
  | Overview: five stages, needs attention, links | Overview | Task 5 |
  | Providers: status, kinds, key source, URLs, keys needed | Providers, Provider | Task 9 |
  | Turn a provider on or off, save keys (admin) | Provider | Task 9 |
  | Forget a provider's keys (typed slug) | Provider | Task 9 |
  | Users, 50 per page; create a user | Users | Task 7 |
  | User: connections, freshness, sync runs, coverage, data outputs | User tabs | Task 8 |
  | Connection link, sync now, disconnect | User | Task 8 |
  | Preview recent data; FHIR, Open mHealth, CSV, archive | User, Data | Task 8 |
  | Delete the user (typed name) | User | Task 8 |
  | Jobs: failed runs first, parked jobs; retry one, retry all | Jobs | Task 10 |
  | Webhooks: create, edit, off and on, test, rotate, delete (typed URL), retry, replay | Webhooks | Task 11 |
  | API keys: create (shown once), revoke (typed prefix), no self revoke | API keys | Task 12 |
  | Audit log, newest first, by action | Audit | Task 13 |
  | Section 6: confirm every write | ConfirmDialog | Tasks 7 to 14 |
  | Section 6: one-time secrets shown once, dropped on close | SecretOnce | Tasks 11, 12, 16 |
  | Section 6: health values only on Preview and downloads, audited | User, Data | Task 8 |
  | Section 6: CSP, no inline code, nothing from elsewhere, downloads built in the page | Go tests, ESLint | Tasks 1, 8 |
  | First-run setup page | Setup | Task 16 |

  Not carried over, by the spec: the user page's health charts (non-goal "Charts of health
  data"), the pasted key held in `sessionStorage` (replaced by the session), the
  `ohs-console-mode` meta tag (read only comes from the session).
- [ ] **Step 3: Delete the old files** listed above; update `make test-js` and the CI step
  name to `Share viewer script helpers (node --test, no dependency)`.
- [ ] **Step 4: Docs.** `docs/console.md`: "Turn it on" (sign in once, the session), "Pages"
  rewritten for the new navigation, inspector and command palette, "Security model"
  (session cookie, CSRF header, no key in the page, the unchanged CSP, cache headers),
  "Look and feel" (tokens of spec 3.2, Light, Dark, Auto, the breakpoints), and a
  "Working on the console" section: `make console`, `make console-dev` with
  `OHS_DEV_TARGET`, `make console-e2e` with `OHS_E2E_DATABASE_URL`, and that `dist` and
  `schema.ts` are committed and checked by CI. Screenshots light and dark replace the old ones.
  CHANGELOG: the new console, sessions in the page, the Users and Audit filters of Task 6.
- [ ] **Step 5: Run everything.**
  Run: `make test test-js lint && make console && git status --porcelain && cd web/console && npm run lint && npm run typecheck && npm test && npm run e2e`
  Expected: PASS, `git status --porcelain` empty after `make console`.
- [ ] **Step 6: Commit.** `git commit -m "feat(console): retire the hand-written console after the parity check"`.

---

## Self-review

- **Spec coverage.** 3.1: Task 1 (stack, schema, dist, cache, CSP, SPA fallback, make
  targets), Task 5 (fonts used by every page from Task 3). 3.2: Task 3 (tokens, Light Dark
  Auto, mono), Task 5 (black band). 3.3: Tasks 3, 4, 15 (panes, inspector, breakpoints,
  palette, addresses per setting in Task 14). 3.4: Tasks 5, 7 to 14, 16. 3.5: Task 2 (status
  and message, 401 round trip), Task 4 (raw answer), Task 14 (412, 422), Task 3 (error
  boundary). Section 4 front unit: Tasks 4 (snippets), 14 (diff, field state), 15 (palette);
  end to end: Tasks 5, 7, 14, 17; contract: `tsc` over `schema.ts` in every task, the OpenAPI
  route test in Task 6. Section 5: Tasks 1 and 5. Section 6 item 3: Task 17.
- **Spec gaps found and how the plan settles them.** (a) `GET /v1/users` has no external id
  filter and `GET /v1/audit` no actor filter: Task 6 adds both. (b) The spec does not say
  whether `/v1/admin/*` follows the acting tenant after a switch; plan 2 settles it on the
  session tenant, and Task 17's `switches tenant and back` proves it end to end. (c) The
  getting started item "first data" has no lifetime count in `GET /v1/pipeline`; Task 5 uses
  the last 24 hours, so an idle tenant with old data shows it undone. (d) The spec lists no
  action for the Consents and Shares tabs; they are read only. (e) Settings for a tenant that
  is not an admin is unspecified; Task 14 shows an explanation. (f) `git diff --exit-code`
  misses new untracked files in `dist`; Task 1 adds a `git status --porcelain` check. (g) The
  dev proxy needs the `Origin` rewrite: plan 2 compares `Origin` with `OHS_PUBLIC_URL`, so
  `make console-dev` expects the local server's public URL to be `OHS_DEV_TARGET`. (i) Spec 3.5
  does not cover a rotated CSRF token; following plan 2, the client refreshes once and retries
  once.
  (h) The mockup's Restart now, secret Reveal, Test connection and Export NDJSON are left out:
  the spec forbids the first two and does not ask for the others.
- **Type consistency.** `ApiClient`, `ApiError`, `useSession`, `useCanWrite`, `useIsAdmin`,
  `useInspect`, `InspectorKind`, `CSRF_REFUSED`, `Pending`, `DiffRow`, `ApplyOutcome`, `PaletteItem` keep one
  name and shape across tasks; `isApiKey` moves from Task 2 to Task 16's `setup.ts` and Task 2
  imports it from there once it exists (Task 2 defines it in `session/` and Task 16 moves it).
- **Proportion.** Signatures, test names and assertions only; no component body is written
  out. The only fixed text is copy the page shows and the snippet the tests compare.

Plan complete and saved to `docs/plans/console-v2-3-react-console.md`.
