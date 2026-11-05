# React Native SDK

`open-health-sync-react-native` is a TypeScript client for the [REST API](../api.md). It runs in
React Native 0.72+ (either of its JavaScript engines), Expo, Node 18+ and browsers. It calls the global
`fetch`, depends on nothing but `fflate` (gzip), and has no React dependency, so the same code
works in an app, in a Node backend and in tests. The source is in
[`sdks/react-native`](../../sdks/react-native). It is AGPL-3.0-only, like the server
([LICENSE](../../LICENSE)).

What all the SDKs share (routes, paging, errors, the part builder, which credential goes where)
is in the [SDK overview](README.md). This page covers what is specific to TypeScript.

## Install

Nothing is published yet. Build it from this repository and install it from the folder:

```sh
git clone https://github.com/soma-intelligence/open-health-sync.git
cd open-health-sync/sdks/react-native
npm install && npm run build
cd /path/to/your/app
npm install /path/to/open-health-sync/sdks/react-native
```

`package.json` has a `react-native` field and export condition pointing at `src/index.ts`, so
Metro bundles the TypeScript source directly; Node and web bundlers use the ESM build in
`dist/` with its type declarations. With a local folder install, Metro may need the folder in
`watchFolders` of `metro.config.js`.

## Create a client

```ts
import { OhsClient } from "open-health-sync-react-native";

const client = new OhsClient({
  baseUrl: "https://ohs.example.org",
  // Called before every request: ask your backend for an upload-scope user token,
  // cache it until it expires.
  tokenProvider: async () => {
    const res = await fetch("https://api.your-app.example/ohs-token", { credentials: "include" });
    const { token } = await res.json();
    return token;
  },
});
```

On your backend, give `token: process.env.OHS_KEY` (the tenant key) instead. Options:

| Option | Default | |
|---|---|---|
| `baseUrl` | required | Base URL of the instance |
| `token` or `tokenProvider` | one is required | Bearer credential, fixed or fetched before each request |
| `fetch` | global `fetch` | Any `fetch` compatible function; tests pass a fake |
| `userAgent` | `open-health-sync-react-native/0.1.0` | Browsers may drop this header |
| `sleep` | `setTimeout` | Wait between upload retries; tests pass a fake |
| `maxUploadAttempts` | 5 | Attempts per upload part, retries included |

**Which credential.** On the REST API a user token (`ohs_ut_`) can call one route only:
`POST /v1/ingest/apple-health`, with the `upload` scope. Every other route (users, tokens,
reads, coverage, freshness, FHIR, connections and `startConnection`) needs the tenant key and
answers 403 to a user token. So the work splits in two:

- **Your backend** holds the tenant key and runs this same package under Node: it creates users,
  mints upload tokens, reads data, polls coverage and starts connections.
- **The app** holds only an upload-scope user token, minted by your backend, and uploads parts
  with it. It gets data and authorize URLs from your backend, through your own API.

The read and connection sections below therefore show backend code.

## Read data

Reads run on your backend, with the tenant key. Every list method returns one page,
`{ data, nextCursor }`. Pass `nextCursor` back as `cursor`,
or let `paginate` walk the pages:

```ts
import { OhsClient, paginate, parseTimestamp } from "open-health-sync-react-native";

const backend = new OhsClient({ baseUrl: "https://ohs.example.org", token: process.env.OHS_KEY! });

const first = await backend.listSamples(userId, { type: "heart_rate", start: "2026-01-10", limit: 500 });

for await (const s of paginate((cursor) =>
  backend.listSamples(userId, { type: "heart_rate", start: "2026-01-10", cursor }),
)) {
  console.log(s.id, parseTimestamp(s.startDate));
}
```

`collectAll(fetchPage)` gathers every record into an array. `start` and `end` take an RFC 3339
or `YYYY-MM-DD` string, sent as is, or a `Date`, sent as RFC 3339 UTC.

| Method | Route |
|---|---|
| `listProviders()` | `GET /v1/providers` |
| `createUser({ externalRef?, idempotencyKey? })`, `listUsers({ cursor?, limit? })`, `getUser(id)` | users |
| `deleteUser(id)` (returns the job id), `getDeletion(jobId)` | user deletion |
| `mintUserToken(id, scopes, ttlSeconds?)` | `POST /v1/users/{id}/tokens` |
| `listConnections(id)`, `getFreshness(id)`, `startConnection(id, provider, redirectUri?)`, `deleteConnection(id, provider)` | connections |
| `listSamples(id, { type, ...range })`, `listCategorySamples(id, { type?, ...range })` | samples |
| `listSleep(id, range)`, `listSleepPhases(id, sessionId, range)` | sleep |
| `listWorkouts(id, { type?, ...range })`, `getWorkoutRoute(id, workoutId, range)` | workouts |
| `listNutrition(id, range)`, `listDaily(id, range)` | nutrition, daily summaries |
| `getCoverage(id, { provider?, start?, end? })` | coverage |
| `searchFhirObservations(id, { type?, code?, category?, ...range })`, `readFhirObservation(id, observationId)` | FHIR R4 |
| `uploadAppleHealthPart(part)`, `uploadParts(parts)` | `POST /v1/ingest/apple-health` |

Records have the fields of the OpenAPI schemas in camelCase (`start_date` becomes `startDate`).
`metadata`, `nutrients` and coverage `metrics` are kept as the server sent them, keys
included. Fields the server adds later are left out, not an error.

**Timestamps stay strings.** Time fields are RFC 3339 strings as the server writes them, up to
nine fractional digits. A `Date` holds milliseconds only, and React Native's default engine does not parse more than
three fractional digits reliably, so nothing is converted for you. `parseTimestamp(s)` trims
the fraction to milliseconds and returns a `Date`. `date` fields (daily summaries, coverage)
are `YYYY-MM-DD` strings.

**Ids** are int64 on the server and `number` here, exact up to 2^53 - 1.

**FHIR.** `searchFhirObservations` returns `{ raw, observations, nextCursor }`: the Bundle as
sent, its Observation resources, and the `cursor` of its `next` link (null on the last page).
It sends `Accept: application/fhir+json`. See [FHIR](../fhir.md) for the mapping.

## Connect a provider

Your backend starts the connection with the tenant key and returns the URL to the app through
your own API; the app opens it in the browser:

```ts
// On your backend (tenant key)
const url = await backend.startConnection(userId, "oura", "myapp://ohs/connected");

// In the app, with the URL your backend returned
import { Linking } from "react-native";
await Linking.openURL(url);
```

After consent the browser lands on `myapp://ohs/connected?status=connected&provider=oura` (or
`status=error`). Without a `redirectUri` the server shows its own result page.
`listConnections` and `getFreshness` tell how each connection is doing.

## Upload

The upload route takes one gzip NDJSON part per call
([contract](../connectors/apple-health.md#upload-contract)). Each line is a JSON object in the
[line format](../connectors/apple-health.md#line-formats). `AppleHealthPartBuilder` groups lines
into parts by type and local day, splits them at 8 MiB of NDJSON, numbers them across the
batch and gzips them:

```ts
import { AppleHealthPartBuilder, localDay, newBatchId } from "open-health-sync-react-native";

const builder = new AppleHealthPartBuilder(newBatchId("app"));
const start = new Date("2026-01-10T14:30:00Z");
builder.add("HKQuantityTypeIdentifierHeartRate", localDay(start, "Europe/Paris"), {
  quantityType: "HKQuantityTypeIdentifierHeartRate",
  startDate: start.toISOString(),
  endDate: start.toISOString(),
  value: 72,
  unit: "count/min",
  metadata: { HKTimeZone: "Europe/Paris" },
});

// In the app, with the upload-token client from "Create a client"
const results = await client.uploadParts(builder.build()); // [{ key, duplicate }]
```

- `localDay(instant, timeZone)` gives the `YYYY-MM-DD` day in an IANA zone. Use the zone you put
  in `HKTimeZone`, so the part's day matches the day the server files the sample under.
- `newBatchId(prefix)` gives `prefix-<UTC time>-<10 random characters>`. Use a new one per run;
  re-sending the same parts with the same batch is safe (`duplicate: true`).
- `build()` returns parts with `type`, `day`, `batch`, `partNumber`, `body` (gzip bytes, a
  `Uint8Array`), `lineCount` and `uncompressedBytes`. Invalid batch, type or day throws a
  `RangeError`.

Processing is asynchronous. Your backend polls `getCoverage(userId)` until `pendingJobs` is 0,
then `rejectedRecords` above 0 means some lines were not stored.

## Reading HealthKit or Health Connect in React Native

The SDK does not read the device's health store. A community module does that (for example
`react-native-health` for HealthKit, or a Health Connect module on Android), and you turn each
sample into a line in the [line format](../connectors/apple-health.md#line-formats). The
upload route currently takes HealthKit-shaped lines only: a Health Connect reading has to be
written with the matching HealthKit identifier and unit, and is stored under the `apple_health`
provider.

A sketch with `react-native-health`, inside a component (documentation only; the SDK does not
depend on that module):

```tsx
import AppleHealthKit, { type HealthValue } from "react-native-health";
import { AppleHealthPartBuilder, localDay, newBatchId, type OhsClient } from "open-health-sync-react-native";

const HR = "HKQuantityTypeIdentifierHeartRate";

function uploadHeartRate(client: OhsClient, since: Date): Promise<void> {
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return new Promise((resolve, reject) => {
    AppleHealthKit.getHeartRateSamples(
      { startDate: since.toISOString(), ascending: true },
      async (err: string, samples: HealthValue[]) => {
        if (err) return reject(new Error("HealthKit query failed"));
        const builder = new AppleHealthPartBuilder(newBatchId("app"));
        for (const s of samples) {
          builder.add(HR, localDay(new Date(s.startDate), tz), {
            quantityType: HR,
            startDate: new Date(s.startDate).toISOString(),
            endDate: new Date(s.endDate).toISOString(),
            value: s.value,
            unit: "count/min",
            metadata: { HKTimeZone: tz },
          });
        }
        try {
          await client.uploadParts(builder.build());
          resolve();
        } catch (e) {
          reject(e);
        }
      },
    );
  });
}

// In a component: useEffect(() => { uploadHeartRate(client, lastRun).catch(report); }, []);
```

The device's current zone is a guess for older samples; when the module gives the sample's own
zone, use that. Keep the last successful run time on the device and pass it as `since` next
time.

## Errors and retries

A non-2xx answer throws `OhsApiError` with `status`, `title`, `detail`, `type`, `requestId`
(the body's `request_id`, else the `X-Request-Id` header) and `retryAfterSeconds`. A body that
is not JSON still gives the status. Quote `requestId` in a bug report. The message never holds
the token or a health value.

```ts
import { OhsApiError } from "open-health-sync-react-native";

try {
  await backend.getUser(userId);
} catch (e) {
  if (e instanceof OhsApiError && e.status === 404) {
    // gone, or not this tenant's
  } else {
    throw e;
  }
}
```

`uploadAppleHealthPart` retries 429, 503, other 5xx answers and network errors: it waits
`Retry-After` seconds when sent, else 1 s doubling up to 30 s, and stops after 5 attempts in
all. Any other 4xx throws at once (a 401 means the token expired: the token provider is called
again on the next request). Other methods do not retry. A 2xx answer of an unexpected shape
throws `OhsDecodeError`.

## Run the tests

Node 22 or later:

```sh
cd sdks/react-native
npm install
npm run lint   # tsc --noEmit, strict, source and tests
npm test       # compiles to dist-test/ and runs node:test
npm run build  # ESM and declarations in dist/
```

The tests use a fake `fetch`: they check methods, paths, query strings and headers, decode a
fixture of every record type, map errors, exercise the retries, walk pages, and gunzip the
built parts.

## Run the example

[`example/quickstart.ts`](../../sdks/react-native/example/quickstart.ts) runs against a live
instance with a tenant key. It creates a user, mints a user token, uploads one heart rate line
with it, polls coverage until `pendingJobs` is 0 (at most 120 s), lists the user's `heart_rate`
samples and exits 1 unless there is exactly one. It prints no token and no health value.

```sh
cd sdks/react-native
OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... npm run example
```
