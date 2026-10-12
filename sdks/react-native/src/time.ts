// SPDX-License-Identifier: MIT

const RFC3339 =
  /^(\d{4}-\d{2}-\d{2})[Tt ](\d{2}:\d{2}:\d{2})(?:\.(\d+))?([Zz]|[+-]\d{2}:\d{2})$/;
const DAY = /^\d{4}-\d{2}-\d{2}$/;

/**
 * Parses an RFC 3339 timestamp as the server sends it (with or without fractional seconds,
 * up to nanoseconds) into a `Date`. Fractional digits beyond milliseconds are dropped first,
 * since `Date` holds milliseconds and some engines (Hermes among them) do not parse more
 * than three digits reliably. Throws a `RangeError` on anything else.
 */
export function parseTimestamp(s: string): Date {
  const m = RFC3339.exec(s);
  if (!m) throw new RangeError("Open Health Sync: not an RFC 3339 timestamp");
  const frac = m[3] ? "." + (m[3] + "00").slice(0, 3) : "";
  const zone = m[4] === "z" ? "Z" : (m[4] as string);
  const d = new Date(`${m[1]}T${m[2]}${frac}${zone}`);
  if (Number.isNaN(d.getTime())) throw new RangeError("Open Health Sync: not an RFC 3339 timestamp");
  return d;
}

/** Formats a `Date` as RFC 3339 in UTC with milliseconds, for query parameters. */
export function formatTimestamp(d: Date): string {
  return d.toISOString();
}

/** True for a `YYYY-MM-DD` string that names a real calendar day. */
export function isDay(s: string): boolean {
  if (!DAY.test(s)) return false;
  const d = new Date(`${s}T00:00:00Z`);
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s;
}

/**
 * The calendar day of `instant` in the IANA `timeZone` (for example "Europe/Paris"), as
 * `YYYY-MM-DD`. Use the zone the sample was recorded in (its `HKTimeZone`) so the part's
 * `X-OHS-Day` matches the day the server files the sample under. Needs `Intl` with time zone
 * data, which Hermes, JavaScriptCore, Node and browsers have.
 */
export function localDay(instant: Date, timeZone: string): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(instant);
  const get = (t: string): string => parts.find((p) => p.type === t)?.value ?? "";
  const year = get("year").padStart(4, "0");
  return `${year}-${get("month")}-${get("day")}`;
}
