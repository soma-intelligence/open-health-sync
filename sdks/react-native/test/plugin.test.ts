// SPDX-License-Identifier: AGPL-3.0-only

import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  ANDROID_READ_PERMISSIONS,
  androidPermissions,
  applyAndroidManifest,
  applyEntitlements,
  applyGradleProperties,
  applyInfoPlist,
} from "../plugin/config.js";
import { supportedTypes } from "../src/health/types.ts";

const P = "android.permission.health.";

function manifest() {
  return {
    manifest: {
      "uses-permission": [{ $: { "android:name": "android.permission.INTERNET" } }],
      application: [
        {
          activity: [
            {
              $: { "android:name": ".MainActivity" },
              "intent-filter": [{ action: [{ $: { "android:name": "android.intent.action.MAIN" } }] }],
            },
          ],
        },
      ],
    } as Record<string, any>,
  };
}

describe("Expo config plugin", () => {
  it("knows the read permission of every type Android has, and of no other", () => {
    assert.deepEqual(Object.keys(ANDROID_READ_PERMISSIONS).sort(), supportedTypes("android").sort());
    const permissions = Object.values(ANDROID_READ_PERMISSIONS);
    assert.equal(new Set(permissions).size, permissions.length);
  });

  it("lists the permissions of the chosen types", () => {
    assert.deepEqual(androidPermissions({ types: ["steps", "sleep", "heartRhythm", "steps"], history: true }), [
      `${P}READ_STEPS`,
      `${P}READ_SLEEP`,
      `${P}READ_HEALTH_DATA_IN_BACKGROUND`,
      `${P}READ_HEALTH_DATA_HISTORY`,
    ]);
    assert.deepEqual(androidPermissions({ types: ["weight"], backgroundRead: false }), [`${P}READ_WEIGHT`]);
    // Every type, and background reads, by default.
    assert.equal(androidPermissions().length, 23);
  });

  it("sets the HealthKit usage sentence and keeps one already there", () => {
    assert.equal(applyInfoPlist({}, { healthShareUsageDescription: "Why" }).NSHealthShareUsageDescription, "Why");
    assert.equal(applyInfoPlist({ NSHealthShareUsageDescription: "Mine" }).NSHealthShareUsageDescription, "Mine");
    assert.match(String(applyInfoPlist({}).NSHealthShareUsageDescription), /health data/);
  });

  it("adds the HealthKit entitlements", () => {
    assert.deepEqual(applyEntitlements({}), {
      "com.apple.developer.healthkit": true,
      "com.apple.developer.healthkit.background-delivery": true,
    });
    assert.deepEqual(applyEntitlements({}, { backgroundDelivery: false }), { "com.apple.developer.healthkit": true });
  });

  it("declares permissions, the rationale filter and the usage alias, once", () => {
    const m = manifest();
    applyAndroidManifest(m, { types: ["steps"] });
    applyAndroidManifest(m, { types: ["steps"] });

    assert.deepEqual(
      m.manifest["uses-permission"].map((p: any) => p.$["android:name"]),
      ["android.permission.INTERNET", `${P}READ_STEPS`, `${P}READ_HEALTH_DATA_IN_BACKGROUND`],
    );
    const app = m.manifest.application[0];
    const actions = app.activity[0]["intent-filter"].map((f: any) => f.action[0].$["android:name"]);
    assert.deepEqual(actions, ["android.intent.action.MAIN", "androidx.health.ACTION_SHOW_PERMISSIONS_RATIONALE"]);
    assert.equal(app["activity-alias"].length, 1);
    const alias = app["activity-alias"][0];
    assert.equal(alias.$["android:targetActivity"], ".MainActivity");
    assert.equal(alias.$["android:permission"], "android.permission.START_VIEW_PERMISSION_USAGE");
    assert.equal(alias["intent-filter"][0].category[0].$["android:name"], "android.intent.category.HEALTH_PERMISSIONS");
  });

  it("raises minSdk to 26 and leaves a higher one alone", () => {
    assert.deepEqual(applyGradleProperties([]), [{ type: "property", key: "android.minSdkVersion", value: "26" }]);
    assert.equal(applyGradleProperties([{ type: "property", key: "android.minSdkVersion", value: "24" }])[0]?.value, "26");
    assert.equal(applyGradleProperties([{ type: "property", key: "android.minSdkVersion", value: "29" }])[0]?.value, "29");
  });
});
