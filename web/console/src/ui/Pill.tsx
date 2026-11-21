import type { ReactNode } from "react";

/** A state chip: green ok, orange warning, red blocked, grey idle; info (indigo) labels a kind, not a state. */
export function Pill({ tone, children }: { tone: "ok" | "warn" | "bad" | "off" | "info"; children: ReactNode }) {
  return <span className={`pill ${tone}`}>{children}</span>;
}
