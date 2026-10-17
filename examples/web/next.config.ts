// SPDX-License-Identifier: AGPL-3.0-only

import { fileURLToPath } from "node:url";
import type { NextConfig } from "next";

// The server SDK is a "file:" dependency: node_modules/@open-health-sync/server links to
// ../../sdks/server/typescript. The bundler reads only files under its root, so the root is
// the repository, two folders up.
const root = fileURLToPath(new URL("../..", import.meta.url));

const config: NextConfig = {
  turbopack: { root },
  outputFileTracingRoot: root,
  poweredByHeader: false,
};

export default config;
