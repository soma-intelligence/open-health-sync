// SPDX-License-Identifier: AGPL-3.0-only

// The Expo config plugin. In app.json:
//
//   "plugins": [["open-health-sync-react-native", { "healthShareUsageDescription": "..." }]]
//
// It writes what the native module needs into the projects `expo prebuild` generates: the
// HealthKit usage sentence and entitlements on iOS, the Health Connect permissions, the
// rationale screen and minSdk 26 on Android. The options are PluginProps in plugin/config.js.

import { createRequire } from "node:module";
import path from "node:path";

import { applyAndroidManifest, applyEntitlements, applyGradleProperties, applyInfoPlist } from "./plugin/config.js";

/** Expo's own helpers, taken from the app (this package does not depend on Expo). */
function expoConfigPlugins(config) {
  const projectRoot = config?._internal?.projectRoot ?? process.cwd();
  return createRequire(path.join(projectRoot, "package.json"))("expo/config-plugins");
}

export default function withOpenHealthSync(config, props = {}) {
  const { withInfoPlist, withEntitlementsPlist, withAndroidManifest, withGradleProperties } = expoConfigPlugins(config);
  config = withInfoPlist(config, (c) => {
    applyInfoPlist(c.modResults, props);
    return c;
  });
  config = withEntitlementsPlist(config, (c) => {
    applyEntitlements(c.modResults, props);
    return c;
  });
  config = withAndroidManifest(config, (c) => {
    applyAndroidManifest(c.modResults, props);
    return c;
  });
  config = withGradleProperties(config, (c) => {
    applyGradleProperties(c.modResults);
    return c;
  });
  return config;
}
