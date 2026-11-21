export type Query = Record<string, string | number | boolean | null | undefined>;

/** Appends a query; null, undefined and "" values are left out. */
export function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v !== null && v !== undefined && v !== "") qs.set(k, String(v));
  }
  const s = qs.toString();
  return s ? `${path}?${s}` : path;
}

/** Joins segments, each escaped, into a path, with an optional query. */
export function apiPath(segments: readonly string[], query?: Query): string {
  return withQuery("/" + segments.map((s) => encodeURIComponent(s)).join("/"), query);
}

/** The file name of a Content-Disposition header, keeping only characters safe in a file name. */
export function filenameFrom(header: string | null): string {
  const m = /filename="?([^";]+)"?/i.exec(header ?? "");
  const name = m?.[1] ? m[1].replace(/[^A-Za-z0-9._-]/g, "_") : "";
  return name && !/^\.+$/.test(name) ? name : "download";
}
