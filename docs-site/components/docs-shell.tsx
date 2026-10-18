import { DocsLayout } from "fumadocs-ui/layouts/notebook";
import type { ReactNode } from "react";
import { baseOptions } from "@/lib/layout.shared";
import { source } from "@/lib/source";

/** The sidebar and top bar around the guides, shared by both routes. */
export function DocsShell({ children }: { children: ReactNode }) {
  const base = baseOptions();
  return (
    <DocsLayout tree={source.getPageTree()} {...base} tabMode="navbar" nav={{ ...base.nav, mode: "top" }}>
      {children}
    </DocsLayout>
  );
}
