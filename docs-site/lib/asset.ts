/** A file under public/, with the base path the site is built for (NEXT_PUBLIC_BASE_PATH). */
export function asset(path: string): string {
  return `${process.env.NEXT_PUBLIC_BASE_PATH ?? ""}${path}`;
}

/** A full address for a path, for files read outside the site, such as llms.txt. */
export function absoluteUrl(path: string): string {
  return `${process.env.NEXT_PUBLIC_SITE_URL?.replace(/\/+$/, "") ?? ""}${asset(path)}`;
}
