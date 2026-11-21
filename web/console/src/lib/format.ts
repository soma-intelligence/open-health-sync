// Pure display helpers, ports of the old console's lib.mjs: no DOM, no state.

type Maybe<T> = T | null | undefined;


/** "45 s", "12 min", "3 h 5 min", "2 d 4 h"; a negative duration (a time ahead) reads "in ...". */
export function formatAgo(seconds: Maybe<number>): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) return "-";
  const future = seconds < 0;
  const s = Math.round(Math.abs(seconds));
  let out: string;
  if (s < 60) out = `${String(s)} s`;
  else if (s < 3600) out = `${String(Math.floor(s / 60))} min`;
  else if (s < 86400) {
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    out = m ? `${String(h)} h ${String(m)} min` : `${String(h)} h`;
  } else {
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    out = h ? `${String(d)} d ${String(h)} h` : `${String(d)} d`;
  }
  return future ? `in ${out}` : out;
}

/** "2026-10-13 08:30 UTC (3 h ago)". null reads "-", an unparsable value reads as it is. */
export function formatTime(iso: Maybe<string>, now: number = Date.now()): string {
  if (iso === null || iso === undefined || iso === "") return "-";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const stamp = new Date(t).toISOString().slice(0, 16).replace("T", " ") + " UTC";
  const ago = (now - t) / 1000;
  const rel = ago >= 0 ? `${formatAgo(ago)} ago` : formatAgo(ago);
  return `${stamp} (${rel})`;
}

/** "2026-10-13 08:30 UTC". */
export function absTime(iso: Maybe<string>): string {
  if (iso === null || iso === undefined || iso === "") return "-";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return new Date(t).toISOString().slice(0, 16).replace("T", " ") + " UTC";
}

/** How long ago, in one unit: "just now", "12 min ago", "3 h ago", "4 d ago", "in 5 min". Past 30 days it is the date. */
export function relTime(iso: Maybe<string>, now: number = Date.now()): string {
  if (iso === null || iso === undefined || iso === "") return "-";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const s = Math.round((now - t) / 1000);
  const a = Math.abs(s);
  if (a < 45) return "just now";
  let out: string;
  if (a < 3600) out = `${String(Math.max(1, Math.round(a / 60)))} min`;
  else if (a < 86400) out = `${String(Math.round(a / 3600))} h`;
  else if (a < 30 * 86400) out = `${String(Math.round(a / 86400))} d`;
  else return new Date(t).toISOString().slice(0, 10);
  return s >= 0 ? `${out} ago` : `in ${out}`;
}

/** A sync window: "2026-10-16 03:33 to 04:33" within a day, both stamps in full otherwise. */
export function formatWindow(start: Maybe<string>, end: Maybe<string>): string {
  const a = absTime(start).replace(" UTC", "");
  const b = absTime(end).replace(" UTC", "");
  if (a === "-" || b === "-") return `${a} to ${b}`;
  return a.slice(0, 10) === b.slice(0, 10) ? `${a} to ${b.slice(11)}` : `${a} to ${b}`;
}

export interface Figure {
  figure: string;
  unit: string;
  word: boolean;
}

/** Parts a value for a display numeral: the figure drawn large, what follows drawn small. A value with no figure ("Never") is a word. */
export function splitFigure(value: unknown): Figure {
  const s = (typeof value === "string" || typeof value === "number" ? String(value) : "").trim() || "-";
  const m = /^([-+]?\d[\d,.]*)(?:\s+(.+))?$/.exec(s);
  if (m) return { figure: m[1] ?? "", unit: m[2] ?? "", word: false };
  return { figure: s, unit: "", word: s !== "-" };
}

/** Groups thousands: 1234567 reads "1,234,567". */
export function formatCount(n: Maybe<number>): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "-";
  return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

const KNOWN_STATUS: Record<string, string> = { needs_reauth: "Needs reconnecting", done: "Delivered", api_key: "API key" };

/** An API status as a label: "needs_reauth" reads "Needs reconnecting", "rate_limited" reads "Rate limited". */
export function humanize(status: Maybe<string>): string {
  if (status === null || status === undefined || status === "") return "-";
  if (Object.hasOwn(KNOWN_STATUS, status)) return KNOWN_STATUS[status] ?? status;
  const s = status.replace(/[_.]/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The one or two letters of a provider or tenant mark: "Apple Health" reads "AH", "intervals.icu" reads "I". */
export function initials(name: Maybe<string>): string {
  const words = (name ?? "?")
    .split(/[\s_]+/)
    .filter((w) => /^[A-Za-z0-9]/.test(w));
  const first = words[0];
  if (first === undefined) return "?";
  const second = words[1];
  return (second === undefined ? first.charAt(0) : first.charAt(0) + second.charAt(0)).toUpperCase();
}

/** A provider named from the names the server gave, else from its slug: "apple_health" reads "Apple health". */
export function providerLabel(names: Maybe<Record<string, string>>, slug: string): string {
  if (names && Object.hasOwn(names, slug)) return names[slug] ?? humanize(slug);
  return humanize(slug);
}

/** 0 B, 950 B, 1.5 kB, 12 MB, 3.2 GB (powers of 1000). */
export function formatBytes(n: Maybe<number>): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "-";
  let v = n;
  const units = ["B", "kB", "MB", "GB", "TB"];
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return i === 0 ? `${String(v)} B` : `${v < 10 ? v.toFixed(1) : String(Math.round(v))} ${units[i] ?? ""}`;
}

