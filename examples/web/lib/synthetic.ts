// SPDX-License-Identifier: AGPL-3.0-only
//
// Synthetic health data, made up in code so the example runs without a wearable. Nothing here
// comes from a real person. The output is deterministic: the same seed and the same day always
// give the same lines, so uploading again sends the same bytes and the server answers
// "duplicate". Every line carries "synthetic": true in its metadata, which the server keeps.
//
// The lines are in the HealthKit line format of POST /v1/ingest/apple-health
// (docs/connectors/apple-health.md#line-formats), grouped the way a phone app groups them:
// one data type and one local day per part.

export const STEP_COUNT = "HKQuantityTypeIdentifierStepCount";
export const RESTING_HEART_RATE = "HKQuantityTypeIdentifierRestingHeartRate";
export const SLEEP_ANALYSIS = "HKCategoryTypeIdentifierSleepAnalysis";

/** HealthKit's sleep analysis values. */
export const SLEEP_VALUE = { inBed: 0, awake: 2, core: 3, deep: 4, rem: 5 } as const;

/** The time zone written in each line's metadata; it decides the local day on the server. */
export const SYNTHETIC_TIMEZONE = "UTC";

/** Who the lines say produced them. */
export const SYNTHETIC_SOURCE = {
  sourceName: "Open Health Sync web example",
  sourceBundleId: "org.openhealthsync.example.web",
  device: "Synthetic data generator",
} as const;

export interface SyntheticOptions {
  /** Last day of data, YYYY-MM-DD. Its night is the one that ends that morning. */
  endDay: string;
  /** Number of days, ending with endDay. */
  days: number;
  /** Another seed gives other values. */
  seed: number;
}

/** One NDJSON line of the upload. */
export interface HealthLine {
  startDate: string;
  endDate: string;
  [field: string]: unknown;
}

/** One upload part: one type, one day, numbered within the batch. */
export interface UploadPart {
  /** X-OHS-Type */
  type: string;
  /** X-OHS-Day */
  day: string;
  /** X-OHS-Part */
  part: number;
  lines: HealthLine[];
}

/** The figures of one day, for showing what an upload holds. */
export interface DayPlan {
  day: string;
  steps: number;
  restingHeartRate: number;
  /** In bed the night that ends this morning. */
  bedtime: string;
  wake: string;
  asleepMinutes: number;
}

const DAY_MS = 86_400_000;
const MINUTE_MS = 60_000;

export function isDay(value: string): boolean {
  return /^\d{4}-\d{2}-\d{2}$/.test(value) && !Number.isNaN(Date.parse(`${value}T00:00:00Z`));
}

export function addDays(day: string, n: number): string {
  return new Date(Date.parse(`${day}T00:00:00Z`) + n * DAY_MS).toISOString().slice(0, 10);
}

/** Yesterday in UTC: the default last day, so every night of the window is over. */
export function defaultEndDay(now: Date = new Date()): string {
  return addDays(now.toISOString().slice(0, 10), -1);
}

/** The batch id of an upload (X-OHS-Batch): letters, digits, "_" and "-". */
export function batchId(options: SyntheticOptions): string {
  return `web-example-${options.endDay}-${options.seed}`;
}

function check(options: SyntheticOptions): void {
  if (!isDay(options.endDay)) throw new RangeError(`endDay must be YYYY-MM-DD, got ${options.endDay}`);
  if (!Number.isInteger(options.days) || options.days < 1 || options.days > 60) {
    throw new RangeError("days must be an integer from 1 to 60");
  }
  if (!Number.isInteger(options.seed) || options.seed < 0) throw new RangeError("seed must be a whole number");
}

// A small seeded generator (FNV-1a hash of the key, then mulberry32): each day and kind of
// value gets its own sequence, so one day's values never depend on another day.
function random(seed: number, day: string, kind: string): () => number {
  let h = 0x811c9dc5;
  for (const ch of `${seed}|${day}|${kind}`) {
    h ^= ch.charCodeAt(0);
    h = Math.imul(h, 0x01000193);
  }
  let state = h >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function iso(ms: number): string {
  return new Date(ms).toISOString();
}

function at(day: string, time: string): number {
  return Date.parse(`${day}T${time}Z`);
}

function common(): Record<string, unknown> {
  return {
    ...SYNTHETIC_SOURCE,
    wasUserEntered: false,
    metadata: { HKTimeZone: SYNTHETIC_TIMEZONE, synthetic: true },
  };
}

function quantity(type: string, start: number, end: number, value: number, unit: string): HealthLine {
  return { quantityType: type, startDate: iso(start), endDate: iso(end), value, unit, ...common() };
}

function category(type: string, start: number, end: number, value: number): HealthLine {
  return { categoryType: type, startDate: iso(start), endDate: iso(end), value, ...common() };
}

// Hourly step weights from 07:00 to 21:00: a walk to work, lunch, the way home, an evening walk.
const STEP_PROFILE = [3, 6, 4, 3, 5, 8, 6, 3, 3, 4, 7, 9, 6, 4, 2];

/** The day's step total split over the hours of STEP_PROFILE, as whole steps. */
function stepLines(day: string, seed: number): HealthLine[] {
  const r = random(seed, day, "steps");
  const weekend = [0, 6].includes(new Date(`${day}T00:00:00Z`).getUTCDay());
  const total = Math.round((weekend ? 7000 : 9000) + (r() - 0.5) * 5000);
  const weights = STEP_PROFILE.map((w) => w * (0.6 + 0.8 * r()));
  const sum = weights.reduce((a, b) => a + b, 0);
  // Largest remainder, so the hours add up to the total exactly.
  const exact = weights.map((w) => (w / sum) * total);
  const counts = exact.map(Math.floor);
  let left = total - counts.reduce((a, b) => a + b, 0);
  const order = exact.map((x, i) => [x - Math.floor(x), i] as const).sort((a, b) => b[0] - a[0] || a[1] - b[1]);
  for (const [, i] of order) {
    if (left-- <= 0) break;
    counts[i] = (counts[i] ?? 0) + 1;
  }
  return counts.flatMap((count, i) => {
    if (count <= 0) return [];
    const start = at(day, `${String(7 + i).padStart(2, "0")}:00:00`);
    return [quantity(STEP_COUNT, start, start + 60 * MINUTE_MS, count, "count")];
  });
}

/** One resting heart rate a day, with a slow 11-day rhythm, in beats per minute. */
function restingHeartRateLine(day: string, seed: number): HealthLine {
  const r = random(seed, day, "resting-heart-rate");
  const t = Date.parse(`${day}T00:00:00Z`) / DAY_MS;
  const value = Math.round(56 + 3 * Math.sin((2 * Math.PI * t) / 11) + (r() - 0.5) * 3);
  const time = at(day, "07:00:00");
  return quantity(RESTING_HEART_RATE, time, time, value, "count/min");
}

/** The night that ends on the morning of day: one "in bed" line and the stages inside it. */
function sleepLines(day: string, seed: number): HealthLine[] {
  const r = random(seed, day, "sleep");
  const minutes = (n: number) => Math.round(n) * MINUTE_MS;
  const bed = at(addDays(day, -1), "22:30:00") + minutes(r() * 60);
  const wake = at(day, "06:15:00") + minutes(r() * 60);
  const asleep = bed + minutes(8 + r() * 14);
  const lastStageEnd = wake - minutes(3 + r() * 7);

  const lines = [category(SLEEP_ANALYSIS, bed, wake, SLEEP_VALUE.inBed)];
  let t = asleep;
  const push = (length: number, value: number) => {
    const end = Math.min(t + minutes(length), lastStageEnd);
    if (end - t >= MINUTE_MS) lines.push(category(SLEEP_ANALYSIS, t, end, value));
    t = end;
  };
  // About 90-minute cycles: more deep sleep early in the night, more REM toward morning.
  for (let cycle = 0; t < lastStageEnd; cycle++) {
    push(25 + r() * 15, SLEEP_VALUE.core);
    push(cycle < 2 ? 18 + r() * 16 : 4 + r() * 8, SLEEP_VALUE.deep);
    push(10 + r() * 10, SLEEP_VALUE.core);
    push(cycle === 0 ? 8 + r() * 6 : 16 + r() * 14, SLEEP_VALUE.rem);
    if (r() < 0.4) push(2 + r() * 3, SLEEP_VALUE.awake);
  }
  return lines;
}

/** Every line of the window, in day order. */
export function syntheticLines(options: SyntheticOptions): HealthLine[] {
  check(options);
  const lines: HealthLine[] = [];
  for (let i = options.days - 1; i >= 0; i--) {
    const day = addDays(options.endDay, -i);
    lines.push(...sleepLines(day, options.seed), ...stepLines(day, options.seed));
    lines.push(restingHeartRateLine(day, options.seed));
  }
  return lines;
}

function typeOf(line: HealthLine): string {
  return String(line.quantityType ?? line.categoryType);
}

/**
 * The lines as upload parts: one type and one local day per part (a night that crosses
 * midnight is in two parts, as a phone sends it), ordered by day then type and numbered from 0,
 * so (day, batch, part) is unique across types.
 */
export function syntheticParts(options: SyntheticOptions): UploadPart[] {
  const groups = new Map<string, HealthLine[]>();
  for (const line of syntheticLines(options)) {
    // The time zone is UTC, so the local day is the UTC date of the start.
    const key = `${line.startDate.slice(0, 10)}|${typeOf(line)}`;
    const group = groups.get(key);
    if (group) group.push(line);
    else groups.set(key, [line]);
  }
  return [...groups.keys()].sort().map((key, part) => {
    const [day = "", type = ""] = key.split("|");
    return { type, day, part, lines: groups.get(key) ?? [] };
  });
}

/** A part's body before compression: one JSON object per line. */
export function toNdjson(lines: HealthLine[]): string {
  return lines.map((line) => JSON.stringify(line)).join("\n") + "\n";
}

/** The figures of each day of the window, computed from the same lines that are uploaded. */
export function dayPlans(options: SyntheticOptions): DayPlan[] {
  const plans: DayPlan[] = [];
  for (let i = options.days - 1; i >= 0; i--) {
    const day = addDays(options.endDay, -i);
    const steps = stepLines(day, options.seed).reduce((sum, line) => sum + Number(line.value), 0);
    const [inBed, ...stages] = sleepLines(day, options.seed);
    const asleep = stages
      .filter((line) => line.value !== SLEEP_VALUE.awake)
      .reduce((sum, line) => sum + Date.parse(line.endDate) - Date.parse(line.startDate), 0);
    plans.push({
      day,
      steps,
      restingHeartRate: Number(restingHeartRateLine(day, options.seed).value),
      bedtime: inBed?.startDate ?? "",
      wake: inBed?.endDate ?? "",
      asleepMinutes: Math.round(asleep / MINUTE_MS),
    });
  }
  return plans;
}
