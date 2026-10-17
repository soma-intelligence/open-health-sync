# Web example

A small Next.js app (App Router, TypeScript) that does what your backend does with Open Health
Sync, from the first user to webhooks, with the [TypeScript server SDK](../../docs/server-sdks.md).
It runs against your instance with a tenant API key. The key stays on the Next.js server: pages
render there and forms post to Server Actions, so the browser never receives it.

Four pages, one per step:

1. **Users.** Create an end user under your own reference (`external_ref`) and list the
   tenant's users.
2. **Get data in**, two ways:
   - Connect a source: the app asks for a connection link, sends the browser to the hosted
     connection page, and the page sends the person back to the app.
   - Upload sample data: the app mints a short-lived token that can only upload, then sends 14
     days of steps, resting heart rate and sleep through the upload route a phone app uses. The
     data is synthetic, made up in code, and marked so in the page and in every line's metadata.
3. **Read it back.** Daily summaries, resting heart rate and the latest night from the REST
   API, then one record as REST JSON, FHIR R4 and Open mHealth, each with the response in a
   collapsible block.
4. **Webhooks.** A route handler that verifies each request with the SDK's `verifyWebhook` and
   keeps the last 20 events in memory, shown on the page with a button to send test events.

## Requirements

- Node 20.9 or later (Next.js 16; CI uses Node 22).
- A running Open Health Sync server, for example from the
  [repository quickstart](../../README.md#quickstart).
- For step 2a only: an OAuth provider turned on, on the admin console's Providers page or with
  its `OHS_<PROVIDER>_*` variables ([connectors](../../docs/connectors/README.md)). The upload
  in step 2b needs nothing.
- For step 4 only: an instance that can reach this app (see
  [Receive webhooks on your machine](#receive-webhooks-on-your-machine)).

Next.js 16.4, React 19.3, TypeScript 7. The SDK is installed from this repository
(`"@open-health-sync/server": "file:../../sdks/server/typescript"`); nothing else is a runtime
dependency.

## Quickstart

From the repository root. First a tenant key for the example (or create one on the admin
console's API keys page):

```sh
ohs admin tenant create --name web-example
# with the repository's docker compose:
docker compose --env-file .env -f deploy/docker-compose.yml exec ohs /ohs admin tenant create --name web-example
```

The server SDK is not on a package registry; build it once:

```sh
cd sdks/server/typescript
npm ci && npm run build
```

Then the app:

```sh
cd ../../../examples/web
cp .env.example .env.local     # set OHS_API_KEY to the key printed above
npm install
npm run dev
```

Open <http://localhost:3000>, create a user, upload the sample data and read it back.

| Variable | Needed | What |
|---|---|---|
| `OHS_URL` | yes | Base URL of the instance, `http://localhost:8080` by default |
| `OHS_API_KEY` | yes | The tenant API key (`ohs_sk_...`). It can read every user of the tenant |
| `OHS_WEBHOOK_SECRET` | no | The `whsec_...` secret of this app's webhook endpoint. Without it, the app keeps the secret of an endpoint registered from its own page in memory, until it restarts |
| `APP_URL` | no | Where this app is reached, `http://localhost:3000` by default. The connection page sends people back to it, and webhooks go to `APP_URL/api/webhooks` |

None of the names starts with `NEXT_PUBLIC_`, so Next.js never copies them into browser code.

## The steps and their routes

### 1. Users: `/`

| Route | Why |
|---|---|
| `POST /v1/users` | Create the user with your `external_ref`. The app sends an `Idempotency-Key` built from the reference, so a double submit creates one user |
| `GET /v1/users` | The users list; the SDK's `iterUsers` follows the cursor |
| `GET /v1/tenant`, `GET /healthz` | The connection card: which tenant the key belongs to, and whether the instance answers |

### 2. Get data in: `/users/{id}`

Connect a source:

| Route | Why |
|---|---|
| `GET /v1/providers` | The OAuth providers turned on, listed on the card |
| `POST /v1/users/{id}/connect-sessions` | A connection link for this user, with `redirect_uri` set to `APP_URL/users/{id}`, `app_name` and a 15-minute lifetime. The Server Action redirects the browser to the `url` it gets back |
| `GET /v1/users/{id}/connections` | The user's connections and their last sync |

The hosted page lists the providers, runs the provider's consent, and sends the person back to
`APP_URL/users/{id}?status=connected&provider=<slug>` (`status=error` when it failed), which the
page shows as a banner. See [connect-page.md](../../docs/connect-page.md).

Upload sample data:

| Route | Why |
|---|---|
| `POST /v1/users/{id}/tokens` | With the tenant key, a user token with `scopes: ["upload"]` that lives 5 minutes |
| `POST /v1/ingest/apple-health` | With that token only, one call per part: gzip NDJSON, with `X-OHS-Type`, `X-OHS-Day`, `X-OHS-Batch` and `X-OHS-Part` |

The generator (`lib/synthetic.ts`) makes the 14 days that end yesterday, in UTC:

- Steps: one `HKQuantityTypeIdentifierStepCount` line per hour from 07:00 to 21:00, about 9,000
  a weekday and 7,000 a weekend day.
- Resting heart rate: one `HKQuantityTypeIdentifierRestingHeartRate` line a day, around 56 bpm.
- Sleep: one night a day as `HKCategoryTypeIdentifierSleepAnalysis` lines (in bed, then deep,
  core, REM and awake stages in cycles). A night starts the evening before, so the parts cover
  15 days.

Each line has `"metadata": {"HKTimeZone": "UTC", "synthetic": true}` and the source name "Open
Health Sync web example". The values come from a seeded generator, so the same days give the
same bytes: upload again and the server answers `duplicate` for every part, and nothing is
stored twice. That is 43 parts and 568 lines. The page shows what each day holds and one part
as sent, before you upload.

### 3. Read it back: `/users/{id}/data`

| Route | Why |
|---|---|
| `GET /v1/users/{id}/coverage` | Pending processing jobs, days with data, rejected lines. While jobs are pending, the page refreshes every 3 seconds |
| `GET /v1/users/{id}/daily` | Steps per day. Apple Health sends no daily summary, so the server derives the day's steps from the samples (`metadata.derived`) |
| `GET /v1/users/{id}/samples?type=resting_heart_rate` | The resting heart rate line |
| `GET /v1/users/{id}/sleep` | The latest night and its stages |
| `GET /v1/users/{id}/fhir/Observation/qs-{sample id}` | The latest resting heart rate as a FHIR R4 Observation (LOINC 8867-4 and 40443-4, UCUM `/min`) |
| `GET /v1/users/{id}/omh/data-points?schema=omh:heart-rate` | The same record as an Open mHealth `heart-rate` data point |

The links under each format open `/users/{id}/raw/{samples,daily,sleep,fhir,omh}`, a route of
this app that passes the JSON of one call through, so you can read a whole response in a tab.

### 4. Webhooks: `/webhooks`

| Route | Why |
|---|---|
| `POST /v1/webhooks` | "Register this endpoint" registers `APP_URL/api/webhooks` for every event type and shows the secret once |
| `POST /v1/webhooks/{id}/rotate-secret` | Offered when the endpoint exists but the app has no secret for it (after a restart without `OHS_WEBHOOK_SECRET`) |
| `POST /v1/webhooks/{id}/test` | A sample of the chosen event type, marked `"test": true` |
| `GET /v1/webhooks/{id}/deliveries` | The last deliveries as the server saw them: status, attempts, the answer code |

The receiver, `app/api/webhooks/route.ts`, reads the raw body, checks it with `verifyWebhook`
(HMAC-SHA256 of the id, timestamp and body; timestamp within 5 minutes) and answers 204. A bad
signature gets 400, and no secret at all gets 503, so the server retries later. The last 20
requests are kept in memory and listed on the page, refused ones too, with the reason. A retry
repeats the event id, so it is counted, not listed twice. Uploading sample data sends one
`data.written` per processed part.

## Receive webhooks on your machine

The instance posts to `APP_URL/api/webhooks`, so it must reach this app.

- **The `ohs` binary on the same machine** as the app: start the instance with
  `OHS_WEBHOOK_ALLOW_PRIVATE=true`, which lets endpoints use plain http and local addresses, and
  keep `APP_URL=http://localhost:3000`. Leave it off on an instance with tenants you do not
  trust ([hardening](../../docs/hardening.md)).
- **Anything else** (the docker compose setup, where `localhost` is the container, or a remote
  instance): open a tunnel to port 3000, for example `cloudflared tunnel --url
  http://localhost:3000` or `ngrok http 3000`, set `APP_URL` to the tunnel's https address in
  `.env.local`, and restart `npm run dev`.

Then press "Register this endpoint" on the Webhooks page, or register it yourself:

```sh
curl -s -X POST $OHS/v1/webhooks -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d "{\"url\":\"$APP_URL/api/webhooks\",\"description\":\"web example\"}"
# {"id":"...","url":"...","status":"active",...,"secret":"whsec_..."}
```

Put the `secret` in `.env.local` as `OHS_WEBHOOK_SECRET` and restart the app, so it keeps
verifying after a restart. See [webhooks.md](../../docs/webhooks.md) for the events.

## How it works

- **The key stays on the server.** `lib/config.ts` reads the environment and `lib/ohs.ts`
  builds the SDK client, both on the server. Pages are server components rendered on each
  request (`dynamic = "force-dynamic"`); every write is a Server Action in `app/actions.ts` that
  returns a short result (a message, a few facts, a webhook secret once). The only browser code
  is the form state (`components/action-form.tsx`) and the refresh of live pages
  (`components/auto-refresh.tsx`).
- **The upload is the phone's path.** `lib/upload.ts` mints the upload-only token with the tenant
  key, then makes a second client that holds only that token, as an app on the phone would. It
  gzips each part, sends four at a time, and retries a 503 or 429 with a growing wait.
- **Errors are shown, not hidden.** A failed call shows the HTTP status, the problem's title and
  detail, and the request id from the SDK's `OhsError`, so you can find it in the server logs.
- **Charts without a library.** `components/charts.tsx` draws the steps columns and the heart
  rate line in HTML and SVG at a fixed height; the values are in the table and figures beside
  them.

| File | What it holds |
|---|---|
| [`app/page.tsx`](app/page.tsx) | Step 1: users |
| [`app/users/[id]/page.tsx`](app/users/[id]/page.tsx) | Step 2: connect a source, upload sample data |
| [`app/users/[id]/data/page.tsx`](app/users/[id]/data/page.tsx) | Step 3: read it back |
| [`app/webhooks/page.tsx`](app/webhooks/page.tsx) | Step 4: webhooks |
| [`app/actions.ts`](app/actions.ts) | The Server Actions behind every form |
| [`app/api/webhooks/route.ts`](app/api/webhooks/route.ts) | The webhook receiver |
| [`app/users/[id]/raw/[view]/route.ts`](app/users/[id]/raw/[view]/route.ts) | The JSON of one read call, passed through |
| [`lib/synthetic.ts`](lib/synthetic.ts) | The synthetic data generator |
| [`lib/upload.ts`](lib/upload.ts) | The upload-only token and the upload |
| [`lib/webhooks.ts`](lib/webhooks.ts) | Verification and the in-memory inbox |

The SDK is a `file:` dependency, so `node_modules/@open-health-sync/server` is a link to
`sdks/server/typescript`, outside the app. Next.js only bundles files under its root, so
`next.config.ts` sets `turbopack.root` and `outputFileTracingRoot` to the repository root. In
your own app, with the SDK files copied in or installed from a package, you do not need either.

## Checks

What CI runs (`.github/workflows/examples.yml`), after building the SDK:

```sh
cd examples/web
npm ci
npm run typecheck   # next typegen && tsc --noEmit
npm test            # node --test: the generator and webhook verification
npm run build
```

`test/synthetic.test.ts` checks that the generator is deterministic, that every part holds one
type and one day and is numbered once, that the values stay in range and that every line is
marked synthetic. `test/webhooks.test.ts` runs the SDK's signed vectors
(`sdks/server/testdata/webhook-vectors.json`) through the receiver, then posts freshly signed
requests to the route handler: a valid one, a retry of it, a changed body, another secret, no
secret, and more than 20 events.

## Limits

- No sign-in. Whoever opens the app acts with the tenant key, and the raw JSON route reads any
  user of the tenant by id. Run it on your machine, never on a public address.
- The webhook inbox lives in the memory of one process: a restart empties it, and two instances
  of the app would each see part of the events. A real receiver stores event ids and queues the
  work.
- The sample data is in UTC and times are shown in UTC.
- The sample data is synthetic and the same for every user. To load real records, use a phone
  app ([examples](../README.md)) or `ohs import apple-health` with an export.

## Licence

AGPL-3.0-only, like the rest of the repository ([LICENSE](../../LICENSE)).
