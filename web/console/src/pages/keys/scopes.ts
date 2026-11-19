import type { KeyScope } from "../../api/types";

/** Every scope of the schema, in the order the picker lists them. */
export const KEY_SCOPES = [
  "*",
  "tenant:read",
  "users:read",
  "users:write",
  "tokens:write",
  "data:read",
  "data:write",
  "data:delete",
  "connections:read",
  "connections:write",
  "webhooks:read",
  "webhooks:write",
  "exports:read",
  "exports:write",
  "keys:read",
  "keys:write",
  "audit:read",
  "admin:read",
  "admin:write",
] as const satisfies readonly KeyScope[];

// Fails tsc when the schema has a scope this list does not.
type Missing = Exclude<KeyScope, (typeof KEY_SCOPES)[number]>;
export const KEY_SCOPES_COMPLETE: [Missing] extends [never] ? true : never = true;

export interface ScopeGroup {
  resource: string;
  scopes: KeyScope[];
}

/** Scopes grouped by the part before ":" in list order; "*" alone comes first. */
export function groupScopes(scopes: readonly KeyScope[]): ScopeGroup[] {
  const groups = new Map<string, KeyScope[]>();
  for (const scope of scopes) {
    const resource = scope.split(":")[0] ?? scope;
    const list = groups.get(resource) ?? [];
    list.push(scope);
    groups.set(resource, list);
  }
  const all = [...groups].map(([resource, list]) => ({ resource, scopes: list }));
  return [...all.filter((g) => g.resource === "*"), ...all.filter((g) => g.resource !== "*")];
}

/** The scopes a choice grants: a :write brings the :read of its resource, "*" brings all. */
export function impliedScopes(chosen: readonly KeyScope[]): Set<KeyScope> {
  if (chosen.includes("*")) return new Set(KEY_SCOPES);
  const out = new Set<KeyScope>(chosen);
  for (const scope of chosen) {
    if (scope.endsWith(":write")) {
      const read = scope.replace(/:write$/, ":read");
      if ((KEY_SCOPES as readonly string[]).includes(read)) out.add(read as KeyScope);
    }
  }
  return out;
}

/** True when a key with these scopes may do scope. */
export function heldBy(held: readonly KeyScope[], scope: KeyScope): boolean {
  return impliedScopes(held).has(scope);
}

/** The scopes this build knows among names the session reports; others cannot be offered. */
export function knownScopes(names: readonly string[]): KeyScope[] {
  return KEY_SCOPES.filter((s) => names.includes(s));
}

/** True when a session holding sessionScopes may give scope to a new key. admin:* counts only on an instance admin tenant. */
export function grantable(sessionScopes: readonly KeyScope[], isAdmin: boolean, scope: KeyScope): boolean {
  return heldBy(sessionScopes, scope) && (isAdmin || !scope.startsWith("admin:"));
}

/** The read only preset: every :read scope the session may give. Empty when it may give none. */
export function readOnlyScopes(sessionScopes: readonly KeyScope[], isAdmin: boolean): KeyScope[] {
  return KEY_SCOPES.filter((s) => s.endsWith(":read") && grantable(sessionScopes, isAdmin, s));
}

/** The full access preset: "*" when the session holds it, else the session's own scopes, spelled out. */
export function fullAccessScopes(sessionScopes: readonly KeyScope[]): KeyScope[] {
  return sessionScopes.includes("*") ? ["*"] : KEY_SCOPES.filter((s) => sessionScopes.includes(s));
}

/** How the picker names a scope: "*" alone gets words, the others are shown as they are. */
export function scopeLabel(scope: KeyScope): string {
  return scope === "*" ? "All scopes (*)" : scope;
}
