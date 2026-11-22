"use client";
import { RootProvider } from "fumadocs-ui/provider/next";
import type { ReactNode } from "react";
import { SearchDialog } from "@/components/layout/search-dialog";

/**
 * The theme follows the system, like openhealthsync.com, with no switch. next-themes writes both
 * the `dark` class the MDX components read and the `data-theme` attribute the tokens read.
 * Search opens with ⌘K or Ctrl K, in this site's own dialog.
 */
export function Provider({ children }: { children: ReactNode }) {
  return (
    <RootProvider
      theme={{ attribute: ["class", "data-theme"], defaultTheme: "system", enableSystem: true, hotKey: false }}
      search={{ SearchDialog }}
    >
      {children}
    </RootProvider>
  );
}
