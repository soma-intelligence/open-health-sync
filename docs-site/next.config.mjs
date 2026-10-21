import { readFileSync } from "node:fs";
import { createMDX } from "fumadocs-mdx/next";

const withMDX = createMDX();

// NEXT_PUBLIC_BASE_PATH serves the site under a path, "/open-health-sync" in production, where
// another site forwards that path here (README, "Deploying"). Leave it empty to serve it at the
// root of a domain.
const basePath = process.env.NEXT_PUBLIC_BASE_PATH || undefined;

// DOCS_AT_ROOT=true serves the guides at the root of the site (with NEXT_PUBLIC_BASE_PATH=/docs:
// /docs/quickstart) and leaves out this site's home page, for a domain whose own site shows the
// project page at "/". Otherwise the home page is at "/" and the guides under /documentation.
// Each build keeps only the route files tagged for it: page.root.tsx or page.nested.tsx.
const docsAtRoot = process.env.DOCS_AT_ROOT === "true";

// The release the pages talk about: the default image tag of the compose file, which the
// release workflow checks against each tag.
const ohsVersion = /OHS_VERSION:-([^}]+)\}/.exec(readFileSync("../deploy/docker-compose.yml", "utf8"))?.[1];

/** @type {import('next').NextConfig} */
const config = {
  output: "export",
  reactStrictMode: true,
  basePath,
  pageExtensions: [docsAtRoot ? "root.tsx" : "nested.tsx", "mdx", "md", "jsx", "js", "tsx", "ts"],
  // Addresses without a trailing slash, like the site that forwards to this one: with one, each
  // would redirect the other way and loop.
  trailingSlash: false,
  images: { unoptimized: true },
  env: { NEXT_PUBLIC_OHS_VERSION: ohsVersion ?? "", NEXT_PUBLIC_DOCS_ROUTE: docsAtRoot ? "" : "/documentation" },
};

export default withMDX(config);
