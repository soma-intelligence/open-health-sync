import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createOpenAPI } from "fumadocs-openapi/server";
import { parse } from "yaml";

type Security = Record<string, string[]>[];

const methods = ["get", "put", "post", "delete", "patch", "head", "options"] as const;

const bearer: Record<string, string> = {
  tenantKey: "Bearer ohs_sk_...",
  userToken: "Bearer ohs_ut_...",
};

// The repository's api/openapi.yaml is the source of truth. Two changes for the reference only:
// its server is "/", the instance that serves it, so a local instance is shown instead; and each
// secured operation gets an Authorization header example, so the request samples carry the
// credential the route takes.
function load() {
  const doc = parse(readFileSync(join(process.cwd(), "..", "api", "openapi.yaml"), "utf8"));
  doc.servers = [{ url: "http://localhost:8080", description: "A local instance (quickstart)" }];
  for (const item of Object.values<Record<string, { security?: Security; parameters?: unknown[] }>>(doc.paths ?? {})) {
    for (const method of methods) {
      const op = item[method];
      if (!op) continue;
      const security: Security = op.security ?? doc.security ?? [];
      const scheme = security.flatMap((s) => Object.keys(s)).find((name) => name in bearer);
      if (!scheme) continue;
      op.parameters = [
        ...(op.parameters ?? []),
        {
          name: "Authorization",
          in: "header",
          required: true,
          description: `The ${scheme === "tenantKey" ? "tenant API key" : "user token"}, as a bearer secret.`,
          schema: { type: "string" },
          example: bearer[scheme],
        },
      ];
    }
  }
  return doc;
}

export const openapi = createOpenAPI({
  input: { ohs: load },
});
