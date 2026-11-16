import { ApiError, type ApiResponse } from "../../api/client";
import type { Setting, SettingsPatchResult, SettingsProblem } from "../../api/types";

/** The draft: a value to save, or null to go back to the default. */
export type Pending = Readonly<Record<string, string | null>>;

export interface FieldState {
  editable: boolean;
  locked: "env" | "bootstrap" | null;
  changed: boolean;
  invalidStored: string | null;
  inputError: string | null;
}

export interface DiffRow {
  name: string;
  label: string;
  apply: "live" | "restart";
  before: string;
  after: string;
  secret: boolean;
}

export type ApplyOutcome =
  | { kind: "ok"; revision: number; restart: string[] }
  | { kind: "moved" }
  | { kind: "conflict"; names: string[] }
  | { kind: "invalid"; errors: Record<string, string> };

export const EMPTY_TYPED = "Enter a value, or revert to default.";

// The server refuses every empty value (null reverts to the default), and an
// empty variable reads as unset under the environment rule: "" is never sent.
const RETENTION = new Set(["OHS_RETENTION_DAYS", "OHS_AUDIT_RETENTION_DAYS", "OHS_TRACKING_RETENTION_DAYS"]);

/** The address segment of a settings section: lower case, spaces as hyphens. */
export function sectionSlug(section: string): string {
  return section.toLowerCase().replace(/\s+/g, "-");
}

/** The router path of one setting (no /console prefix). */
export function settingPath(s: Setting): string {
  return `/settings/${sectionSlug(s.section)}/${s.name}`;
}

function lockOf(s: Setting): FieldState["locked"] {
  if (s.apply === "bootstrap") return "bootstrap";
  if (s.source === "env") return "env";
  return null;
}

function boundsText(s: Setting): string {
  if (s.min !== null && s.max !== null) return `from ${String(s.min)} to ${String(s.max)}`;
  if (s.min !== null) return `of at least ${String(s.min)}`;
  if (s.max !== null) return `of at most ${String(s.max)}`;
  return "";
}

function inputErrorOf(s: Setting, v: string | null): string | null {
  if (v === null) return null;
  if (v === "") return EMPTY_TYPED;
  if (s.type === "int") {
    const n = /^-?\d+$/.test(v.trim()) ? Number(v) : NaN;
    if (!Number.isSafeInteger(n) || (s.min !== null && n < s.min) || (s.max !== null && n > s.max)) {
      return `Enter a whole number${boundsText(s) ? " " + boundsText(s) : ""}.`;
    }
  }
  return null;
}

export function fieldState(s: Setting, pending: Pending): FieldState {
  const locked = lockOf(s);
  const changed = Object.hasOwn(pending, s.name);
  return {
    editable: locked === null,
    locked,
    changed,
    invalidStored: s.invalid,
    inputError: changed ? inputErrorOf(s, pending[s.name] ?? null) : null,
  };
}

function defaultText(s: Setting): string {
  const shown = s.default !== "" ? s.default : (s.default_note ?? "");
  return shown === "" ? "default" : `default (${shown})`;
}

function beforeText(s: Setting): string {
  if (s.secret) return s.value === null ? "not set" : "set";
  return s.value ?? "not set";
}

/** The review rows: what each pending name holds now and what it will hold, by section then name. */
export function diffRows(settings: readonly Setting[], pending: Pending): DiffRow[] {
  const sectionAt = new Map<string, number>();
  for (const s of settings) if (!sectionAt.has(s.section)) sectionAt.set(s.section, sectionAt.size);
  const byName = new Map(settings.map((s) => [s.name, s]));
  const rows: { row: DiffRow; section: number }[] = [];
  for (const [name, v] of Object.entries(pending)) {
    const s = byName.get(name);
    if (!s) continue;
    const after = v === null ? defaultText(s) : s.secret ? "new value" : v;
    rows.push({
      section: sectionAt.get(s.section) ?? 0,
      row: { name, label: s.label, apply: s.apply === "live" ? "live" : "restart", before: beforeText(s), after, secret: s.secret },
    });
  }
  rows.sort((a, b) => a.section - b.section || (a.row.name < b.row.name ? -1 : a.row.name > b.row.name ? 1 : 0));
  return rows.map((r) => r.row);
}

export function restartCount(rows: readonly DiffRow[]): number {
  return rows.filter((r) => r.apply === "restart").length;
}

/** The PATCH body. Throws on an empty value: it is never sent. */
export function toPatch(pending: Pending): Record<string, string | null> {
  const out: Record<string, string | null> = {};
  for (const [name, v] of Object.entries(pending)) {
    if (v === "") throw new Error(`${name}: an empty value is never sent`);
    out[name] = v;
  }
  return out;
}

/**
 * How the server reads OHS_TRACKING_RETENTION_DAYS, kept in step with
 * internal/etl/automation/automation.go and tracking.go: 0 means the default
 * (90 days), 1 to 13 count as 14, a negative value keeps everything.
 */
export const TRACKING_RETENTION = { defaultDays: 90, minDays: 14 } as const;

/** The days a retention keeps, or null when it keeps everything (server meaning). */
function retentionDays(name: string, raw: string): number | null {
  const t = raw.trim();
  if (!/^-?\d+$/.test(t)) return null;
  const n = Number(t);
  if (name === "OHS_TRACKING_RETENTION_DAYS") {
    if (n < 0) return null;
    return n === 0 ? TRACKING_RETENTION.defaultDays : Math.max(n, TRACKING_RETENTION.minDays);
  }
  return n <= 0 ? null : n;
}

/**
 * A retention that keeps everything now and would delete after N days once the
 * change is applied: the next daily cleanup deletes for good. A retention that
 * deletes already never warns.
 */
export function retentionWarnings(settings: readonly Setting[], pending: Pending): string[] {
  const out: string[] = [];
  for (const s of settings) {
    if (!RETENTION.has(s.name) || !Object.hasOwn(pending, s.name) || s.value === null) continue;
    if (retentionDays(s.name, s.value) !== null) continue;
    const after = retentionDays(s.name, pending[s.name] ?? s.default);
    if (after === null) continue;
    out.push(`${s.name} will delete data older than ${String(after)} days at the next daily cleanup. This cannot be undone.`);
  }
  return out;
}

/** After a reload: drops what is no longer the console's to change, or what is already saved. */
export function prunePending(settings: readonly Setting[], pending: Pending): { pending: Pending; dropped: string[] } {
  const byName = new Map(settings.map((s) => [s.name, s]));
  const kept: Record<string, string | null> = {};
  const dropped: string[] = [];
  for (const [name, v] of Object.entries(pending)) {
    const s = byName.get(name);
    const gone =
      !s ||
      lockOf(s) !== null ||
      (v === null && s.source === "default") ||
      (v !== null && !s.secret && s.source === "console" && s.value === v);
    if (gone) dropped.push(name);
    else kept[name] = v;
  }
  return { pending: kept, dropped };
}

function problemSettings(body: unknown): SettingsProblem["settings"] {
  if (body === null || typeof body !== "object") return [];
  const list = (body as { settings?: unknown }).settings;
  if (!Array.isArray(list)) return [];
  return list.filter(
    (x): x is SettingsProblem["settings"][number] =>
      x !== null && typeof x === "object" && typeof (x as { name?: unknown }).name === "string",
  );
}

/** Reads the answer of PATCH /v1/admin/settings. An error that is none of 412, 409 and 422 is thrown. */
export function applyOutcome(result: ApiResponse<unknown> | ApiError): ApplyOutcome {
  if (result instanceof ApiError) {
    if (result.status === 412) return { kind: "moved" };
    if (result.status === 409) return { kind: "conflict", names: problemSettings(result.body).map((x) => x.name) };
    if (result.status === 422) {
      const errors: Record<string, string> = {};
      for (const x of problemSettings(result.body)) errors[x.name] = typeof x.message === "string" ? x.message : "";
      return { kind: "invalid", errors };
    }
    throw result;
  }
  const data = result.data as Partial<SettingsPatchResult> | null;
  return { kind: "ok", revision: data?.revision ?? 0, restart: data?.restart_required ?? [] };
}
