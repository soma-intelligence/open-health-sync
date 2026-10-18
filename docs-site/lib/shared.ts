import { createGetUrl } from "fumadocs-core/source";

export const appName = "Open Health Sync";
/** Where the guides start inside the site: "" when they are at its root (next.config.mjs). */
export const docsRoute = process.env.NEXT_PUBLIC_DOCS_ROUTE ?? "/documentation";

/**
 * A link written as /documentation/... in a page, moved to where the guides are served. Pages
 * synced from docs/ and the hand-written ones link that way.
 */
export function docsHref(href: string): string {
  const m = /^\/documentation(?=[/#?]|$)/.exec(href);
  if (!m) return href;
  const out = docsRoute + href.slice(m[0].length);
  return out.startsWith("/") ? out : `/${out}`;
}
export const docsImageRoute = "/og/docs";
export const docsContentRoute = "/llms.mdx/docs";

// Written whole: the repository checks only allow the organization name as part of it.
export const repoUrl = "https://github.com/soma-intelligence/open-health-sync";
const branch = "main";

/** The hosted beta's waitlist, on the project's short address. */
export const waitlistUrl = "https://openhealthsync.com/waitlist";

/** The current release, read from deploy/docker-compose.yml by next.config.mjs. */
export const latestVersion = process.env.NEXT_PUBLIC_OHS_VERSION ?? "";

const getContentUrl = createGetUrl(docsContentRoute);

export function getPageMarkdownUrl(page: { slugs: string[]; locale?: string }) {
  const segments = [...page.slugs, "content.md"];

  return { segments, url: getContentUrl(segments, page.locale) };
}

const getImageUrl = createGetUrl(docsImageRoute);

export function getPageImageUrl(page: { slugs: string[]; locale?: string }) {
  const segments = [...page.slugs, "image.png"];

  return { segments, url: getImageUrl(segments, page.locale) };
}

/** Where a page's text is edited: its file under docs/ when it is synced from there. */
export function getPageEditUrl(page: { path: string; data: { source?: string } }) {
  const file = page.data.source ?? `docs-site/content/docs/${page.path}`;
  return `${repoUrl}/blob/${branch}/${file}`;
}
