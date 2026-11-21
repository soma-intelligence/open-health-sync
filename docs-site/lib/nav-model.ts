// The sidebar, the breadcrumb and the previous and next links all read this model, built once from
// the fumadocs page tree by lib/nav.ts. It holds plain strings only, so it crosses to the client
// as is, and the helpers below run on both sides.

export type NavPage = {
  type: "page";
  name: string;
  url: string;
  /** HTTP method of an endpoint page, from its generated frontmatter */
  method?: string;
  /** a status badge, such as "beta" or "planned" */
  status?: string;
  external?: boolean;
};
export type NavLabel = { type: "label"; name: string };
export type NavGroup = {
  type: "group";
  name: string;
  /** endpoint pages under it, shown beside the name of an API group */
  count?: number;
  defaultOpen?: boolean;
  entries: NavEntry[];
};
export type NavEntry = NavPage | NavLabel | NavGroup;
export type NavSection = { name?: string; entries: NavEntry[] };
export type NavRoot = { id: string; name: string; description?: string; url: string; sections: NavSection[] };

export function pagesOf(entries: NavEntry[]): NavPage[] {
  return entries.flatMap((e) => (e.type === "page" ? [e] : e.type === "group" ? pagesOf(e.entries) : []));
}

export function firstPage(sections: NavSection[]): NavPage | undefined {
  return sections.flatMap((s) => pagesOf(s.entries)).find((p) => !p.external);
}

/** The root a page belongs to: the one that lists it. */
export function rootOf(nav: NavRoot[], url: string): NavRoot | undefined {
  return nav.find((root) => root.sections.some((s) => pagesOf(s.entries).some((p) => p.url === url)));
}

/** The root, section and groups above a page, for its breadcrumb. */
export function trailOf(nav: NavRoot[], url: string): string[] {
  const root = rootOf(nav, url);
  if (!root) return [];
  for (const section of root.sections) {
    const path = findPath(section.entries, url);
    if (path) return [root.name, ...(section.name ? [section.name] : []), ...path];
  }
  return [root.name];
}

function findPath(entries: NavEntry[], url: string): string[] | undefined {
  for (const entry of entries) {
    if (entry.type === "page" && entry.url === url) return [];
    if (entry.type === "group") {
      const path = findPath(entry.entries, url);
      if (path) return [entry.name, ...path];
    }
  }
  return undefined;
}

/** The pages before and after one, in sidebar order, inside its root. */
export function neighboursOf(nav: NavRoot[], url: string): { previous?: NavPage; next?: NavPage } {
  const root = rootOf(nav, url);
  if (!root) return {};
  const pages = root.sections.flatMap((s) => pagesOf(s.entries)).filter((p) => !p.external);
  const i = pages.findIndex((p) => p.url === url);
  return { previous: pages[i - 1], next: i >= 0 ? pages[i + 1] : undefined };
}
