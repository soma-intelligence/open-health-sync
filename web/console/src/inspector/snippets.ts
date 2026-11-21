import type { Method } from "../api/client";
import { withQuery } from "../api/paths";
import related from "./related-settings.json";

export interface InspectorRequest {
  method: Method;
  path: string;
  query?: Record<string, string>;
  body?: unknown;
}

export type InspectorKind =
  | "pipeline"
  | "users"
  | "user"
  | "providers"
  | "provider"
  | "jobs"
  | "webhooks"
  | "webhook"
  | "keys"
  | "audit"
  | "audit-event"
  | "settings"
  | "setting";

export type SnippetLang = "curl" | "ts" | "python";

const RELATED: Partial<Record<string, readonly string[]>> = related;

/** The settings that shape what a page of this kind shows. */
export function relatedSettings(kind: InspectorKind): readonly string[] {
  return RELATED[kind] ?? [];
}

const hasBody = (req: InspectorRequest): boolean =>
  req.method !== "GET" && req.method !== "HEAD" && req.body !== undefined && req.body !== null;

const pretty = (value: unknown): string => JSON.stringify(value, null, 2);

// Every line after the first moves right by pad.
const indent = (text: string, pad: string): string => text.split("\n").join("\n" + pad);

function curl(req: InspectorRequest, url: string): string {
  const head = req.method === "GET" ? `curl "${url}"` : `curl -X ${req.method} "${url}"`;
  const lines = [head, '  -H "Authorization: Bearer $OHS_API_KEY"'];
  if (hasBody(req)) {
    lines.push('  -H "Content-Type: application/json"');
    lines.push(`  -d '${pretty(req.body).replace(/'/g, "'\\''")}'`);
  }
  return lines.join(" \\\n");
}

function typescript(req: InspectorRequest, url: string): string {
  const lines = [
    `const res = await fetch(${JSON.stringify(url)}, {`,
    `  method: ${JSON.stringify(req.method)},`,
    "  headers: {",
    "    Authorization: `Bearer ${process.env.OHS_API_KEY}`,",
  ];
  if (hasBody(req)) {
    lines.push('    "Content-Type": "application/json",');
    lines.push("  },");
    lines.push(`  body: JSON.stringify(${indent(pretty(req.body), "  ")}),`);
  } else {
    lines.push("  },");
  }
  lines.push("});", "const data = await res.json();");
  return lines.join("\n");
}

// A JSON value as a Python literal.
function py(value: unknown, depth: number): string {
  if (value === null || value === undefined) return "None";
  if (value === true) return "True";
  if (value === false) return "False";
  if (typeof value === "number") return String(value);
  if (typeof value === "string") return JSON.stringify(value);
  const pad = "    ".repeat(depth + 1);
  const end = "    ".repeat(depth);
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]";
    return `[\n${value.map((v) => pad + py(v, depth + 1)).join(",\n")},\n${end}]`;
  }
  const entries = Object.entries(value as Record<string, unknown>);
  if (entries.length === 0) return "{}";
  return `{\n${entries.map(([k, v]) => `${pad}${JSON.stringify(k)}: ${py(v, depth + 1)}`).join(",\n")},\n${end}}`;
}

function python(req: InspectorRequest, origin: string): string {
  const lines = ["import os, requests", "", `res = requests.${req.method.toLowerCase()}(`, `    ${JSON.stringify(origin + req.path)},`];
  const query = Object.keys(req.query ?? {}).length > 0 ? req.query : undefined;
  if (query) {
    const pairs = Object.entries(query).map(([k, v]) => `${JSON.stringify(k)}: ${JSON.stringify(v)}`);
    lines.push(`    params={${pairs.join(", ")}},`);
  }
  lines.push("    headers={\"Authorization\": f\"Bearer {os.environ['OHS_API_KEY']}\"},");
  if (hasBody(req)) lines.push(`    json=${py(req.body, 1)},`);
  lines.push(")", "data = res.json()");
  return lines.join("\n");
}

/** A runnable call for the request. It names the key by environment variable and never holds one. */
export function snippet(req: InspectorRequest, lang: SnippetLang, origin: string): string {
  const url = origin + withQuery(req.path, req.query);
  if (lang === "curl") return curl(req, url);
  if (lang === "ts") return typescript(req, url);
  return python(req, origin);
}
