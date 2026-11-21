// SPDX-License-Identifier: AGPL-3.0-only

import {
  type AuthorizationResult,
  type BackgroundSyncEvent,
  type SyncReport,
  HealthSyncError,
  decodeAuthorization,
  decodeBackgroundSync,
  decodeSyncReport,
} from "./report.ts";
import { type HealthDataType, type HealthPlatform, EVERY_TYPE, nativeTypeNames, supportedTypes } from "./types.ts";

/**
 * The native module as this file uses it: the shape of the codegen spec
 * (NativeOpenHealthSync.ts) without a dependency on React Native, so tests pass a fake.
 */
export interface NativeHealthModule {
  availability(): Promise<string>;
  configure(baseUrl: string, userToken: string): Promise<void>;
  clearCredentials(): Promise<void>;
  isConfigured(): Promise<boolean>;
  requestAuthorization(types: string[], background: boolean, history: boolean): Promise<string>;
  sync(types: string[]): Promise<string>;
  flush(force: boolean): Promise<string>;
  enableBackgroundSync(
    types: string[],
    intervalMinutes: number,
    requireUnmeteredNetwork: boolean,
    frequency: string,
  ): Promise<void>;
  disableBackgroundSync(): Promise<void>;
  pendingParts(): Promise<number>;
  lastBackgroundSync(): Promise<string>;
  onBackgroundSync(handler: (payload: { json: string }) => void): { remove(): void };
}

/** Whether the health store can be used. */
export type HealthAvailability =
  | "available"
  /** Android 13 and lower: the Health Connect app must be installed or updated first. */
  | "providerUpdateRequired"
  | "unavailable";

/** Where to upload, and as whom. */
export interface HealthSyncCredentials {
  /** Base URL of the instance, `https://ohs.example.org`. */
  baseUrl: string;
  /** A user token (`ohs_ut_`) with the `upload` scope, minted by your backend. Never the tenant key. */
  userToken: string;
}

export interface AuthorizationOptions {
  /** Android: also ask to read in the background, which background sync needs. */
  background?: boolean;
  /** Android: also ask for data older than 30 days before the first grant. */
  history?: boolean;
}

/** How often iOS is asked to wake the app for new samples. iOS decides in the end. */
export type BackgroundFrequency = "immediate" | "hourly" | "daily" | "weekly";

export interface BackgroundSyncOptions {
  /** The types to sync in the background; every type the server stores when left out. */
  types?: readonly HealthDataType[];
  /** Android: minutes between two runs, 15 at least (WorkManager's floor). 60 when left out. */
  intervalMinutes?: number;
  /** Android: run only on an unmetered network. */
  requireUnmeteredNetwork?: boolean;
  /** iOS: how often HealthKit should wake the app. `immediate` when left out. */
  frequency?: BackgroundFrequency;
}

export interface FlushOptions {
  /** Android: send now, without waiting for the retry time of a part that failed before. */
  force?: boolean;
}

/** Reads HealthKit (iOS) or Health Connect (Android) and uploads what changed. */
export interface HealthSync {
  /** `ios` or `android` when the native module is linked on that platform, else `null`. */
  readonly platform: HealthPlatform | null;
  /** Whether the health store can be used here. Never rejects. */
  isAvailable(): Promise<boolean>;
  /** Like `isAvailable`, and tells when Health Connect only needs an install or update. */
  availability(): Promise<HealthAvailability>;
  /**
   * Hands the base URL and the user token to the native side, which keeps them (Keychain on
   * iOS, a Keystore-encrypted file on Android) so a background run can upload without
   * JavaScript. Call it again with a new token when the old one expires.
   */
  configure(credentials: HealthSyncCredentials): Promise<void>;
  /** Whether credentials are kept on the native side. */
  isConfigured(): Promise<boolean>;
  /** Removes the kept credentials and turns background sync off. Call it at sign-out. */
  clearCredentials(): Promise<void>;
  /** Shows the system permission screen for `types` (every type when left out). */
  requestAuthorization(types?: readonly HealthDataType[], options?: AuthorizationOptions): Promise<AuthorizationResult>;
  /**
   * Reads what changed since the last sync of `types` (every type when left out) and
   * uploads it. An upload failure does not reject: the report carries `uploadError` and
   * what is left behind.
   */
  sync(types?: readonly HealthDataType[]): Promise<SyncReport>;
  /** Sends what an earlier sync left behind, without reading everything again. */
  flush(options?: FlushOptions): Promise<SyncReport>;
  /** Keeps syncing while the app is closed. Call it once permissions are granted. */
  enableBackgroundSync(options?: BackgroundSyncOptions): Promise<void>;
  /** Stops background sync. What is queued is kept. */
  disableBackgroundSync(): Promise<void>;
  /** Android: parts waiting in the queue on disk. Always 0 on iOS. */
  pendingParts(): Promise<number>;
  /** The result of the last background run, kept across restarts; `null` when none ran. */
  lastBackgroundSync(): Promise<BackgroundSyncEvent | null>;
  /** Calls `listener` after each background run that happens while JavaScript is alive. */
  addBackgroundSyncListener(listener: (event: BackgroundSyncEvent) => void): { remove(): void };
}

const FREQUENCIES: readonly string[] = ["immediate", "hourly", "daily", "weekly"];
const DEFAULT_INTERVAL_MINUTES = 60;
const MIN_INTERVAL_MINUTES = 15;

function invalid(message: string): never {
  throw new HealthSyncError("invalid_argument", message);
}

/**
 * Builds the health sync API over a native module. `native` is null where the module is not
 * linked: `isAvailable` then answers false and every other call rejects with `not_linked`.
 * The package's `healthSync` is this function applied to the real module.
 */
export function createHealthSync(deps: { native: NativeHealthModule | null; platform: string }): HealthSync {
  const platform: HealthPlatform | null =
    deps.native !== null && (deps.platform === "ios" || deps.platform === "android") ? deps.platform : null;

  function linked(): { native: NativeHealthModule; platform: HealthPlatform } {
    if (deps.native === null || platform === null) {
      throw new HealthSyncError(
        "not_linked",
        "the Open Health Sync native module is not in this build (it needs iOS or Android, the New Architecture, and a native build: not Expo Go)",
      );
    }
    return { native: deps.native, platform };
  }

  /** The names to give the native side for `types`; `undefined` is every type. */
  function names(types: readonly HealthDataType[] | undefined, p: HealthPlatform): string[] {
    if (types === undefined) return [EVERY_TYPE];
    if (types.length === 0) invalid("types must not be empty; leave it out to mean every type");
    let out: string[];
    try {
      out = nativeTypeNames(types, p);
    } catch (e) {
      invalid(e instanceof Error ? e.message : "unknown health data type");
    }
    if (out.length === 0) invalid(`none of the given types exists on ${p}`);
    return out;
  }

  async function call<T>(run: (n: NativeHealthModule, p: HealthPlatform) => Promise<T>): Promise<T> {
    const l = linked();
    try {
      return await run(l.native, l.platform);
    } catch (e) {
      throw HealthSyncError.from(e);
    }
  }

  async function availability(): Promise<HealthAvailability> {
    if (deps.native === null || platform === null) return "unavailable";
    try {
      const a = await deps.native.availability();
      return a === "available" || a === "providerUpdateRequired" ? a : "unavailable";
    } catch {
      return "unavailable";
    }
  }

  return {
    platform,

    availability,

    async isAvailable() {
      return (await availability()) === "available";
    },

    configure(credentials) {
      return call(async (n) => {
        const baseUrl = typeof credentials?.baseUrl === "string" ? credentials.baseUrl.trim().replace(/\/+$/, "") : "";
        const userToken = typeof credentials?.userToken === "string" ? credentials.userToken.trim() : "";
        if (!/^https?:\/\/[^\s/]+/i.test(baseUrl)) invalid("baseUrl must be an http or https URL");
        if (userToken === "") invalid("userToken must not be empty");
        // The tenant key reads and deletes every user's data: it must never reach an app.
        if (userToken.startsWith("ohs_sk_")) {
          invalid("userToken is a tenant key; an app must hold a user token with the upload scope");
        }
        await n.configure(baseUrl, userToken);
      });
    },

    isConfigured() {
      return call((n) => n.isConfigured());
    },

    clearCredentials() {
      return call((n) => n.clearCredentials());
    },

    requestAuthorization(types, options) {
      return call(async (n, p) => {
        const requested = (types === undefined ? supportedTypes(p) : [...new Set(types)]).filter(
          (t) => nativeTypeNames([t], p).length > 0,
        );
        const json = await n.requestAuthorization(names(types, p), options?.background === true, options?.history === true);
        return decodeAuthorization(json, p, requested);
      });
    },

    sync(types) {
      return call(async (n, p) => decodeSyncReport(await n.sync(names(types, p)), p));
    },

    flush(options) {
      return call(async (n, p) => decodeSyncReport(await n.flush(options?.force === true), p));
    },

    enableBackgroundSync(options) {
      return call(async (n, p) => {
        const interval = options?.intervalMinutes ?? DEFAULT_INTERVAL_MINUTES;
        if (!Number.isFinite(interval) || interval <= 0) invalid("intervalMinutes must be a positive number");
        const frequency = options?.frequency ?? "immediate";
        if (!FREQUENCIES.includes(frequency)) invalid("frequency must be immediate, hourly, daily or weekly");
        await n.enableBackgroundSync(
          names(options?.types, p),
          Math.max(MIN_INTERVAL_MINUTES, Math.round(interval)),
          options?.requireUnmeteredNetwork === true,
          frequency,
        );
      });
    },

    disableBackgroundSync() {
      return call((n) => n.disableBackgroundSync());
    },

    pendingParts() {
      return call((n) => n.pendingParts());
    },

    lastBackgroundSync() {
      return call(async (n, p) => {
        const json = await n.lastBackgroundSync();
        return json === "" ? null : decodeBackgroundSync(json, p);
      });
    },

    addBackgroundSyncListener(listener) {
      const l = linked();
      return l.native.onBackgroundSync((payload) => {
        let event: BackgroundSyncEvent;
        try {
          event = decodeBackgroundSync(payload.json, l.platform);
        } catch {
          // A result this version cannot read is not worth crashing the app for.
          return;
        }
        listener(event);
      });
    },
  };
}
