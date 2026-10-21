// SPDX-License-Identifier: AGPL-3.0-only
//
// Small building blocks shared by the pages. Server components: no state, no browser code.

import type { ReactNode } from "react";
import type { ShownError } from "../lib/ohs.ts";

const ICONS = {
  pulse: "M1.5 8.5h3l1.8-4.5 3.4 9 1.8-4.5h3",
  arrow: "M2.75 8h10.5M9.25 4l4 4-4 4",
  check: "m3.25 8.25 3 3 6.5-6.75",
  alert: "M7.13 2.25a1 1 0 0 1 1.74 0l5.6 9.75a1 1 0 0 1-.87 1.5H2.4a1 1 0 0 1-.87-1.5zM8 6.25v3M8 11.4v.1",
  info: "M8 1.75a6.25 6.25 0 1 0 0 12.5 6.25 6.25 0 0 0 0-12.5zM8 7.25v4M8 4.9v.1",
  external: "M9.5 2.25h4.25V6.5M13.5 2.5 7.75 8.25M11.75 9.5v3.25a1 1 0 0 1-1 1h-7.5a1 1 0 0 1-1-1v-7.5a1 1 0 0 1 1-1H6.5",
} as const;

export function Icon({ name }: { name: keyof typeof ICONS }) {
  return (
    <svg className="icon" viewBox="0 0 16 16" aria-hidden="true">
      <path d={ICONS[name]} />
    </svg>
  );
}

/** An API route the page calls, as "METHOD /path". */
export function Route({ method, path }: { method: "GET" | "POST"; path: string }) {
  return (
    <code className="route" title={`${method} ${path}`}>
      <span className={`method ${method.toLowerCase()}`}>{method}</span>
      <span className="path">{path}</span>
    </code>
  );
}

export function Routes({ children }: { children: ReactNode }) {
  return <div className="routes">{children}</div>;
}

export function PageHead(props: { step: string; title: string; sub: ReactNode; routes?: ReactNode }) {
  return (
    <header className="page-head">
      <div className="head-main">
        <p className="label">{props.step}</p>
        <h1>{props.title}</h1>
        <p className="sub">{props.sub}</p>
      </div>
      {props.routes}
    </header>
  );
}

export function Notice(props: { kind?: "ok" | "warn" | "bad"; children: ReactNode }) {
  const icon = props.kind === "ok" ? "check" : props.kind ? "alert" : "info";
  return (
    <div className={`notice ${props.kind ?? ""}`} role={props.kind === "bad" ? "alert" : undefined}>
      <Icon name={icon} />
      <div>{props.children}</div>
    </div>
  );
}

/** An API error: the problem's title and detail, the status, and the request id to quote. */
export function ErrorNotice({ error, what }: { error: ShownError; what: string }) {
  return (
    <Notice kind="bad">
      <strong>{what} failed.</strong> {error.message}
      {(error.status || error.requestId) && (
        <span className="small">
          {" "}
          ({[error.status && `HTTP ${error.status}`, error.requestId && `request ${error.requestId}`].filter(Boolean).join(", ")})
        </span>
      )}
    </Notice>
  );
}

/** A response body, pretty printed, folded until opened. */
export function Json({ value, label, open = false }: { value: unknown; label: string; open?: boolean }) {
  const text = JSON.stringify(value, null, 2);
  return (
    <details className="json" open={open}>
      <summary>
        {label}
        <span className="meta">{text.split("\n").length} lines</span>
      </summary>
      <pre>{text}</pre>
    </details>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <div className="empty-title">{title}</div>
      {children && <p>{children}</p>}
    </div>
  );
}

const dateTime = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  timeZone: "UTC",
  timeZoneName: "short",
});
const dateOnly = new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short", timeZone: "UTC" });
const timeOnly = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", timeZone: "UTC" });

/** Times are shown in UTC, the zone of the synthetic data, so a page reads the same everywhere. */
export function formatDateTime(iso: string): string {
  return dateTime.format(new Date(iso));
}

export function formatDay(day: string): string {
  return dateOnly.format(new Date(`${day}T00:00:00Z`));
}

export function formatTime(iso: string): string {
  return timeOnly.format(new Date(iso));
}

export function formatDuration(seconds: number): string {
  const minutes = Math.round(seconds / 60);
  return `${Math.floor(minutes / 60)} h ${String(minutes % 60).padStart(2, "0")} min`;
}

export const formatNumber = (n: number) => new Intl.NumberFormat("en-US").format(n);
