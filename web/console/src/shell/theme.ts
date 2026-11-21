import { useCallback, useState } from "react";

import { readPref, writePref } from "../lib/prefs";

export type ThemeChoice = "light" | "dark" | "auto";

export const THEME_CHOICES: readonly ThemeChoice[] = ["light", "dark", "auto"];

export function parseTheme(value: string | null): ThemeChoice {
  return value === "light" || value === "dark" ? value : "auto";
}

/** Auto leaves the choice to prefers-color-scheme; light and dark pin it. */
export function applyTheme(choice: ThemeChoice, root: HTMLElement = document.documentElement): void {
  if (choice === "auto") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", choice);
}

export function useTheme(): [ThemeChoice, (choice: ThemeChoice) => void] {
  const [choice, setChoice] = useState<ThemeChoice>(() => parseTheme(readPref("theme")));
  const update = useCallback((next: ThemeChoice) => {
    applyTheme(next);
    writePref("theme", next);
    setChoice(next);
  }, []);
  return [choice, update];
}
