// SPDX-License-Identifier: AGPL-3.0-only

// The entry point `open-health-sync-react-native/health`. It is apart from the package's
// main entry because it imports React Native: the main entry stays usable in Node and in
// browsers.

import { Platform } from "react-native";

import NativeOpenHealthSync from "./NativeOpenHealthSync.ts";
import { type HealthSync, createHealthSync } from "./sync.ts";

/** Reads HealthKit (iOS) or Health Connect (Android) and uploads what changed. */
export const healthSync: HealthSync = createHealthSync({ native: NativeOpenHealthSync ?? null, platform: Platform.OS });

export {
  createHealthSync,
  type HealthSync,
  type HealthAvailability,
  type HealthSyncCredentials,
  type AuthorizationOptions,
  type BackgroundFrequency,
  type BackgroundSyncOptions,
  type FlushOptions,
  type NativeHealthModule,
} from "./sync.ts";
export {
  HealthSyncError,
  type HealthSyncErrorCode,
  type UploadError,
  type UploadErrorCode,
  type SyncReport,
  type AuthorizationResult,
  type BackgroundSyncEvent,
} from "./report.ts";
export {
  HEALTH_DATA_TYPES,
  ALL_HEALTH_DATA_TYPES,
  isTypeSupported,
  supportedTypes,
  type HealthDataType,
  type HealthPlatform,
  type PlatformSupport,
} from "./types.ts";
