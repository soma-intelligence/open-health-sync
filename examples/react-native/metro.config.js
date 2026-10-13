// SPDX-License-Identifier: AGPL-3.0-only

// The SDK is installed from ../../sdks/react-native (a "file:" dependency, so a symlink in
// node_modules). Metro only sees files inside its watch folders, so add the SDK folder, and
// let modules imported from the SDK source (fflate) resolve from this app's node_modules.
const path = require("path");
const { getDefaultConfig } = require("expo/metro-config");

const projectRoot = __dirname;
const sdkRoot = path.resolve(projectRoot, "../../sdks/react-native");

const config = getDefaultConfig(projectRoot);

config.watchFolders = [...(config.watchFolders ?? []), sdkRoot];
config.resolver.nodeModulesPaths = [path.resolve(projectRoot, "node_modules")];

module.exports = config;
