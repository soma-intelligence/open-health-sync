import { readFileSync } from "node:fs";
import { createMDX } from "fumadocs-mdx/next";

const withMDX = createMDX();

// NEXT_PUBLIC_BASE_PATH serves the site under a path, for example "/open-health-sync" on a
// GitHub Pages project site. Leave it empty to serve it at the root of a domain.
const basePath = process.env.NEXT_PUBLIC_BASE_PATH || undefined;

// The release the pages talk about: the default image tag of the compose file, which the
// release workflow checks against each tag.
const ohsVersion = /OHS_VERSION:-([^}]+)\}/.exec(readFileSync("../deploy/docker-compose.yml", "utf8"))?.[1];

/** @type {import('next').NextConfig} */
const config = {
  output: "export",
  reactStrictMode: true,
  basePath,
  trailingSlash: true,
  images: { unoptimized: true },
  env: { NEXT_PUBLIC_OHS_VERSION: ohsVersion ?? "" },
};

export default withMDX(config);
