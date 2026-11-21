import type { ReactNode } from "react";

/** Ids, keys, setting names and code: JetBrains Mono. */
export function Mono({ children }: { children: ReactNode }) {
  return <span className="mono">{children}</span>;
}
