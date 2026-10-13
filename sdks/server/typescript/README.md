# Open Health Sync server SDK for TypeScript

A client of the Open Health Sync API for your backend, on Node.js 18 or later. It uses the
global `fetch` and has no runtime dependency. Licensed AGPL-3.0-only, like the rest of the
repository. Not published to npm.

- `src/client.ts` and `src/types.ts` are **generated** from `api/openapi.yaml` by
  `internal/sdkgen`. Do not edit them: change the OpenAPI document and run `make sdk` from the
  repository root. A Go test fails when they are out of date.
- `src/webhooks.ts` (webhook signature verification) is written by hand.

## Install

From a clone of the repository:

```sh
cd sdks/server/typescript
npm ci && npm run build          # writes dist/
cd /path/to/your/app
npm install /path/to/open-health-sync/sdks/server/typescript
```

Or copy `src/client.ts`, `src/types.ts` and `src/webhooks.ts` into your project: they import
nothing but each other and `node:crypto`.

## Use

```ts
import { OpenHealthSync, OhsError } from "@open-health-sync/server";

const ohs = new OpenHealthSync({ baseUrl: "https://health.example.com", apiKey: process.env.OHS_API_KEY! });

for await (const user of ohs.iterUsers()) console.log(user.id, user.external_ref);

const { authorize_url } = await ohs.startConnection(userId, "withings", { redirect_uri: "https://app.example.com/done" });

for await (const day of ohs.iterDaily(userId, { start: "2026-01-01", end: "2026-02-01" })) {
  console.log(day.date, day.step_count);
}
```

Every operation of the API is a method named after its `operationId`; every cursor list also
has an `iter...` method that follows `next_cursor`. Errors are thrown as `OhsError` (`status`,
`title`, `detail`, `requestId`). See [docs/server-sdks.md](../../../docs/server-sdks.md).

## Test

```sh
npm ci
npm run check   # tsc, no emit
npm test        # node --test on a local HTTP server, and the webhook vectors
```
