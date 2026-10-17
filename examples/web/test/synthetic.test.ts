// SPDX-License-Identifier: AGPL-3.0-only
//
// The synthetic data generator: deterministic, plausible, marked as synthetic, and grouped
// into parts the upload route accepts (one type and one day per part, unique part numbers).

import assert from "node:assert/strict";
import { test } from "node:test";
import {
  RESTING_HEART_RATE,
  SLEEP_ANALYSIS,
  SLEEP_VALUE,
  STEP_COUNT,
  addDays,
  batchId,
  dayPlans,
  defaultEndDay,
  syntheticLines,
  syntheticParts,
  toNdjson,
} from "../lib/synthetic.ts";

const options = { endDay: "2026-03-14", days: 14, seed: 1 };

test("the same options give the same bytes", () => {
  const a = syntheticParts(options).map((part) => toNdjson(part.lines));
  const b = syntheticParts({ ...options }).map((part) => toNdjson(part.lines));
  assert.deepEqual(a, b);
});

test("another seed gives other values", () => {
  assert.notDeepEqual(syntheticLines(options), syntheticLines({ ...options, seed: 2 }));
});

test("a day's values do not depend on the window", () => {
  // The last day of a 14-day window and of a 3-day window ending the same day are the same.
  const long = dayPlans(options).at(-1);
  const short = dayPlans({ ...options, days: 3 }).at(-1);
  assert.deepEqual(long, short);
});

test("every line is marked synthetic and carries its time zone", () => {
  for (const line of syntheticLines(options)) {
    assert.deepEqual(line.metadata, { HKTimeZone: "UTC", synthetic: true });
    assert.equal(line.wasUserEntered, false);
    assert.match(String(line.sourceName), /example/);
  }
});

test("values are plausible", () => {
  for (const plan of dayPlans(options)) {
    assert.ok(plan.steps >= 4000 && plan.steps <= 12000, `steps ${plan.steps} on ${plan.day}`);
    assert.ok(plan.restingHeartRate >= 50 && plan.restingHeartRate <= 62, `resting ${plan.restingHeartRate}`);
    const inBed = (Date.parse(plan.wake) - Date.parse(plan.bedtime)) / 3_600_000;
    assert.ok(inBed >= 6.75 && inBed <= 9, `in bed ${inBed} h`);
    assert.ok(plan.asleepMinutes >= 360 && plan.asleepMinutes <= 520, `asleep ${plan.asleepMinutes} min`);
  }
});

test("one night per day, with its stages inside the time in bed", () => {
  const lines = syntheticLines(options).filter((line) => line.categoryType === SLEEP_ANALYSIS);
  const nights = lines.filter((line) => line.value === SLEEP_VALUE.inBed);
  assert.equal(nights.length, options.days);
  for (const night of nights) {
    const stages = lines.filter(
      (line) => line.value !== SLEEP_VALUE.inBed && line.startDate >= night.startDate && line.startDate < night.endDate,
    );
    assert.ok(stages.length >= 8, "a night has several stages");
    for (const stage of stages) {
      assert.ok(stage.endDate <= night.endDate);
      assert.ok(stage.endDate > stage.startDate);
    }
    const kinds = new Set(stages.map((stage) => stage.value));
    for (const value of [SLEEP_VALUE.core, SLEEP_VALUE.deep, SLEEP_VALUE.rem]) assert.ok(kinds.has(value));
  }
  // Each night ends on the morning of a day of the window, the last one on endDay.
  assert.equal(nights.at(-1)?.endDate.slice(0, 10), options.endDay);
});

test("parts hold one type and one day, numbered from 0", () => {
  const parts = syntheticParts(options);
  assert.deepEqual(
    parts.map((part) => part.part),
    parts.map((_, i) => i),
  );
  const seen = new Set<string>();
  for (const part of parts) {
    assert.ok([STEP_COUNT, RESTING_HEART_RATE, SLEEP_ANALYSIS].includes(part.type));
    assert.ok(part.lines.length > 0);
    for (const line of part.lines) {
      assert.equal(line.quantityType ?? line.categoryType, part.type);
      assert.equal(line.startDate.slice(0, 10), part.day);
    }
    const key = `${part.day}|${part.type}`;
    assert.ok(!seen.has(key), `one part per type and day: ${key}`);
    seen.add(key);
  }
  // Steps and resting heart rate on each of the 14 days, sleep on 15 (the first night starts
  // the evening before the window).
  assert.equal(parts.length, 14 + 14 + 15);
  assert.equal(parts[0]?.day, addDays(options.endDay, -14));
});

test("NDJSON has one JSON object per line", () => {
  const [part] = syntheticParts(options);
  assert.ok(part);
  const text = toNdjson(part.lines);
  assert.ok(text.endsWith("\n"));
  const rows = text.trimEnd().split("\n");
  assert.equal(rows.length, part.lines.length);
  for (const row of rows) assert.equal(typeof JSON.parse(row), "object");
});

test("batch ids and days", () => {
  assert.match(batchId(options), /^[A-Za-z0-9_-]{1,128}$/);
  assert.equal(defaultEndDay(new Date("2026-03-15T05:00:00Z")), "2026-03-14");
  assert.equal(addDays("2026-02-28", 1), "2026-03-01");
  assert.throws(() => syntheticLines({ ...options, endDay: "14/03/2026" }), RangeError);
  assert.throws(() => syntheticLines({ ...options, days: 0 }), RangeError);
});
