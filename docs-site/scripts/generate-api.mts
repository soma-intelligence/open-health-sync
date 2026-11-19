// Generates one page per operation of ../api/openapi.yaml, grouped by tag. The output is
// git-ignored and rebuilt by `npm run dev` and `npm run build`.

import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { generateFiles } from "fumadocs-openapi";
import { parse } from "yaml";
import { openapi } from "../lib/openapi.ts";

const output = "./content/docs/api-reference/endpoints";

// The tags come from the OpenAPI document, so a new tag reaches the sidebar without an edit here.
// These go first, in this order; every other tag follows in the order the document lists it.
const preferred = ["users", "tokens", "providers", "connections", "ingest", "read", "mcp", "provider-facing", "ops"];

// Sidebar titles where a capitalized tag name reads badly. An `x-displayName` in the document wins.
const labels: Record<string, string> = {
  read: "Read data",
  mcp: "MCP",
  "provider-facing": "Provider-facing",
  ops: "Operations",
  "self-reports": "Self-reports",
  fhir: "FHIR R4",
  omh: "Open mHealth",
  admin: "Instance admin",
  audit: "Audit log",
};

// Sidebar sections after the preferred tags. A tag not named here goes under "More endpoints".
const sections: Record<string, string> = {
  tenant: "Administration",
  pipeline: "Administration",
  admin: "Administration",
  audit: "Administration",
  console: "Administration",
};
const otherSection = "More endpoints";

// A tag with more pages than this gets separators by resource inside its folder.
const subgroupAbove = 20;

type Tag = { name: string; "x-displayName"?: string };
const doc = parse(readFileSync(join(process.cwd(), "..", "api", "openapi.yaml"), "utf8")) as { tags?: Tag[] };
const declared = (doc.tags ?? []).map((t) => t.name);
const titleOf = (tag: string) =>
  doc.tags?.find((t) => t.name === tag)?.["x-displayName"] ??
  labels[tag] ??
  tag.charAt(0).toUpperCase() + tag.slice(1).replaceAll("-", " ");

const kebab = (s: string) => s.replace(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase();

// The route of each generated page, by tag, to split long folders by resource.
const routes = new Map<string, { name: string; route: string }[]>();

rmSync(output, { recursive: true, force: true });

await generateFiles({
  input: openapi,
  output,
  per: "operation",
  groupBy: "tag",
  includeDescription: true,
  beforeWrite(files) {
    for (const file of files) {
      file.path = file.path.replace(/[^/]+(?=\.mdx$)/, kebab);
      // "Edit on GitHub" opens the OpenAPI file, the page's real source.
      file.content = file.content.replace(/^---\n/, '---\nsource: "api/openapi.yaml"\n');
      const m = /^(?:.*\/)?([^/]+)\/([^/]+)\.mdx$/.exec(file.path);
      const route = /"path":"([^"]+)"/.exec(file.content)?.[1];
      if (!m || !route) continue;
      const list = routes.get(m[1]) ?? [];
      list.push({ name: m[2], route });
      routes.set(m[1], list);
    }
  },
});

// Every folder written, in sidebar order: the preferred tags, the other declared tags, then any
// tag an operation uses without declaring it.
const written = readdirSync(output, { withFileTypes: true })
  .filter((e) => e.isDirectory())
  .map((e) => e.name);
const order = [...new Set([...preferred, ...declared, ...written.sort()])].filter((tag) => written.includes(tag));

// The preferred tags under the "Endpoints" heading of api-reference/meta.json, then one section per
// group, each in document order.
const rest = order.filter((t) => !preferred.includes(t));
const sectionOf = (tag: string) => sections[tag] ?? otherSection;
const grouped = order.filter((t) => preferred.includes(t));
for (const name of new Set(rest.map(sectionOf))) {
  grouped.push(`---${name}---`, ...rest.filter((t) => sectionOf(t) === name));
}
writeFileSync(join(output, "meta.json"), JSON.stringify({ pages: grouped }, null, 2));

// The resource a route acts on: /v1/admin/tenants/{tenant_id}/webhooks/{id} is "webhooks".
const resourceOf = (route: string) => {
  const parts = route
    .replace(/^\/v\d+\//, "")
    .replace(/^admin\//, "")
    .split("/")
    .filter((p) => p && !p.startsWith("{"));
  const rest = parts[0] === "tenants" && parts.length > 1 ? parts.slice(1) : parts;
  return rest[0] ?? "other";
};

for (const tag of order) {
  const meta: Record<string, unknown> = { title: titleOf(tag), defaultOpen: tag === "users" };
  const list = routes.get(tag) ?? [];
  if (list.length > subgroupAbove) {
    const byResource = new Map<string, string[]>();
    for (const { name, route } of list) {
      const key = resourceOf(route);
      byResource.set(key, [...(byResource.get(key) ?? []), name]);
    }
    // Resources with one or two routes share an "Other" group, so separators stay meaningful.
    const other: string[] = [];
    const groups: [string, string[]][] = [];
    for (const [key, names] of [...byResource].sort(([a], [b]) => a.localeCompare(b))) {
      if (names.length < 3) other.push(...names);
      else groups.push([key.charAt(0).toUpperCase() + key.slice(1).replaceAll("-", " "), names.sort()]);
    }
    if (other.length) groups.push(["Other", other.sort()]);
    meta.pages = groups.flatMap(([title, names]) => [`---${title}---`, ...names]);
  }
  mkdirSync(join(output, tag), { recursive: true });
  writeFileSync(join(output, tag, "meta.json"), JSON.stringify(meta, null, 2));
}

// Every generated page must be reachable from the sidebar.
let listed = 0;
let total = 0;
for (const tag of written) {
  const files = readdirSync(join(output, tag)).filter((f) => f.endsWith(".mdx"));
  total += files.length;
  if (!grouped.includes(tag)) throw new Error(`generate-api: tag folder ${tag} is missing from the sidebar`);
  const meta = JSON.parse(readFileSync(join(output, tag, "meta.json"), "utf8")) as { pages?: string[] };
  const missing = meta.pages ? files.filter((f) => !meta.pages?.includes(f.replace(/\.mdx$/, ""))) : [];
  if (missing.length) throw new Error(`generate-api: ${tag}: ${missing.join(", ")} missing from the sidebar`);
  listed += files.length;
}
console.log(`generate-api: ${listed} of ${total} endpoint pages in the sidebar, ${order.length} tags`);
