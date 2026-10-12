// SPDX-License-Identifier: MIT

import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { gunzipSync } from "node:zlib";

import {
  AppleHealthPartBuilder,
  DEFAULT_MAX_PART_BYTES,
  cursorFromUrl,
  localDay,
  newBatchId,
  parseTimestamp,
} from "../src/index.ts";

const HR = "HKQuantityTypeIdentifierHeartRate";
const STEPS = "HKQuantityTypeIdentifierStepCount";

function lines(body: Uint8Array): string[] {
  const text = gunzipSync(body).toString("utf8");
  assert.ok(text.endsWith("\n"));
  return text.slice(0, -1).split("\n");
}

describe("AppleHealthPartBuilder", () => {
  it("groups by type and day in order of first appearance and numbers parts across groups", () => {
    const b = new AppleHealthPartBuilder("run-1");
    b.add(HR, "2026-01-10", { value: 1 });
    b.add(STEPS, "2026-01-10", { value: 2 });
    b.add(HR, "2026-01-11", { value: 3 });
    b.add(HR, "2026-01-10", { value: 4 });
    assert.equal(b.lineCount, 4);
    const parts = b.build();
    assert.deepEqual(
      parts.map((p) => [p.type, p.day, p.batch, p.partNumber, p.lineCount]),
      [
        [HR, "2026-01-10", "run-1", 0, 2],
        [STEPS, "2026-01-10", "run-1", 1, 1],
        [HR, "2026-01-11", "run-1", 2, 1],
      ],
    );
    assert.deepEqual(lines(parts[0]!.body), ['{"value":1}', '{"value":4}']);
    assert.deepEqual(lines(parts[1]!.body), ['{"value":2}']);
    assert.equal(parts[0]!.uncompressedBytes, 24);
    assert.equal(DEFAULT_MAX_PART_BYTES, 8 * 1024 * 1024);
  });

  it("splits a group so no part exceeds the maximum, and puts an oversized line alone", () => {
    // Each {"v":N} line with N one digit is 8 bytes with its newline.
    const b = new AppleHealthPartBuilder("b", 20);
    for (let i = 0; i < 5; i++) b.add(HR, "2026-01-10", { v: i });
    b.add(HR, "2026-01-10", { big: "x".repeat(40) });
    b.add(HR, "2026-01-10", { v: 9 });
    b.add(STEPS, "2026-01-10", { v: 1 });
    const parts = b.build();
    assert.deepEqual(
      parts.map((p) => [p.type, p.partNumber, p.uncompressedBytes]),
      [
        [HR, 0, 16],
        [HR, 1, 16],
        [HR, 2, 8],
        [HR, 3, 51],
        [HR, 4, 8],
        [STEPS, 5, 8],
      ],
    );
    assert.deepEqual(lines(parts[3]!.body), [JSON.stringify({ big: "x".repeat(40) })]);
    const all = parts.filter((p) => p.type === HR).flatMap((p) => lines(p.body));
    assert.equal(all.length, 7);
  });

  it("round trips a real line through gzip, UTF-8 included", () => {
    const line = {
      quantityType: HR,
      startDate: "2026-01-10T14:30:00.000Z",
      endDate: "2026-01-10T14:30:00.000Z",
      value: 72.5,
      unit: "count/min",
      sourceName: "Montre de Zoé",
      metadata: { HKTimeZone: "Europe/Paris" },
    };
    const [p] = new AppleHealthPartBuilder("utf8").add(HR, "2026-01-10", line).build();
    assert.deepEqual(JSON.parse(lines(p!.body)[0]!), line);
    assert.equal(p!.body[0], 0x1f);
    assert.equal(p!.body[1], 0x8b);
  });

  it("validates its input", () => {
    assert.throws(() => new AppleHealthPartBuilder("bad batch"), RangeError);
    assert.throws(() => new AppleHealthPartBuilder("x".repeat(129)), RangeError);
    assert.throws(() => new AppleHealthPartBuilder("ok", 0), RangeError);
    const b = new AppleHealthPartBuilder("ok");
    assert.throws(() => b.add("HK.Type", "2026-01-10", {}), RangeError);
    assert.throws(() => b.add(HR, "2026-1-10", {}), RangeError);
    assert.throws(() => b.add(HR, "2026-02-30", {}), RangeError);
    assert.throws(() => b.add(HR, "2026-01-10", [] as unknown as Record<string, unknown>), TypeError);
    assert.equal(b.build().length, 0);
  });
});

describe("helpers", () => {
  it("newBatchId gives ids the server accepts", () => {
    const a = newBatchId("app");
    const b = newBatchId("app");
    assert.match(a, /^app-\d{8}T\d{6}Z-[a-z0-9]{10}$/);
    assert.notEqual(a, b);
    assert.match(newBatchId("p".repeat(128)), /^[A-Za-z0-9_-]{1,128}$/);
    assert.throws(() => newBatchId("no spaces"), RangeError);
  });

  it("localDay uses the given time zone", () => {
    const t = new Date("2026-01-10T23:30:00Z");
    assert.equal(localDay(t, "UTC"), "2026-01-10");
    assert.equal(localDay(t, "Europe/Paris"), "2026-01-11");
    assert.equal(localDay(t, "America/Los_Angeles"), "2026-01-10");
  });

  it("parseTimestamp accepts what the server emits", () => {
    assert.equal(parseTimestamp("2026-01-10T14:30:00.123456789Z").toISOString(), "2026-01-10T14:30:00.123Z");
    assert.equal(parseTimestamp("2026-01-10T14:30:00Z").toISOString(), "2026-01-10T14:30:00.000Z");
    assert.equal(parseTimestamp("2026-01-10T14:30:00.5Z").toISOString(), "2026-01-10T14:30:00.500Z");
    assert.equal(parseTimestamp("2026-01-10T15:30:00.1+01:00").toISOString(), "2026-01-10T14:30:00.100Z");
    assert.throws(() => parseTimestamp("2026-01-10"), RangeError);
    assert.throws(() => parseTimestamp("yesterday"), RangeError);
  });

  it("cursorFromUrl reads the cursor parameter", () => {
    assert.equal(cursorFromUrl("https://x/y?limit=1&cursor=a%2Bb%3D"), "a+b=");
    assert.equal(cursorFromUrl("https://x/y?limit=1"), null);
    assert.equal(cursorFromUrl("https://x/y"), null);
  });
});
