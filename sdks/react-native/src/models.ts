// SPDX-License-Identifier: AGPL-3.0-only

/**
 * Records and responses of the Open Health Sync API, with camelCase field names.
 *
 * Timestamps are kept as the RFC 3339 strings the server sends (UTC, up to nine fractional
 * digits). Use `parseTimestamp` to get a `Date` (millisecond precision). `date` fields are
 * `YYYY-MM-DD` strings. Ids typed `number` are int64 on the server; they are exact up to
 * 2^53 - 1.
 */

/** An RFC 3339 timestamp, for example `2026-01-10T14:30:00.123456789Z`. */
export type Timestamp = string;

/** A calendar day, `YYYY-MM-DD`. */
export type DateString = string;

/** A free JSON object, kept as the server sent it. */
export type JsonObject = { [key: string]: unknown };

/** One page of a list route. `nextCursor` is null on the last page. */
export interface Page<T> {
  data: T[];
  nextCursor: string | null;
}

export type ProviderKind = "oauth" | "push" | "device";

export interface Provider {
  slug: string;
  kinds: ProviderKind[];
}

export type UserStatus = "active" | "deleting";

export interface User {
  id: string;
  externalRef: string | null;
  status: UserStatus;
  createdAt: Timestamp;
}

export type DeletionStatus = "pending" | "running" | "done" | "failed";

export interface DeletionJob {
  jobId: string;
  userId: string;
  status: DeletionStatus;
  createdAt: Timestamp;
  finishedAt: Timestamp | null;
}

export type TokenScope = "upload" | "connect" | "read";

export interface UserToken {
  /** The `ohs_ut_` secret. Shown once; never log it. */
  token: string;
  expiresAt: Timestamp;
  scopes: TokenScope[];
}

export type ConnectionStatus = "active" | "needs_reauth" | "disconnected";

export interface Connection {
  provider: string;
  status: ConnectionStatus;
  scopes?: string[];
  lastSyncAt?: Timestamp | null;
  initialBackfillAt?: Timestamp | null;
  createdAt: Timestamp;
  updatedAt?: Timestamp;
}

export type FreshnessState = "fresh" | "stale" | "failing" | "syncing" | "needs_reauth" | "disconnected";

export interface SourceFreshness {
  provider: string;
  kind: "oauth" | "device";
  status: ConnectionStatus;
  state: FreshnessState;
  syncedThrough: Timestamp | null;
  lagSeconds: number | null;
  lastSyncAt?: Timestamp | null;
  lastEventAt?: Timestamp | null;
  pushHealthy: boolean;
  pullEverySeconds: number;
  nextPullAt?: Timestamp | null;
  lastAttemptAt?: Timestamp | null;
  lastError?: string | null;
  consecutiveFailures: number;
}

export interface Freshness {
  asOf: Timestamp;
  data: SourceFreshness[];
}

/** Fields every stored record carries. */
export interface Provenance {
  /** The integration the record came through (apple_health, withings...). */
  provider: string;
  /** The vendor that produced the record, when it differs or is known. */
  source?: string | null;
  device?: string | null;
  ingestedAt: Timestamp;
}

export interface Sample extends Provenance {
  id: number;
  type: string;
  value: number;
  unit: string | null;
  startDate: Timestamp;
  endDate: Timestamp | null;
  sourceName?: string | null;
  sourceBundleId?: string | null;
  deviceModel?: string | null;
  productType?: string | null;
  externalId?: string | null;
  isPrimarySource?: boolean | null;
  wasUserEntered?: boolean | null;
  metadata: JsonObject;
}

export interface CategorySample extends Provenance {
  id: number;
  type: string;
  valueInt?: number | null;
  valueLabel?: string | null;
  startDate: Timestamp;
  endDate?: Timestamp | null;
  sourceName?: string | null;
  externalId?: string | null;
  metadata: JsonObject;
}

export interface SleepSession extends Provenance {
  id: number;
  bedtimeStart: Timestamp;
  bedtimeEnd?: Timestamp | null;
  totalSleepSeconds?: number | null;
  deepSleepSeconds?: number | null;
  remSleepSeconds?: number | null;
  lightSleepSeconds?: number | null;
  awakeSeconds?: number | null;
  sleepScore?: number | null;
  hrvAvgMs?: number | null;
  restingHr?: number | null;
  respiratoryRate?: number | null;
  spo2Avg?: number | null;
  externalId?: string | null;
  metadata: JsonObject;
}

export interface SleepPhase extends Provenance {
  id: number;
  sessionId: number;
  phase: string;
  startDate: Timestamp;
  endDate?: Timestamp | null;
  durationS: number;
}

export interface Workout extends Provenance {
  id: number;
  activityType: string;
  startDate: Timestamp;
  endDate?: Timestamp | null;
  durationS?: number | null;
  activeEnergyKcal?: number | null;
  distanceM?: number | null;
  avgHeartRate?: number | null;
  maxHeartRate?: number | null;
  avgSpeedMps?: number | null;
  elevationGainM?: number | null;
  vo2Max?: number | null;
  externalId?: string | null;
  metadata: JsonObject;
}

export interface RoutePoint extends Provenance {
  id: number;
  workoutId: number;
  timestamp: Timestamp;
  latitude: number;
  longitude: number;
  altitudeM?: number | null;
  speedMps?: number | null;
  courseDeg?: number | null;
}

export interface NutritionLog extends Provenance {
  id: number;
  startDate: Timestamp;
  endDate?: Timestamp | null;
  mealType?: string | null;
  energyKcal?: number | null;
  proteinG?: number | null;
  carbsG?: number | null;
  fatG?: number | null;
  fiberG?: number | null;
  sugarG?: number | null;
  sodiumMg?: number | null;
  /** Extended nutrients by name. */
  nutrients?: JsonObject | null;
  externalId?: string | null;
  metadata: JsonObject;
}

export interface DailySummary extends Provenance {
  id: number;
  /** The user's local calendar day, `YYYY-MM-DD`. */
  date: DateString;
  readinessScore?: number | null;
  activityScore?: number | null;
  sleepScore?: number | null;
  recoveryScore?: number | null;
  strainScore?: number | null;
  stepCount?: number | null;
  activeKcal?: number | null;
  totalKcal?: number | null;
  distanceM?: number | null;
  floorsClimbed?: number | null;
  restingHr?: number | null;
  hrvAvgMs?: number | null;
  metadata: JsonObject;
}

export interface CoverageDay {
  provider: string;
  date: DateString;
  /** Sum over metrics. */
  records: number;
  /** Record count per metric name. */
  metrics: { [metric: string]: number };
  lastSyncedAt: Timestamp;
}

export interface Coverage {
  /** ETL jobs of the user not finished yet. Poll until 0 after an upload. */
  pendingJobs: number;
  /** ETL jobs that exhausted their retries. */
  parkedJobs: number;
  /** Lines the ETL could not store. */
  rejectedRecords: number;
  /** True when more than 10000 days matched; `days` holds the earliest. */
  truncated: boolean;
  days: CoverageDay[];
}

/** A FHIR R4 searchset Bundle, kept as raw JSON, with the parts most callers need. */
export interface FhirBundle {
  /** The Bundle exactly as the server sent it. */
  raw: JsonObject;
  /** The Observation resources of the page, as raw JSON. Empty on an empty page. */
  observations: JsonObject[];
  /** The `cursor` of the Bundle's `next` link, or null on the last page. */
  nextCursor: string | null;
}

export interface UploadResult {
  /** Raw object key on the server. */
  key: string;
  /** True when the server already held these exact bytes. */
  duplicate: boolean;
}

// ---- decoding -------------------------------------------------------------------------

const PROVENANCE = ["provider", "source", "device", "ingested_at"] as const;

function snakeToCamel(s: string): string {
  return s.replace(/_([a-z0-9])/g, (_m, c: string) => c.toUpperCase());
}

/** Thrown when a 2xx response does not have the shape the API documents. */
export class OhsDecodeError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "OhsDecodeError";
    Object.setPrototypeOf(this, OhsDecodeError.prototype);
  }
}

function asObject(raw: unknown, what: string): JsonObject {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    throw new OhsDecodeError(`Open Health Sync: expected a JSON object for ${what}`);
  }
  return raw as JsonObject;
}

function asArray(raw: unknown, what: string): unknown[] {
  if (!Array.isArray(raw)) {
    throw new OhsDecodeError(`Open Health Sync: expected a JSON array for ${what}`);
  }
  return raw;
}

/**
 * Copies the listed snake_case fields that are present into a camelCase object. Values are
 * copied as is, so free maps (metadata, nutrients, metrics) keep their own keys. Fields not
 * listed are ignored.
 */
function pick<T>(raw: unknown, what: string, fields: readonly string[]): T {
  const obj = asObject(raw, what);
  const out: JsonObject = {};
  for (const f of fields) {
    if (Object.prototype.hasOwnProperty.call(obj, f)) {
      out[snakeToCamel(f)] = obj[f];
    }
  }
  return out as T;
}

function decoder<T>(what: string, fields: readonly string[]): (raw: unknown) => T {
  return (raw) => pick<T>(raw, what, fields);
}

export const decodeProvider = decoder<Provider>("Provider", ["slug", "kinds"]);
export const decodeUser = decoder<User>("User", ["id", "external_ref", "status", "created_at"]);
export const decodeDeletionJob = decoder<DeletionJob>("DeletionJob", [
  "job_id", "user_id", "status", "created_at", "finished_at",
]);
export const decodeUserToken = decoder<UserToken>("UserToken", ["token", "expires_at", "scopes"]);
export const decodeConnection = decoder<Connection>("Connection", [
  "provider", "status", "scopes", "last_sync_at", "initial_backfill_at", "created_at", "updated_at",
]);
export const decodeSourceFreshness = decoder<SourceFreshness>("SourceFreshness", [
  "provider", "kind", "status", "state", "synced_through", "lag_seconds", "last_sync_at",
  "last_event_at", "push_healthy", "pull_every_seconds", "next_pull_at", "last_attempt_at",
  "last_error", "consecutive_failures",
]);
export const decodeSample = decoder<Sample>("Sample", [
  ...PROVENANCE, "id", "type", "value", "unit", "start_date", "end_date", "source_name",
  "source_bundle_id", "device_model", "product_type", "external_id", "is_primary_source",
  "was_user_entered", "metadata",
]);
export const decodeCategorySample = decoder<CategorySample>("CategorySample", [
  ...PROVENANCE, "id", "type", "value_int", "value_label", "start_date", "end_date",
  "source_name", "external_id", "metadata",
]);
export const decodeSleepSession = decoder<SleepSession>("SleepSession", [
  ...PROVENANCE, "id", "bedtime_start", "bedtime_end", "total_sleep_seconds",
  "deep_sleep_seconds", "rem_sleep_seconds", "light_sleep_seconds", "awake_seconds",
  "sleep_score", "hrv_avg_ms", "resting_hr", "respiratory_rate", "spo2_avg", "external_id",
  "metadata",
]);
export const decodeSleepPhase = decoder<SleepPhase>("SleepPhase", [
  ...PROVENANCE, "id", "session_id", "phase", "start_date", "end_date", "duration_s",
]);
export const decodeWorkout = decoder<Workout>("Workout", [
  ...PROVENANCE, "id", "activity_type", "start_date", "end_date", "duration_s",
  "active_energy_kcal", "distance_m", "avg_heart_rate", "max_heart_rate", "avg_speed_mps",
  "elevation_gain_m", "vo2_max", "external_id", "metadata",
]);
export const decodeRoutePoint = decoder<RoutePoint>("RoutePoint", [
  ...PROVENANCE, "id", "workout_id", "timestamp", "latitude", "longitude", "altitude_m",
  "speed_mps", "course_deg",
]);
export const decodeNutritionLog = decoder<NutritionLog>("NutritionLog", [
  ...PROVENANCE, "id", "start_date", "end_date", "meal_type", "energy_kcal", "protein_g",
  "carbs_g", "fat_g", "fiber_g", "sugar_g", "sodium_mg", "nutrients", "external_id", "metadata",
]);
export const decodeDailySummary = decoder<DailySummary>("DailySummary", [
  ...PROVENANCE, "id", "date", "readiness_score", "activity_score", "sleep_score",
  "recovery_score", "strain_score", "step_count", "active_kcal", "total_kcal", "distance_m",
  "floors_climbed", "resting_hr", "hrv_avg_ms", "metadata",
]);
const decodeCoverageDay = decoder<CoverageDay>("CoverageDay", [
  "provider", "date", "records", "metrics", "last_synced_at",
]);

export function decodeCoverage(raw: unknown): Coverage {
  const c = pick<Coverage>(raw, "Coverage", [
    "pending_jobs", "parked_jobs", "rejected_records", "truncated", "days",
  ]);
  c.days = asArray(c.days ?? [], "Coverage.days").map(decodeCoverageDay);
  return c;
}

export function decodeFreshness(raw: unknown): Freshness {
  const obj = asObject(raw, "Freshness");
  return {
    asOf: obj["as_of"] as Timestamp,
    data: asArray(obj["data"] ?? [], "Freshness.data").map(decodeSourceFreshness),
  };
}

export function decodeList<T>(raw: unknown, what: string, item: (raw: unknown) => T): T[] {
  const obj = asObject(raw, what);
  return asArray(obj["data"] ?? [], `${what}.data`).map(item);
}

export function decodePage<T>(raw: unknown, what: string, item: (raw: unknown) => T): Page<T> {
  const obj = asObject(raw, what);
  const next = obj["next_cursor"];
  return {
    data: asArray(obj["data"] ?? [], `${what}.data`).map(item),
    nextCursor: typeof next === "string" && next !== "" ? next : null,
  };
}

export function decodeUploadResult(raw: unknown): UploadResult {
  return pick<UploadResult>(raw, "UploadResult", ["key", "duplicate"]);
}

/** Returns the `cursor` query parameter of a URL, or null. Does not rely on `URL`. */
export function cursorFromUrl(url: string): string | null {
  const q = url.indexOf("?");
  if (q < 0) return null;
  const hash = url.indexOf("#", q);
  const query = url.slice(q + 1, hash < 0 ? undefined : hash);
  for (const pair of query.split("&")) {
    const eq = pair.indexOf("=");
    const name = decodeQueryComponent(eq < 0 ? pair : pair.slice(0, eq));
    if (name === "cursor") {
      return eq < 0 ? "" : decodeQueryComponent(pair.slice(eq + 1));
    }
  }
  return null;
}

function decodeQueryComponent(s: string): string {
  try {
    return decodeURIComponent(s.replace(/\+/g, " "));
  } catch {
    return s;
  }
}

export function decodeFhirBundle(raw: unknown): FhirBundle {
  const obj = asObject(raw, "FHIR Bundle");
  let nextCursor: string | null = null;
  const links = Array.isArray(obj["link"]) ? (obj["link"] as unknown[]) : [];
  for (const l of links) {
    if (l && typeof l === "object") {
      const link = l as JsonObject;
      if (link["relation"] === "next" && typeof link["url"] === "string") {
        const c = cursorFromUrl(link["url"]);
        nextCursor = c === "" ? null : c;
        break;
      }
    }
  }
  const observations: JsonObject[] = [];
  const entries = Array.isArray(obj["entry"]) ? (obj["entry"] as unknown[]) : [];
  for (const e of entries) {
    if (e && typeof e === "object") {
      const resource = (e as JsonObject)["resource"];
      if (resource && typeof resource === "object" && !Array.isArray(resource)) {
        observations.push(resource as JsonObject);
      }
    }
  }
  return { raw: obj, observations, nextCursor };
}
