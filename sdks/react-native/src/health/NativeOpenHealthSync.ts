// SPDX-License-Identifier: AGPL-3.0-only

// The codegen spec of the native module (New Architecture). React Native generates the
// Kotlin and Objective-C interfaces from this file, so it only uses the types codegen knows.
// Reports cross the bridge as JSON text, decoded and checked in report.ts.

import type { CodegenTypes, TurboModule } from "react-native";
import { TurboModuleRegistry } from "react-native";

export type BackgroundSyncPayload = {
  /** A background sync result as JSON (see report.ts). */
  json: string;
};

export interface Spec extends TurboModule {
  /** `available`, `providerUpdateRequired` or `unavailable`. */
  availability(): Promise<string>;
  /** Keeps the base URL and the user token on the native side (Keychain, Android Keystore). */
  configure(baseUrl: string, userToken: string): Promise<void>;
  clearCredentials(): Promise<void>;
  isConfigured(): Promise<boolean>;
  /** Resolves the authorization result as JSON. */
  requestAuthorization(types: Array<string>, background: boolean, history: boolean): Promise<string>;
  /** Resolves the sync report as JSON. */
  sync(types: Array<string>): Promise<string>;
  /** Resolves the sync report as JSON. */
  flush(force: boolean): Promise<string>;
  enableBackgroundSync(
    types: Array<string>,
    intervalMinutes: number,
    requireUnmeteredNetwork: boolean,
    frequency: string,
  ): Promise<void>;
  disableBackgroundSync(): Promise<void>;
  pendingParts(): Promise<number>;
  /** The last background sync result as JSON, or an empty string when there is none. */
  lastBackgroundSync(): Promise<string>;
  readonly onBackgroundSync: CodegenTypes.EventEmitter<BackgroundSyncPayload>;
}

// `get`, not `getEnforcing`: the module is missing in Expo Go, on the web and in tests, and
// importing this package must not throw there.
export default TurboModuleRegistry.get<Spec>("OpenHealthSync");
