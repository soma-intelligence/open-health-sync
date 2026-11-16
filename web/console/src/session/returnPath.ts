const OVERVIEW = "/console/overview";

/** Where to go after sign in: a console page other than sign in, else the overview. */
export function safeReturnPath(from: unknown): string {
  if (typeof from !== "string" || !from.startsWith("/") || from.startsWith("//") || from.includes("\\")) return OVERVIEW;
  let url: URL;
  try {
    url = new URL(from, "http://console.invalid");
  } catch {
    return OVERVIEW;
  }
  if (url.origin !== "http://console.invalid") return OVERVIEW;
  if (!url.pathname.startsWith("/console/") || /^\/console\/sign-in\/?$/.test(url.pathname)) return OVERVIEW;
  return url.pathname + url.search;
}
