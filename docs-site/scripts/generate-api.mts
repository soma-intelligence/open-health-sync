// Generates one page per operation of ../api/openapi.yaml, grouped by tag. The output is
// git-ignored and rebuilt by `npm run dev` and `npm run build`.

import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { generateFiles } from "fumadocs-openapi";
import { openapi } from "../lib/openapi.ts";

const output = "./content/docs/api-reference/endpoints";

// Sidebar titles and order of the OpenAPI tags.
const tags: [string, string][] = [
  ["users", "Users"],
  ["tokens", "Tokens"],
  ["providers", "Providers"],
  ["connections", "Connections"],
  ["ingest", "Ingest"],
  ["read", "Read data"],
  ["mcp", "MCP"],
  ["provider-facing", "Provider-facing"],
  ["ops", "Operations"],
];

const kebab = (s: string) => s.replace(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase();

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
    }
  },
});

writeFileSync(join(output, "meta.json"), JSON.stringify({ pages: tags.map(([tag]) => tag) }, null, 2));
for (const [tag, title] of tags) {
  mkdirSync(join(output, tag), { recursive: true });
  writeFileSync(join(output, tag, "meta.json"), JSON.stringify({ title, defaultOpen: tag === "users" }, null, 2));
}
