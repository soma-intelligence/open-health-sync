// SPDX-License-Identifier: AGPL-3.0-only

// What the Expo config plugin (../app.plugin.js) writes into the native projects, as plain
// functions over the parsed files so they are tested without Expo.

/**
 * The Health Connect read permission of each data type Android has
 * (`android.permission.health.` + the value). The keys are the Android names of
 * src/health/types.ts; a test keeps the two lists equal.
 */
export const ANDROID_READ_PERMISSIONS = {
  steps: "READ_STEPS",
  heartRate: "READ_HEART_RATE",
  restingHeartRate: "READ_RESTING_HEART_RATE",
  heartRateVariability: "READ_HEART_RATE_VARIABILITY",
  oxygenSaturation: "READ_OXYGEN_SATURATION",
  respiratoryRate: "READ_RESPIRATORY_RATE",
  bodyTemperature: "READ_BODY_TEMPERATURE",
  bloodPressure: "READ_BLOOD_PRESSURE",
  bloodGlucose: "READ_BLOOD_GLUCOSE",
  weight: "READ_WEIGHT",
  height: "READ_HEIGHT",
  bodyFat: "READ_BODY_FAT",
  activeEnergy: "READ_ACTIVE_CALORIES_BURNED",
  distance: "READ_DISTANCE",
  sleep: "READ_SLEEP",
  workouts: "READ_EXERCISE",
  menstrualFlow: "READ_MENSTRUATION",
  intermenstrualBleeding: "READ_INTERMENSTRUAL_BLEEDING",
  nutrition: "READ_NUTRITION",
  hydration: "READ_HYDRATION",
  totalEnergy: "READ_TOTAL_CALORIES_BURNED",
  basalMetabolicRate: "READ_BASAL_METABOLIC_RATE",
};

const PREFIX = "android.permission.health.";
const RATIONALE_ACTION = "androidx.health.ACTION_SHOW_PERMISSIONS_RATIONALE";
const USAGE_ALIAS = "ViewPermissionUsageActivity";

/** The lowest Android API level Health Connect runs on. */
export const MIN_SDK_VERSION = 26;

/**
 * @typedef {object} PluginProps
 * @property {string} [healthShareUsageDescription] iOS: the sentence shown when access is asked.
 * @property {boolean} [backgroundDelivery] iOS: add the background delivery entitlement (default true).
 * @property {string[]} [types] Android: the data types whose read permission is declared
 *   (default: every type Android has). A type only iOS has is ignored.
 * @property {boolean} [backgroundRead] Android: declare READ_HEALTH_DATA_IN_BACKGROUND (default true).
 * @property {boolean} [history] Android: declare READ_HEALTH_DATA_HISTORY (default false).
 * @property {string} [rationaleActivity] Android: the activity that shows your privacy
 *   policy when Health Connect asks why the app wants the data (default `.MainActivity`).
 */

/**
 * The permissions the Android manifest must declare.
 * @param {PluginProps} [props]
 * @returns {string[]}
 */
export function androidPermissions(props = {}) {
  const types = props.types ?? Object.keys(ANDROID_READ_PERMISSIONS);
  const out = [];
  for (const type of types) {
    const permission = ANDROID_READ_PERMISSIONS[type];
    if (permission !== undefined && !out.includes(PREFIX + permission)) out.push(PREFIX + permission);
  }
  if (props.backgroundRead !== false) out.push(PREFIX + "READ_HEALTH_DATA_IN_BACKGROUND");
  if (props.history === true) out.push(PREFIX + "READ_HEALTH_DATA_HISTORY");
  return out;
}

/**
 * Adds the usage sentence iOS shows when it asks for access. A sentence already in the
 * file is kept unless one is given.
 * @param {Record<string, unknown>} infoPlist
 * @param {PluginProps} [props]
 */
export function applyInfoPlist(infoPlist, props = {}) {
  const sentence = props.healthShareUsageDescription ?? infoPlist.NSHealthShareUsageDescription;
  infoPlist.NSHealthShareUsageDescription = sentence ?? "Allow $(PRODUCT_NAME) to read your health data to sync it.";
  return infoPlist;
}

/**
 * Adds the HealthKit entitlement, and the background delivery one unless turned off.
 * @param {Record<string, unknown>} entitlements
 * @param {PluginProps} [props]
 */
export function applyEntitlements(entitlements, props = {}) {
  entitlements["com.apple.developer.healthkit"] = true;
  if (props.backgroundDelivery !== false) {
    entitlements["com.apple.developer.healthkit.background-delivery"] = true;
  }
  return entitlements;
}

/**
 * Declares the read permissions and the screen Health Connect opens to explain them: the
 * rationale intent filter on the activity (Android 13 and lower) and an activity alias for
 * the permission usage intent (Android 14 and higher). Running it twice changes nothing.
 * @param {{ manifest: Record<string, any> }} androidManifest the manifest as Expo parses it (xml2js)
 * @param {PluginProps} [props]
 */
export function applyAndroidManifest(androidManifest, props = {}) {
  const manifest = androidManifest.manifest;
  const name = (node) => node?.$?.["android:name"];

  const declared = (manifest["uses-permission"] ??= []);
  for (const permission of androidPermissions(props)) {
    if (!declared.some((p) => name(p) === permission)) declared.push({ $: { "android:name": permission } });
  }

  const application = manifest.application?.[0];
  if (application === undefined) return androidManifest;
  const target = props.rationaleActivity ?? ".MainActivity";

  const activity = (application.activity ?? []).find((a) => name(a) === target);
  if (activity !== undefined) {
    const filters = (activity["intent-filter"] ??= []);
    if (!filters.some((f) => (f.action ?? []).some((a) => name(a) === RATIONALE_ACTION))) {
      filters.push({ action: [{ $: { "android:name": RATIONALE_ACTION } }] });
    }
  }

  const aliases = (application["activity-alias"] ??= []);
  if (!aliases.some((a) => name(a) === USAGE_ALIAS)) {
    aliases.push({
      $: {
        "android:name": USAGE_ALIAS,
        "android:exported": "true",
        "android:permission": "android.permission.START_VIEW_PERMISSION_USAGE",
        "android:targetActivity": target,
      },
      "intent-filter": [
        {
          action: [{ $: { "android:name": "android.intent.action.VIEW_PERMISSION_USAGE" } }],
          category: [{ $: { "android:name": "android.intent.category.HEALTH_PERMISSIONS" } }],
        },
      ],
    });
  }
  return androidManifest;
}

/**
 * Raises `android.minSdkVersion` in gradle.properties to what Health Connect needs.
 * @param {{ type: string, key?: string, value?: string }[]} properties the file as Expo parses it
 */
export function applyGradleProperties(properties) {
  const current = properties.find((p) => p.type === "property" && p.key === "android.minSdkVersion");
  if (current === undefined) {
    properties.push({ type: "property", key: "android.minSdkVersion", value: String(MIN_SDK_VERSION) });
  } else if (!(Number.parseInt(current.value ?? "", 10) >= MIN_SDK_VERSION)) {
    current.value = String(MIN_SDK_VERSION);
  }
  return properties;
}
