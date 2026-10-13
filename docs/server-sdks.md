# Server SDKs

Two clients of the API for your backend, the code that holds a tenant API key (`ohs_sk_...`):

| Language | Directory | Needs |
|---|---|---|
| TypeScript | [`sdks/server/typescript`](../sdks/server/typescript) | Node.js 18 or later, no runtime dependency (global `fetch`) |
| Python | [`sdks/server/python`](../sdks/server/python) | Python 3.9 or later, standard library only |

Both are AGPL-3.0-only, like the server, and neither is published to a package registry:
install them from a clone of this repository.

They are generated from [`api/openapi.yaml`](../api/openapi.yaml) by `internal/sdkgen`, so
they cover the API exactly: one method per operation, named after its `operationId`
(`listDaily` in TypeScript, `list_daily` in Python), with typed parameters, bodies and results.
A Go test regenerates them and fails when the committed files differ, so they cannot drift
from the document. The webhook verification helpers are the only hand-written part.

Left out on purpose: the routes providers call (`/oauth/{provider}/callback`,
`/inbound/{provider}`), `/mcp` (AI agents speak MCP to it, see [mcp.md](mcp.md)), `/metrics`
and `/openapi.yaml`. The liveness and readiness checks are there (`healthz`, `readyz`).

## Install

TypeScript, built once, then installed by path:

```sh
cd open-health-sync/sdks/server/typescript
npm ci && npm run build
cd /path/to/your/app
npm install /path/to/open-health-sync/sdks/server/typescript
```

You can also copy `src/client.ts`, `src/types.ts` and `src/webhooks.ts` into your code base;
they import only each other and `node:crypto`.

Python:

```sh
pip install /path/to/open-health-sync/sdks/server/python
```

pip also installs from the Git repository directly, with `#subdirectory=sdks/server/python`.

## Create a client

```ts
import { OpenHealthSync } from "@open-health-sync/server";

const ohs = new OpenHealthSync({
  baseUrl: "https://health.example.com",
  apiKey: process.env.OHS_API_KEY!,
});
```

```python
import os
from open_health_sync import OpenHealthSync

ohs = OpenHealthSync("https://health.example.com", os.environ["OHS_API_KEY"])
```

`apiKey` / `api_key` is sent as `Authorization: Bearer`. The user routes (the Apple Health and
Health Connect uploads) need a user token instead: make a second client with the token from
`mintUserToken` / `mint_user_token`. In TypeScript, pass `fetch` to use another implementation
(a mock in tests, a proxy agent); in Python, `timeout` sets the per-request timeout in seconds.

## List users

Every cursor list has an iterator that follows `next_cursor` until it is null:

```ts
for await (const user of ohs.iterUsers()) {
  console.log(user.id, user.external_ref);
}
```

```python
for user in ohs.iter_users():
    print(user["id"], user["external_ref"])
```

To work page by page, call the list method itself (`listUsers({ cursor, limit })`,
`list_users(cursor=..., limit=...)`) and pass `next_cursor` back as `cursor`.

`createUser` / `create_user` takes an idempotency key, the one route that honours it:

```ts
const user = await ohs.createUser({ external_ref: "customer-42" }, { idempotencyKey: "signup-42" });
```

```python
user = ohs.create_user({"external_ref": "customer-42"}, idempotency_key="signup-42")
```

## Connect a provider

```ts
const { authorize_url } = await ohs.startConnection(user.id, "withings", {
  redirect_uri: "https://app.example.com/health/connected",
});
// Send the user to authorize_url.
```

```python
consent = ohs.start_connection(user["id"], "withings", {"redirect_uri": "https://app.example.com/health/connected"})
# Send the user to consent["authorize_url"].
```

See [connecting a provider](api.md#connecting-a-provider) for what happens next.

## Read daily summaries

```ts
for await (const day of ohs.iterDaily(user.id, { start: "2026-01-01", end: "2026-02-01" })) {
  console.log(day.date, day.provider, day.step_count, day.resting_hr);
}
```

```python
for day in ohs.iter_daily(user["id"], start="2026-01-01", end="2026-02-01"):
    print(day["date"], day["provider"], day.get("step_count"), day.get("resting_hr"))
```

The other reads work the same way: `iterSamples(userId, { type: "heart_rate" })`,
`iterSleep`, `iterWorkouts`, `getCoverage`, `searchFHIRObservations` and so on. Values are in
the canonical units of the [data model](data-model.md). `exportDataset` returns the `Response`
in TypeScript, so a large CSV or Parquet file can be streamed; the Python method returns the
bytes.

## Errors

An answer outside 2xx is thrown (raised) as `OhsError`, read from the
`application/problem+json` body: `status`, `title`, `detail` and the request id
(`requestId` / `request_id`, from `X-Request-Id`), to quote when reporting a problem.

```ts
import { OhsError } from "@open-health-sync/server";

try {
  await ohs.getUser(id);
} catch (err) {
  if (err instanceof OhsError && err.status === 404) {
    // not this tenant's user, or deleted
  } else {
    throw err;
  }
}
```

```python
from open_health_sync import OhsError

try:
    ohs.get_user(user_id)
except OhsError as err:
    if err.status != 404:
        raise
```

The clients do not retry. Retry a 503 with `Retry-After`, or a network error, yourself; use an
idempotency key when retrying `createUser`.

## Verify a webhook

`verifyWebhook` / `verify_webhook` checks the Standard Webhooks signature, the timestamp
(five minutes of tolerance) and returns the parsed event, or throws
`WebhookVerificationError`. Give it the raw body as received, not a body parsed and serialized
again. Both are tested against vectors signed by the server's own code
(`sdks/server/testdata/webhook-vectors.json`). See [webhooks.md](webhooks.md) for the events.

```ts
import express from "express";
import { verifyWebhook, WebhookVerificationError } from "@open-health-sync/server";

const app = express();
app.post("/hooks/health", express.raw({ type: "application/json" }), (req, res) => {
  try {
    const event = verifyWebhook(process.env.OHS_WEBHOOK_SECRET!, req.headers, req.body);
    // Deduplicate on event.id, queue the work, answer fast.
    res.sendStatus(204);
  } catch (err) {
    if (err instanceof WebhookVerificationError) return res.sendStatus(400);
    throw err;
  }
});
```

```python
import os
from flask import Flask, request
from open_health_sync import WebhookVerificationError, verify_webhook

app = Flask(__name__)

@app.post("/hooks/health")
def health_hook():
    try:
        event = verify_webhook(os.environ["OHS_WEBHOOK_SECRET"], request.headers, request.get_data())
    except WebhookVerificationError:
        return "", 400
    # Deduplicate on event["id"], queue the work, answer fast.
    return "", 204
```

Express and Flask are only examples; the helpers take any header map and body bytes.

## Regenerate

After a change to `api/openapi.yaml`, from the repository root:

```sh
make sdk        # the SDKs, the Postman collection and the webhook vectors
make sdk-test   # their tests, as CI runs them (Node.js 22, Python 3)
```

`go test ./internal/sdkgen/` fails until the regenerated files are committed. The generator
is plain Go on [kin-openapi](https://github.com/getkin/kin-openapi), the library the contract
tests already use. The same run writes the API collection described in
[api-collections.md](api-collections.md).
