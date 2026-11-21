import type { ReactNode } from "react";
import { Link } from "react-router";

export interface TabItem<T extends string> {
  id: T;
  label: string;
  /** Router path of the tab, so the tab lives in the address. */
  to: string;
}

/** Tabs that are links: the address says which is open, and Back works. */
export function Tabs<T extends string>({ tabs, current, label }: { tabs: readonly TabItem<T>[]; current: T; label: string }) {
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {tabs.map((t) => (
        <Link key={t.id} to={t.to} role="tab" id={`tab-${t.id}`} aria-selected={t.id === current} aria-controls="tabpanel" className={t.id === current ? "tab active" : "tab"}>
          {t.label}
        </Link>
      ))}
    </div>
  );
}

export function TabPanel({ current, children }: { current: string; children: ReactNode }) {
  return (
    <div className="tabpanel" role="tabpanel" id="tabpanel" aria-labelledby={`tab-${current}`}>
      {children}
    </div>
  );
}
