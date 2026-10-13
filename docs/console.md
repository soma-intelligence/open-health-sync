# Admin console

A small, read-only web page inside the `ohs` binary for people who run an instance: which users
exist, how their connections are doing, which sources are behind or failing, and whether webhook
deliveries go through. It answers "is everything syncing?" without writing `curl` calls. It is
not a developer dashboard and changes nothing: no button in it writes to the server.

## Turn it on

The console is off by default. Set `OHS_CONSOLE=true` on the process that runs the `api` role and
restart it:

```sh
# .env
OHS_CONSOLE=true
```

Then open `https://your-host/console` and paste a tenant API key (`ohs_sk_...`, from
`ohs admin tenant create`, `ohs admin key create` or the bootstrap tenant). With `OHS_CONSOLE`
unset or `false` the route is not mounted and `/console` answers 404.

The console sees what that key sees: one tenant. To look at another tenant, sign out and use its
key.

## What it shows

**Overview.** Liveness (`/healthz`) and readiness (`/readyz`), the number of users (counted by
paging through `GET /v1/users`, shown as `10000+` past ten thousand), the number of webhook
endpoints and how many of them are failing or disabled, and the providers enabled on the
instance with their kinds (`oauth`, `push`, `device`).

**Users.** The tenant's users, 50 per page, oldest first: id, `external_ref`, creation time and
status. A user being deleted shows as `deleting`.

**User detail.**

- Connections: provider, status, last sync, when the initial backfill finished. A connection in
  `needs_reauth` is highlighted: nothing syncs until the user connects that provider again.
- Freshness, one line per source: state, how far the data reaches (`synced_through`), lag, last
  attempt, consecutive failures and the last error. `stale`, `failing` and `needs_reauth` are
  red, `syncing` is amber. See [background-sync.md](background-sync.md) for what each state
  means.
- Coverage: pending and parked ETL jobs, rejected records (red above 0), and per provider the
  number of days and records stored, the first and last day, and the last sync.

**Webhooks.** Every endpoint with its status, `failing_since`, last success and the reason it
was disabled. A disabled endpoint, or one failing right now, is red. Opening one lists its 50
most recent deliveries: event type, status, attempts, last HTTP code, last error and the next
attempt. Parked deliveries are red. Retrying a delivery or changing an endpoint is done with the
[webhook routes](webhooks.md), not here.

## Security model

- **No new credential and no new server-side auth.** The page is static. It calls the existing
  API routes with `Authorization: Bearer <key>`, exactly as `curl` would, so the API's own key
  checks apply. A user token (`ohs_ut_...`) is refused.
- **The key stays in the browser tab.** It is kept in `sessionStorage`, which the browser clears
  when the tab closes; "Sign out" clears it at once. It is never put in a cookie, a URL or
  `localStorage`, and never sent to any other origin.
- **Read only.** Every request goes through one wrapper that refuses any method but `GET` and any
  URL that is not a path on the same server. The routes it reads are `GET /v1/providers`,
  `/v1/users`, `/v1/users/{id}`, `/v1/users/{id}/connections`, `/v1/users/{id}/freshness`,
  `/v1/users/{id}/coverage`, `/v1/webhooks`, `/v1/webhooks/{id}`,
  `/v1/webhooks/{id}/deliveries`, `/healthz` and `/readyz`. The console shows no health values:
  coverage counts records, it does not read them.
- **Same origin, strict headers.** The page, its one script and its one stylesheet are embedded
  in the binary; nothing loads from a CDN. Every console response carries
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; connect-src
  'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and
  `X-Frame-Options: DENY`; the HTML is sent with `Cache-Control: no-store`. The page has no inline
  script, and API data is inserted as text, never as markup.
- **Exposure.** The page itself holds nothing secret, and without a key it shows only the sign-in
  form. Still, if you prefer that operators reach it only from inside, block `/console` at your
  reverse proxy for the public address, or put it behind the proxy's own login, or leave
  `OHS_CONSOLE` off and read the same routes with `curl`.
