// SPDX-License-Identifier: MIT

// Quickstart against a running Open Health Sync instance.
//
//   OHS_URL=http://localhost:8080 OHS_KEY=ohs_sk_... npm run example
//
// Creates a user, mints a user token, uploads one heart rate line with it, waits for the
// server to process the part, then reads the sample back. Exits 1 unless exactly one
// heart_rate sample is stored. Prints no token and no health value.

import {
  AppleHealthPartBuilder,
  OhsApiError,
  OhsClient,
  collectAll,
  localDay,
  newBatchId,
} from "../src/index.ts";

const HEART_RATE = "HKQuantityTypeIdentifierHeartRate";

function env(name: string): string {
  const v = process.env[name];
  if (!v) {
    console.error(`${name} is not set`);
    process.exit(2);
  }
  return v;
}

async function main(): Promise<number> {
  const baseUrl = env("OHS_URL");
  const tenantKey = env("OHS_KEY");

  // Server side: the tenant key creates the user and mints a short-lived user token.
  const server = new OhsClient({ baseUrl, token: tenantKey });
  const user = await server.createUser();
  console.log(`created user ${user.id}`);
  const userToken = await server.mintUserToken(user.id, ["upload"]);
  console.log(`minted a user token with scopes ${userToken.scopes.join(",")}`);

  // App side: the user token uploads.
  const app = new OhsClient({ baseUrl, tokenProvider: async () => userToken.token });
  const now = new Date();
  const ts = now.toISOString();
  const parts = new AppleHealthPartBuilder(newBatchId("rn-quickstart"))
    .add(HEART_RATE, localDay(now, "UTC"), {
      quantityType: HEART_RATE,
      startDate: ts,
      endDate: ts,
      value: 72,
      unit: "count/min",
      metadata: { HKTimeZone: "UTC" },
    })
    .build();
  const results = await app.uploadParts(parts);
  console.log(`uploaded ${results.length} part(s)`);

  // Server side again: wait until every part is processed.
  const deadline = Date.now() + 120_000;
  for (;;) {
    const coverage = await server.getCoverage(user.id);
    if (coverage.pendingJobs === 0) {
      if (coverage.rejectedRecords > 0) console.log(`rejected records: ${coverage.rejectedRecords}`);
      break;
    }
    if (Date.now() > deadline) {
      console.error("timed out waiting for pending jobs");
      return 1;
    }
    await new Promise((r) => setTimeout(r, 1000));
  }

  const samples = await collectAll((cursor) =>
    server.listSamples(user.id, { type: "heart_rate", cursor }),
  );
  console.log(`heart_rate samples: ${samples.length}`);
  return samples.length === 1 ? 0 : 1;
}

main().then(
  (code) => process.exit(code),
  (err: unknown) => {
    if (err instanceof OhsApiError) {
      console.error(err.message);
    } else {
      console.error(err instanceof Error ? err.message : "unexpected error");
    }
    process.exit(1);
  },
);
