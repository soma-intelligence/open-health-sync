# First-run setup in the browser

> **Next release.** This is on `main` and ships in the next release; the 0.1.0 image does not have it.

A fresh instance has no tenant, so no API key exists yet. The usual way to get the first one is a
terminal: `ohs admin tenant create`, or `OHS_BOOTSTRAP_TENANT`, which prints the key once in the
logs. Where there is no terminal and no log viewer, as with Umbrel, CasaOS and similar app
stores, the first-run setup does it from a browser instead: open `/console/setup`, type the
setup token, and the page creates the first tenant and shows its API key once.

## Turn it on

Set a setup token, and the console so the page is served:

```sh
# .env
OHS_SETUP_TOKEN=<openssl rand -hex 32>
OHS_CONSOLE=true
```

With docker compose, put both in `.env` (next to `OHS_ENCRYPTION_KEY`), run
`docker compose -f deploy/docker-compose.yml up -d`, then open
`http://localhost:8080/console/setup` (or your https address). Type the token and, optionally, a
tenant name (default `default`). The page shows the tenant id and its API key with a copy button.
**Store the key at once: it is not shown again.** The same browser tab is then signed in to the
[console](console.md) with it until the tab closes. The first tenant is the
[instance admin](console.md#the-instance-admin): from the console, its key turns providers on and
saves their keys.

`OHS_SETUP_TOKEN` must be 16 characters or more, or the process refuses to start. Unset (the
default), the feature is off: `GET` and `POST /v1/setup` are not mounted and answer 404, and the
console never shows the setup link.

On an app store, the store's per-install app password (the secret it generates for each install
and shows you on the app's page) is the natural value for `OHS_SETUP_TOKEN`.

## The routes

The page calls two routes, which a script can call too:

```sh
curl -s $OHS/v1/setup
# {"needed":true}

curl -s -X POST $OHS/v1/setup -H 'Content-Type: application/json' \
  -d '{"setup_token":"'"$OHS_SETUP_TOKEN"'","tenant_name":"home"}'
# {"tenant_id":"...","api_key":"ohs_sk_..."}
```

| Route | Answer |
|---|---|
| `GET /v1/setup` | `{"needed": true}` while the instance has no tenant, else `false`. No credential |
| `POST /v1/setup` | 201 with `tenant_id` and `api_key` (`Cache-Control: no-store`); 401 for a wrong token; 429 with `Retry-After` past the [rate limit](self-hosting.md#rate-limits) (`OHS_RATE_LIMIT_OAUTH`, per client address); 409 once any tenant exists; 400 for a bad body or tenant name; 413 above 4 KiB |

The tenant name is trimmed, at most 100 bytes and without control characters. The full shapes are
in [`api/openapi.yaml`](../api/openapi.yaml).

## Security model

- **One shot.** `POST /v1/setup` creates a tenant only while the instance has none. The check and
  the creation run in one transaction under an advisory lock that every tenant creation takes
  (`ohs admin`, the bootstrap tenant, the setup), so two calls at once, or a call racing
  `ohs admin tenant create`, never yield two first tenants: one call wins, the others get 409.
- **Closed for good.** Once any tenant exists, however it was created, the setup answers 409,
  even to the right token, and the page says the instance is set up. Leaving `OHS_SETUP_TOKEN`
  set afterwards opens nothing; remove it when you like.
- **The token is a password.** It is compared in constant time (SHA-256 digests of both sides
  through `crypto/subtle`), never logged, never stored, and never echoed in an answer. It is
  only useful before the first tenant exists, so the window is short; still, pick a random value.
- **The key is shown once.** It is returned in the 201 body only, stored as a hash like every key,
  and never logged. If the page is closed before you copy it, the instance is set up but the key
  is lost: create another with `ohs admin key create --tenant <id>`, or start over on an empty
  database.
- **Audited.** The creation is the first event in the new tenant's [audit log](audit-log.md):
  `tenant.create`, actor `system` / `setup`, with the key's prefix only. It is written in the same
  transaction as the tenant, so a failed write creates nothing. A refused call (401, 409) is not
  an audit event, like any call without a valid credential.
- **Same origin, strict headers.** `/console/setup` is a page of its own beside the console,
  with the same Content-Security-Policy and headers. Its one script posts only to `/v1/setup` on
  the same server, without cookies, and inserts the key as text.
- **Put HTTPS in front.** The token and the key cross the network in the request and the
  answer. On anything but `localhost`, serve the instance through a reverse proxy with TLS (see
  [self-hosting.md](self-hosting.md)) before you open the setup page.
