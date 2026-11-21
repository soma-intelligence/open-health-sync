import type { ReactNode } from "react";

export function Pill({ tone, children }: { tone: "ok" | "warn" | "bad" | "off"; children: ReactNode }) {
  return <span className={`pill ${tone}`}>{children}</span>;
}
