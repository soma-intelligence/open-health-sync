// SPDX-License-Identifier: AGPL-3.0-only

import { type HealthDataType, type HealthPlatform, dataTypesOfNativeName } from "./types.ts";

/** Why a health sync call failed. */
export type HealthSyncErrorCode =
  /** The native module is not in this build (Expo Go, the web, the old architecture). */
  | "not_linked"
  /** The health store cannot be used on this device. */
  | "unavailable"
  /** `configure` has not been called, or the credentials were cleared. */
  | "not_configured"
  /** An argument was refused before anything ran. */
  | "invalid_argument"
  /** Android: no activity is in front to show the permission screen. */
  | "no_activity"
  /** A permission request is already on screen. */
  | "in_progress"
  /** The health store refused a read (permission taken back, background read not allowed). */
  | "permission_denied"
  /** The health store failed (locked device, Health Connect unreachable). */
  | "health_store"
  /** The Keychain or the Android Keystore refused to keep or give the credentials. */
  | "storage"
  /** The native side answered something this package cannot read. */
  | "decode"
  | "unknown";

const ERROR_CODES: readonly string[] = [
  "not_linked",
  "unavailable",
  "not_configured",
  "invalid_argument",
  "no_activity",
  "in_progress",
  "permission_denied",
  "health_store",
  "storage",
  "decode",
  "unknown",
];

/** An error of the health sync API. The message never holds the token or a health value. */
export class HealthSyncError extends Error {
  readonly code: HealthSyncErrorCode;

  constructor(code: HealthSyncErrorCode, message: string) {
    super(message);
    this.name = "HealthSyncError";
    // Keep instanceof working when classes are transpiled (Babel, older targets).
    Object.setPrototypeOf(this, HealthSyncError.prototype);
    this.code = code;
  }

  /** Turns what a native call rejected with into a `HealthSyncError`. */
  static from(e: unknown): HealthSyncError {
    if (e instanceof HealthSyncError) return e;
    const o = typeof e === "object" && e !== null ? (e as { code?: unknown; message?: unknown }) : {};
    const code = typeof o.code === "string" && ERROR_CODES.includes(o.code) ? (o.code as HealthSyncErrorCode) : "unknown";
    return new HealthSyncError(code, typeof o.message === "string" && o.message !== "" ? o.message : "health sync failed");
  }
}

/** Why an upload stopped. */
export type UploadErrorCode =
  /** No network, or the server could not be reached. */
  | "network"
  /** 401: the user token expired or was revoked. Mint a new one and call `configure`. */
  | "unauthorized"
  /** 403: the token lacks the `upload` scope, or the connector is turned off. */
  | "forbidden"
  /** 429. */
  | "rate_limited"
  /** A 5xx answer. */
  | "server"
  /** Another answer of the API. */
  | "api"
  | "unknown";

const UPLOAD_ERROR_CODES: readonly string[] = ["network", "unauthorized", "forbidden", "rate_limited", "server", "api", "unknown"];

/** The upload failure of a sync that left data behind. */
export interface UploadError {
  code: UploadErrorCode;
  /** The native SDK's message; no token, no health value. */
  message: string;
  /** The HTTP status, when the server answered. */
  status?: number;
  /** The request id to quote in a bug report. */
  requestId?: string;
}

/**
 * What one `sync` or `flush` did. The same object on both platforms; a field a platform has
 * no use for is 0 or empty.
 *
 * The keys of the `...ByType` maps are the platform's own type names (a HealthKit identifier
 * such as `HKQuantityTypeIdentifierHeartRate`, a Health Connect record type such as
 * `HeartRate`), because one data type can cover several of them.
 */
export interface SyncReport {
  platform: HealthPlatform;
  /** The batch ids used. */
  batchIds: string[];
  /** Parts the server stored or already had. */
  partsUploaded: number;
  /** Of `partsUploaded`, the parts the server already had. */
  duplicateParts: number;
  /** Android: parts still in the queue on disk. Always 0 on iOS, which has no queue. */
  partsPending: number;
  /** Android: parts the server refused for good; they left the queue. */
  partsDropped: number;
  /** Lines read, by native type name. Uploaded on iOS, queued (then uploaded) on Android. */
  linesByType: Record<string, number>;
  /** Deletions sent (iOS) or queued (Android), by native type name. */
  deletionsByType: Record<string, number>;
  /** iOS: deletions kept for a later sync, until the server says it applies them. */
  deletionsPendingByType: Record<string, number>;
  /** Records left out because the server does not store them as they are. */
  skippedRecords: number;
  /** Android: types skipped because their read permission is not granted. */
  typesWithoutPermission: HealthDataType[];
  /** Android: types read again over a window because their changes token had expired. */
  expiredChangesTokenTypes: HealthDataType[];
  /**
   * iOS: native type names that were not synced because an upload failed before their
   * turn. Their anchors did not move, so `flush` or the next `sync` sends them.
   */
  pendingTypes: string[];
  /** Requested native type names this OS version does not have. */
  unavailableTypes: string[];
  /** Why the upload stopped with data left behind; null when everything went up. */
  uploadError: UploadError | null;
  /** True when nothing is left behind: no upload error, no pending part, no pending type. */
  complete: boolean;
}

/** What `requestAuthorization` learned. */
export interface AuthorizationResult {
  platform: HealthPlatform;
  /** The types asked for that this platform has. */
  requested: HealthDataType[];
  /**
   * Android: the requested types the user granted. `null` on iOS, where HealthKit never
   * tells an app whether read access was granted (a declined type just returns nothing).
   */
  granted: HealthDataType[] | null;
  /** Android: whether reading in the background is granted; `null` when not asked, and on iOS. */
  backgroundGranted: boolean | null;
  /** Android: whether data older than 30 days is granted; `null` when not asked, and on iOS. */
  historyGranted: boolean | null;
}

/** The result of one background sync run. */
export type BackgroundSyncEvent =
  | { ok: true; /** RFC 3339 time of the run. */ at: string; report: SyncReport }
  | { ok: false; at: string; error: { code: HealthSyncErrorCode; message: string } };

// Decoding

type Json = Record<string, unknown>;

function fail(what: string): never {
  throw new HealthSyncError("decode", `unexpected ${what} from the native module`);
}

function parseObject(json: string, what: string): Json {
  let v: unknown;
  try {
    v = JSON.parse(json);
  } catch {
    fail(what);
  }
  if (typeof v !== "object" || v === null || Array.isArray(v)) fail(what);
  return v as Json;
}

function count(o: Json, key: string): number {
  const v = o[key];
  if (v === undefined || v === null) return 0;
  if (typeof v !== "number" || !Number.isInteger(v) || v < 0) fail(`"${key}"`);
  return v;
}

function strings(o: Json, key: string): string[] {
  const v = o[key];
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v) || v.some((s) => typeof s !== "string")) fail(`"${key}"`);
  return v as string[];
}

function counts(o: Json, key: string): Record<string, number> {
  const v = o[key];
  if (v === undefined || v === null) return {};
  if (typeof v !== "object" || Array.isArray(v)) fail(`"${key}"`);
  const out: Record<string, number> = {};
  for (const [name, n] of Object.entries(v as Json)) {
    if (typeof n !== "number" || !Number.isInteger(n) || n < 0) fail(`"${key}"`);
    out[name] = n;
  }
  return out;
}

function flag(o: Json, key: string): boolean | null {
  const v = o[key];
  if (v === undefined || v === null) return null;
  if (typeof v !== "boolean") fail(`"${key}"`);
  return v;
}

function dataTypes(names: string[], platform: HealthPlatform): HealthDataType[] {
  const out: HealthDataType[] = [];
  for (const name of names) {
    for (const type of dataTypesOfNativeName(name, platform)) {
      if (!out.includes(type)) out.push(type);
    }
  }
  return out;
}

function uploadError(v: unknown): UploadError | null {
  if (v === undefined || v === null) return null;
  if (typeof v !== "object" || Array.isArray(v)) fail('"uploadError"');
  const o = v as Json;
  const code = typeof o.code === "string" && UPLOAD_ERROR_CODES.includes(o.code) ? (o.code as UploadErrorCode) : "unknown";
  const out: UploadError = { code, message: typeof o.message === "string" ? o.message : "" };
  if (typeof o.status === "number") out.status = o.status;
  if (typeof o.requestId === "string" && o.requestId !== "") out.requestId = o.requestId;
  return out;
}

function reportOf(o: Json, platform: HealthPlatform): SyncReport {
  const partsPending = count(o, "partsPending");
  const pendingTypes = strings(o, "pendingTypes");
  const error = uploadError(o.uploadError);
  return {
    platform,
    batchIds: strings(o, "batchIds"),
    partsUploaded: count(o, "partsUploaded"),
    duplicateParts: count(o, "duplicateParts"),
    partsPending,
    partsDropped: count(o, "partsDropped"),
    linesByType: counts(o, "linesByType"),
    deletionsByType: counts(o, "deletionsByType"),
    deletionsPendingByType: counts(o, "deletionsPendingByType"),
    skippedRecords: count(o, "skippedRecords"),
    typesWithoutPermission: dataTypes(strings(o, "typesWithoutPermission"), platform),
    expiredChangesTokenTypes: dataTypes(strings(o, "expiredTokenTypes"), platform),
    pendingTypes,
    unavailableTypes: strings(o, "unavailableTypes"),
    uploadError: error,
    complete: error === null && partsPending === 0 && pendingTypes.length === 0,
  };
}

/** Reads the JSON a native `sync` or `flush` resolved. */
export function decodeSyncReport(json: string, platform: HealthPlatform): SyncReport {
  return reportOf(parseObject(json, "sync report"), platform);
}

/** Reads the JSON a native `requestAuthorization` resolved. */
export function decodeAuthorization(json: string, platform: HealthPlatform, requested: HealthDataType[]): AuthorizationResult {
  const o = parseObject(json, "authorization result");
  const granted = o.granted === undefined || o.granted === null ? null : dataTypes(strings(o, "granted"), platform);
  return {
    platform,
    requested,
    // A type made of several native types (blood pressure on iOS) counts when asked for.
    granted: granted === null ? null : granted.filter((t) => requested.includes(t)),
    backgroundGranted: flag(o, "background"),
    historyGranted: flag(o, "history"),
  };
}

/** Reads the JSON of a background sync result. */
export function decodeBackgroundSync(json: string, platform: HealthPlatform): BackgroundSyncEvent {
  const o = parseObject(json, "background sync result");
  const at = typeof o.at === "string" ? o.at : fail('"at"');
  if (o.ok === true) {
    if (typeof o.report !== "object" || o.report === null || Array.isArray(o.report)) fail('"report"');
    return { ok: true, at, report: reportOf(o.report as Json, platform) };
  }
  if (o.ok !== false) fail('"ok"');
  const e = HealthSyncError.from(o.error);
  return { ok: false, at, error: { code: e.code, message: e.message } };
}
