// SPDX-License-Identifier: AGPL-3.0-only
//
// Where the app says it is reached: APP_URL, then a Vercel deployment's own
// address, then this machine.

import assert from "node:assert/strict";
import { test } from "node:test";
import { readConfig } from "../lib/config.ts";

const base = { OHS_URL: "https://ohs.example.org/", OHS_API_KEY: "ohs_sk_test" };

test("APP_URL wins", () => {
  const { config } = readConfig({ ...base, APP_URL: "https://app.example.org/", VERCEL_PROJECT_PRODUCTION_URL: "x.vercel.app" });
  assert.equal(config?.appUrl, "https://app.example.org");
});

test("a Vercel deployment uses its production address", () => {
  const { config } = readConfig({ ...base, VERCEL_PROJECT_PRODUCTION_URL: "my-health-app.vercel.app" });
  assert.equal(config?.appUrl, "https://my-health-app.vercel.app");
});

test("this machine otherwise", () => {
  const { config } = readConfig(base);
  assert.equal(config?.appUrl, "http://localhost:3000");
  assert.equal(config?.ohsUrl, "https://ohs.example.org");
});
