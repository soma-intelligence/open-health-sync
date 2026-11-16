// The only place that touches localStorage: three preferences, nothing else.

export type PrefName = "theme" | "inspector" | "inspector-lang";

const NAMES: readonly PrefName[] = ["theme", "inspector", "inspector-lang"];

const keyOf = (name: PrefName): string => `ohs.console.${name}`;

export function readPref(name: PrefName): string | null {
  if (!NAMES.includes(name)) return null;
  try {
    return localStorage.getItem(keyOf(name));
  } catch {
    return null;
  }
}

export function writePref(name: PrefName, value: string): void {
  if (!NAMES.includes(name)) return;
  try {
    localStorage.setItem(keyOf(name), value);
  } catch {
    // Storage is blocked or full: the preference lasts for this visit only.
  }
}
