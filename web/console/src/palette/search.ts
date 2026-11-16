import type { Setting } from "../api/types";
import { sectionSlug } from "../pages/settings/model";

export interface PaletteItem {
  group: "Go to" | "Actions" | "Settings";
  title: string;
  hint?: string;
  /** The address, basename included. */
  to?: string;
  /** Open the create dialog of the page at `to`. */
  open?: "create";
  run?: "theme" | "sign-out";
}

export interface PaletteContext {
  admin: boolean;
  canWrite: boolean;
  settings: readonly Setting[];
}

const DEFAULT_LIMIT = 14;
const BASE = "/console";

const PAGES: readonly PaletteItem[] = [
  { group: "Go to", title: "Overview", to: `${BASE}/overview` },
  { group: "Go to", title: "Users", to: `${BASE}/users` },
  { group: "Go to", title: "Providers", to: `${BASE}/providers` },
  { group: "Go to", title: "Jobs", to: `${BASE}/jobs` },
  { group: "Go to", title: "Webhooks", to: `${BASE}/webhooks` },
  { group: "Go to", title: "API keys", to: `${BASE}/keys` },
  { group: "Go to", title: "Audit log", to: `${BASE}/audit` },
];

const WRITE_ACTIONS: readonly PaletteItem[] = [
  { group: "Actions", title: "Create user", to: `${BASE}/users`, open: "create" },
  { group: "Actions", title: "Create API key", to: `${BASE}/keys`, open: "create" },
  { group: "Actions", title: "Add webhook endpoint", to: `${BASE}/webhooks`, open: "create" },
];

/** Every item the palette can offer, in the order of an empty query: pages, actions, settings. */
export function paletteItems(ctx: PaletteContext): PaletteItem[] {
  const out: PaletteItem[] = [...PAGES];
  if (ctx.admin) out.push({ group: "Go to", title: "Settings", to: `${BASE}/settings` });
  if (ctx.canWrite) out.push(...WRITE_ACTIONS);
  out.push({ group: "Actions", title: "Switch theme", run: "theme" }, { group: "Actions", title: "Sign out", run: "sign-out" });
  if (ctx.admin) {
    for (const s of ctx.settings) {
      out.push({ group: "Settings", title: s.name, hint: s.label, to: `${BASE}/settings/${sectionSlug(s.section)}/${s.name}` });
    }
  }
  return out;
}

/** Lower case, every run of other characters as one space: OHS_LOG_LEVEL reads "ohs log level". */
function norm(text: string): string {
  return text.toLowerCase().replace(/[^a-z0-9]+/g, " ").trim();
}

/** 0 prefix, 1 word prefix, 2 substring, 3 no match. */
function rank(text: string, q: string): number {
  const t = norm(text);
  if (t.startsWith(q)) return 0;
  if (t.includes(` ${q}`)) return 1;
  if (t.includes(q)) return 2;
  return 3;
}

/** Items matching the query by title or hint, best match first; ties keep their order. */
export function searchPalette(items: readonly PaletteItem[], query: string, limit = DEFAULT_LIMIT): PaletteItem[] {
  const q = norm(query);
  if (q === "") return items.filter((i) => i.group !== "Settings").slice(0, limit);
  const scored: { item: PaletteItem; score: number; at: number }[] = [];
  items.forEach((item, at) => {
    const score = Math.min(rank(item.title, q), item.hint === undefined ? 3 : rank(item.hint, q));
    if (score < 3) scored.push({ item, score, at });
  });
  scored.sort((a, b) => a.score - b.score || a.at - b.at);
  return scored.slice(0, limit).map((x) => x.item);
}
