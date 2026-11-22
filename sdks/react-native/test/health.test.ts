// SPDX-License-Identifier: AGPL-3.0-only

import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { type BackgroundSyncEvent, HealthSyncError, decodeSyncReport } from "../src/health/report.ts";
import { createHealthSync } from "../src/health/sync.ts";
import {
  ALL_HEALTH_DATA_TYPES,
  HEALTH_DATA_TYPES,
  type HealthDataType,
  dataTypesOfNativeName,
  isTypeSupported,
  nativeTypeNames,
  supportedTypes,
} from "../src/health/types.ts";
import { fakeNative, nativeError } from "./fake-native.ts";

const HR = "HKQuantityTypeIdentifierHeartRate";
const TOKEN = "ohs_ut_test_token";

function rejectsWith(code: string) {
  return (e: unknown) => {
    assert.ok(e instanceof HealthSyncError, `expected a HealthSyncError, got ${String(e)}`);
    assert.equal(e.code, code);
    return true;
  };
}

describe("health data types", () => {
  it("names every type once, with a source on at least one platform", () => {
    assert.equal(new Set(ALL_HEALTH_DATA_TYPES).size, ALL_HEALTH_DATA_TYPES.length);
    for (const type of ALL_HEALTH_DATA_TYPES) {
      const s = HEALTH_DATA_TYPES[type];
      assert.ok(s.ios !== null || s.android !== null, type);
      if (s.ios !== null) assert.ok(s.ios.length > 0, type);
    }
  });

  it("gives each Health Connect record type to one data type only", () => {
    const seen = new Map<string, string>();
    for (const type of supportedTypes("android")) {
      const name = HEALTH_DATA_TYPES[type].android as string;
      assert.equal(seen.get(name), undefined, `${name} is named by ${seen.get(name)} and ${type}`);
      seen.set(name, type);
    }
    // The 22 record types of the Kotlin SDK's HealthConnectRecordType.
    assert.equal(seen.size, 22);
  });

  it("uses HealthKit identifiers or a group the Swift SDK expands on iOS", () => {
    for (const type of supportedTypes("ios")) {
      for (const name of HEALTH_DATA_TYPES[type].ios as readonly string[]) {
        assert.match(name, /^(HK[A-Za-z0-9]+|@nutrition|@workouts|@heartRhythm)$/, `${type}: ${name}`);
      }
    }
  });

  it("tells what each platform supports", () => {
    assert.equal(isTypeSupported("steps", "ios"), true);
    assert.equal(isTypeSupported("steps", "android"), true);
    assert.equal(isTypeSupported("totalEnergy", "ios"), false);
    assert.equal(isTypeSupported("heartRhythm", "android"), false);
    assert.ok(supportedTypes("ios").includes("stateOfMind"));
    assert.ok(!supportedTypes("android").includes("stateOfMind"));
  });

  it("resolves names per platform, drops what the platform lacks, and removes repeats", () => {
    const types: HealthDataType[] = ["heartRate", "bloodPressure", "totalEnergy", "heartRate", "heartRhythm"];
    assert.deepEqual(nativeTypeNames(types, "ios"), [
      HR,
      "HKQuantityTypeIdentifierBloodPressureSystolic",
      "HKQuantityTypeIdentifierBloodPressureDiastolic",
      "@heartRhythm",
    ]);
    assert.deepEqual(nativeTypeNames(types, "android"), ["HeartRate", "BloodPressure", "TotalCaloriesBurned"]);
    assert.throws(() => nativeTypeNames(["nope" as HealthDataType], "ios"), RangeError);
  });

  it("maps a native name back to its data types", () => {
    assert.deepEqual(dataTypesOfNativeName("Weight", "android"), ["weight"]);
    assert.deepEqual(dataTypesOfNativeName("HKQuantityTypeIdentifierBloodPressureDiastolic", "ios"), ["bloodPressure"]);
    assert.deepEqual(dataTypesOfNativeName("Vo2Max", "android"), []);
  });
});

describe("healthSync without the native module", () => {
  for (const [name, deps] of [
    ["module missing", { native: null, platform: "ios" }],
    ["platform without a health store", { native: fakeNative().native, platform: "web" }],
  ] as const) {
    it(`${name}: is not available and rejects with not_linked`, async () => {
      const health = createHealthSync(deps);
      assert.equal(health.platform, null);
      assert.equal(await health.isAvailable(), false);
      assert.equal(await health.availability(), "unavailable");
      await assert.rejects(health.sync(), rejectsWith("not_linked"));
      await assert.rejects(health.configure({ baseUrl: "https://ohs.example.org", userToken: TOKEN }), rejectsWith("not_linked"));
      await assert.rejects(health.pendingParts(), rejectsWith("not_linked"));
      assert.throws(() => health.addBackgroundSyncListener(() => {}), rejectsWith("not_linked"));
    });
  }
});

describe("availability", () => {
  it("passes the native answer through", async () => {
    for (const a of ["available", "providerUpdateRequired", "unavailable"] as const) {
      const health = createHealthSync({ native: fakeNative({ availability: a }).native, platform: "android" });
      assert.equal(await health.availability(), a);
      assert.equal(await health.isAvailable(), a === "available");
    }
  });

  it("never rejects", async () => {
    const f = fakeNative({ availability: nativeError("health_store", "boom") });
    const health = createHealthSync({ native: f.native, platform: "ios" });
    assert.equal(await health.isAvailable(), false);
    const odd = createHealthSync({ native: fakeNative({ availability: "maybe" }).native, platform: "ios" });
    assert.equal(await odd.availability(), "unavailable");
  });
});

describe("configure", () => {
  it("hands a trimmed base URL and the token to the native side", async () => {
    const f = fakeNative();
    const health = createHealthSync({ native: f.native, platform: "ios" });
    await health.configure({ baseUrl: " https://ohs.example.org/ ", userToken: ` ${TOKEN} ` });
    assert.deepEqual(f.calls, [{ method: "configure", args: ["https://ohs.example.org", TOKEN] }]);
  });

  it("refuses a bad URL, an empty token and a tenant key, without calling the native side", async () => {
    const f = fakeNative();
    const health = createHealthSync({ native: f.native, platform: "android" });
    for (const credentials of [
      { baseUrl: "ohs.example.org", userToken: TOKEN },
      { baseUrl: "ftp://ohs.example.org", userToken: TOKEN },
      { baseUrl: "https://ohs.example.org", userToken: "  " },
      { baseUrl: "https://ohs.example.org", userToken: "ohs_sk_secret_tenant_key" },
    ]) {
      await assert.rejects(health.configure(credentials), rejectsWith("invalid_argument"));
    }
    assert.equal(f.calls.length, 0);
  });

  it("never puts the token in an error message", async () => {
    const health = createHealthSync({ native: fakeNative().native, platform: "ios" });
    const secret = "ohs_sk_secret_tenant_key";
    await assert.rejects(health.configure({ baseUrl: "https://ohs.example.org", userToken: secret }), (e: unknown) => {
      assert.ok(e instanceof Error);
      assert.ok(!e.message.includes(secret));
      return true;
    });
  });

  it("reports and clears the kept credentials", async () => {
    const f = fakeNative({ isConfigured: false });
    const health = createHealthSync({ native: f.native, platform: "ios" });
    assert.equal(await health.isConfigured(), false);
    await health.clearCredentials();
    assert.deepEqual(
      f.calls.map((c) => c.method),
      ["isConfigured", "clearCredentials"],
    );
  });
});

describe("requestAuthorization", () => {
  it("asks for the platform's names and reports what Android granted", async () => {
    const f = fakeNative({
      requestAuthorization: JSON.stringify({ granted: ["Steps", "Weight"], background: true, history: false }),
    });
    const health = createHealthSync({ native: f.native, platform: "android" });
    const result = await health.requestAuthorization(["steps", "heartRate", "heartRhythm"], { background: true, history: true });
    assert.deepEqual(f.calls, [{ method: "requestAuthorization", args: [["Steps", "HeartRate"], true, true] }]);
    assert.deepEqual(result, {
      platform: "android",
      requested: ["steps", "heartRate"],
      // weight was granted earlier but not asked for now: it is not in the answer.
      granted: ["steps"],
      backgroundGranted: true,
      historyGranted: false,
    });
  });

  it("asks for every type when none is given, and knows nothing of grants on iOS", async () => {
    const f = fakeNative({ requestAuthorization: JSON.stringify({ granted: null }) });
    const health = createHealthSync({ native: f.native, platform: "ios" });
    const result = await health.requestAuthorization();
    assert.deepEqual(f.calls, [{ method: "requestAuthorization", args: [["*"], false, false] }]);
    assert.deepEqual(result.requested, supportedTypes("ios"));
    assert.equal(result.granted, null);
    assert.equal(result.backgroundGranted, null);
    assert.equal(result.historyGranted, null);
  });

  it("maps native rejections to their code", async () => {
    const f = fakeNative({ requestAuthorization: nativeError("no_activity", "no activity in front") });
    const health = createHealthSync({ native: f.native, platform: "android" });
    await assert.rejects(health.requestAuthorization(["steps"]), rejectsWith("no_activity"));
  });
});

describe("sync", () => {
  const androidReport = {
    batchIds: ["android-20260110T143000Z-abcdefghij"],
    partsUploaded: 3,
    duplicateParts: 1,
    partsPending: 2,
    partsDropped: 1,
    linesByType: { Steps: 40, HeartRate: 900 },
    deletionsByType: { Steps: 2 },
    skippedRecords: 4,
    typesWithoutPermission: ["Weight", "Vo2Max"],
    expiredTokenTypes: ["HeartRate"],
    uploadError: { code: "unauthorized", message: "Open Health Sync API error 401", status: 401, requestId: "req_1" },
  };

  it("passes the names and decodes an Android report", async () => {
    const f = fakeNative({ sync: JSON.stringify(androidReport) });
    const health = createHealthSync({ native: f.native, platform: "android" });
    const report = await health.sync(["steps", "heartRate", "weight"]);
    assert.deepEqual(f.calls, [{ method: "sync", args: [["Steps", "HeartRate", "Weight"]] }]);
    assert.deepEqual(report, {
      platform: "android",
      batchIds: androidReport.batchIds,
      partsUploaded: 3,
      duplicateParts: 1,
      partsPending: 2,
      partsDropped: 1,
      linesByType: { Steps: 40, HeartRate: 900 },
      deletionsByType: { Steps: 2 },
      deletionsPendingByType: {},
      skippedRecords: 4,
      // A record type this package has no word for is left out.
      typesWithoutPermission: ["weight"],
      expiredChangesTokenTypes: ["heartRate"],
      pendingTypes: [],
      unavailableTypes: [],
      uploadError: { code: "unauthorized", message: "Open Health Sync API error 401", status: 401, requestId: "req_1" },
      complete: false,
    });
  });

  it("decodes an iOS report and says when nothing is left behind", async () => {
    const f = fakeNative({
      sync: JSON.stringify({
        batchIds: ["ios-1"],
        partsUploaded: 1,
        linesByType: { [HR]: 12 },
        deletionsPendingByType: { [HR]: 1 },
        unavailableTypes: ["HKDataTypeStateOfMind"],
        uploadError: null,
      }),
    });
    const health = createHealthSync({ native: f.native, platform: "ios" });
    const report = await health.sync();
    assert.deepEqual(f.calls, [{ method: "sync", args: [["*"]] }]);
    assert.equal(report.platform, "ios");
    assert.deepEqual(report.linesByType, { [HR]: 12 });
    assert.deepEqual(report.deletionsPendingByType, { [HR]: 1 });
    assert.deepEqual(report.unavailableTypes, ["HKDataTypeStateOfMind"]);
    assert.equal(report.partsPending, 0);
    assert.equal(report.uploadError, null);
    assert.equal(report.complete, true);
  });

  it("is not complete while iOS types wait for their turn", () => {
    const report = decodeSyncReport(
      JSON.stringify({ pendingTypes: [HR], uploadError: { code: "network", message: "offline" } }),
      "ios",
    );
    assert.equal(report.complete, false);
    assert.deepEqual(report.pendingTypes, [HR]);
    assert.deepEqual(report.uploadError, { code: "network", message: "offline" });
  });

  it("reads an unknown upload error code as unknown", () => {
    const report = decodeSyncReport(JSON.stringify({ uploadError: { code: "teapot", message: "x" } }), "android");
    assert.equal(report.uploadError?.code, "unknown");
  });

  it("refuses an empty list, an unknown name and a list the platform has nothing for", async () => {
    const f = fakeNative();
    const health = createHealthSync({ native: f.native, platform: "android" });
    await assert.rejects(health.sync([]), rejectsWith("invalid_argument"));
    await assert.rejects(health.sync(["nope" as HealthDataType]), rejectsWith("invalid_argument"));
    await assert.rejects(health.sync(["heartRhythm", "stateOfMind"]), rejectsWith("invalid_argument"));
    assert.equal(f.calls.length, 0);
  });

  it("rejects with the native code when the store cannot be read", async () => {
    for (const code of ["not_configured", "permission_denied", "health_store", "unavailable"]) {
      const f = fakeNative({ sync: nativeError(code, "native message") });
      const health = createHealthSync({ native: f.native, platform: "ios" });
      await assert.rejects(health.sync(["steps"]), rejectsWith(code));
    }
    const odd = createHealthSync({ native: fakeNative({ sync: nativeError("E_WHATEVER", "x") }).native, platform: "ios" });
    await assert.rejects(odd.sync(), rejectsWith("unknown"));
  });

  it("rejects with decode when the native side answers something else", async () => {
    for (const reply of ["", "[]", "not json", JSON.stringify({ partsUploaded: -1 }), JSON.stringify({ batchIds: [1] })]) {
      const health = createHealthSync({ native: fakeNative({ sync: reply }).native, platform: "android" });
      await assert.rejects(health.sync(), rejectsWith("decode"));
    }
  });
});

describe("flush and pendingParts", () => {
  it("flushes with or without force", async () => {
    const f = fakeNative({ flush: JSON.stringify({ partsUploaded: 2, partsPending: 0 }) });
    const health = createHealthSync({ native: f.native, platform: "android" });
    const report = await health.flush();
    await health.flush({ force: true });
    assert.deepEqual(f.calls, [
      { method: "flush", args: [false] },
      { method: "flush", args: [true] },
    ]);
    assert.equal(report.partsUploaded, 2);
    assert.equal(report.complete, true);
  });

  it("reads the queue size", async () => {
    const health = createHealthSync({ native: fakeNative({ pendingParts: 7 }).native, platform: "android" });
    assert.equal(await health.pendingParts(), 7);
  });
});

describe("background sync", () => {
  it("passes the defaults", async () => {
    const f = fakeNative();
    const health = createHealthSync({ native: f.native, platform: "ios" });
    await health.enableBackgroundSync();
    assert.deepEqual(f.calls, [{ method: "enableBackgroundSync", args: [["*"], 60, false, "immediate"] }]);
  });

  it("passes the options and raises a short interval to 15 minutes", async () => {
    const f = fakeNative();
    const health = createHealthSync({ native: f.native, platform: "android" });
    await health.enableBackgroundSync({ types: ["steps", "sleep"], intervalMinutes: 5, requireUnmeteredNetwork: true, frequency: "hourly" });
    assert.deepEqual(f.calls, [{ method: "enableBackgroundSync", args: [["Steps", "SleepSession"], 15, true, "hourly"] }]);
  });

  it("refuses bad options", async () => {
    const f = fakeNative();
    const health = createHealthSync({ native: f.native, platform: "android" });
    await assert.rejects(health.enableBackgroundSync({ intervalMinutes: 0 }), rejectsWith("invalid_argument"));
    await assert.rejects(health.enableBackgroundSync({ intervalMinutes: Number.NaN }), rejectsWith("invalid_argument"));
    await assert.rejects(health.enableBackgroundSync({ frequency: "often" as "hourly" }), rejectsWith("invalid_argument"));
    await assert.rejects(health.enableBackgroundSync({ types: [] }), rejectsWith("invalid_argument"));
    assert.equal(f.calls.length, 0);
  });

  it("turns it off", async () => {
    const f = fakeNative();
    await createHealthSync({ native: f.native, platform: "ios" }).disableBackgroundSync();
    assert.deepEqual(f.calls, [{ method: "disableBackgroundSync", args: [] }]);
  });

  it("delivers background results to listeners until they are removed", async () => {
    const f = fakeNative();
    const health = createHealthSync({ native: f.native, platform: "android" });
    const events: BackgroundSyncEvent[] = [];
    const subscription = health.addBackgroundSyncListener((e) => events.push(e));
    assert.equal(f.listenerCount, 1);

    f.emit(JSON.stringify({ ok: true, at: "2026-01-10T14:30:00Z", report: { partsUploaded: 1, linesByType: { Steps: 3 } } }));
    f.emit(JSON.stringify({ ok: false, at: "2026-01-10T15:30:00Z", error: { code: "permission_denied", message: "background read not granted" } }));
    f.emit("garbage");

    assert.equal(events.length, 2);
    const [first, second] = events;
    assert.ok(first?.ok === true);
    assert.equal(first.at, "2026-01-10T14:30:00Z");
    assert.equal(first.report.platform, "android");
    assert.deepEqual(first.report.linesByType, { Steps: 3 });
    assert.deepEqual(second, {
      ok: false,
      at: "2026-01-10T15:30:00Z",
      error: { code: "permission_denied", message: "background read not granted" },
    });

    subscription.remove();
    assert.equal(f.listenerCount, 0);
    f.emit(JSON.stringify({ ok: true, at: "2026-01-10T16:30:00Z", report: {} }));
    assert.equal(events.length, 2);
  });

  it("gives the last background result, or null when there is none", async () => {
    const none = createHealthSync({ native: fakeNative().native, platform: "ios" });
    assert.equal(await none.lastBackgroundSync(), null);

    const f = fakeNative({
      lastBackgroundSync: JSON.stringify({
        ok: true,
        at: "2026-01-10T14:30:00Z",
        report: { uploadError: { code: "network", message: "offline" }, pendingTypes: [HR] },
      }),
    });
    const last = await createHealthSync({ native: f.native, platform: "ios" }).lastBackgroundSync();
    assert.ok(last?.ok === true);
    assert.equal(last.report.uploadError?.code, "network");
    assert.equal(last.report.complete, false);
  });
});
