import type { ReactNode } from "react";
import { getNav } from "@/lib/nav";
import { DocsFrame } from "./docs-frame";

/** The header and sidebar around the guides, shared by both routes and the 404 page. */
export function DocsShell({ children }: { children: ReactNode }) {
  return <DocsFrame nav={getNav()}>{children}</DocsFrame>;
}
