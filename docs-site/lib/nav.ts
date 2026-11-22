import type { Folder, Item, Node, Root } from "fumadocs-core/page-tree";
import { firstPage, type NavEntry, type NavGroup, type NavPage, type NavRoot, type NavSection } from "./nav-model";
import { source } from "./source";

const text = (name: unknown) => (typeof name === "string" ? name : "");

function toPage(item: Item): NavPage {
  const page = item.external ? undefined : source.getNodePage(item);
  const data = page?.data as { status?: string; _openapi?: { method?: string } } | undefined;
  const external = item.external ?? /^https?:/.test(item.url);
  return {
    type: "page",
    name: text(item.name),
    url: item.url,
    ...(data?._openapi?.method ? { method: data._openapi.method.toUpperCase() } : {}),
    ...(data?.status ? { status: data.status } : {}),
    ...(external ? { external } : {}),
  };
}

function countPages(entries: NavEntry[]): number {
  return entries.reduce(
    (n, e) => n + (e.type === "page" ? (e.method ? 1 : 0) : e.type === "group" ? countPages(e.entries) : 0),
    0,
  );
}

/** A folder's children; its separators become labels inside the group. */
function toEntries(nodes: Node[]): NavEntry[] {
  const out: NavEntry[] = [];
  for (const node of nodes) {
    if (node.type === "separator") {
      if (text(node.name)) out.push({ type: "label", name: text(node.name) });
    } else if (node.type === "folder") out.push(toGroup(node));
    else out.push(toPage(node));
  }
  return out;
}

function toGroup(folder: Folder): NavGroup {
  const entries = toEntries(folder.children);
  if (folder.index) entries.unshift({ ...toPage(folder.index), name: "Overview" });
  const count = countPages(entries);
  return {
    type: "group",
    name: text(folder.name),
    ...(count ? { count } : {}),
    ...(folder.defaultOpen ? { defaultOpen: true } : {}),
    entries,
  };
}

/** A root folder's children, cut into sections at its separators. */
function toSections(nodes: Node[]): NavSection[] {
  const sections: NavSection[] = [{ entries: [] }];
  for (const node of nodes) {
    if (node.type === "separator") sections.push({ name: text(node.name) || undefined, entries: [] });
    else if (node.type === "folder") sections[sections.length - 1].entries.push(toGroup(node));
    else sections[sections.length - 1].entries.push(toPage(node));
  }
  return sections.filter((s) => s.entries.length > 0);
}

let cached: NavRoot[] | undefined;

/** The three parts of the site (guides, API reference, hosted platform), each with its own sidebar. */
export function getNav(): NavRoot[] {
  if (cached) return cached;
  const tree: Root = source.getPageTree();
  cached = tree.children
    .filter((n): n is Folder => n.type === "folder" && Boolean(n.root))
    .map((folder) => {
      const sections = toSections(folder.children);
      if (folder.index) sections.unshift({ entries: [toPage(folder.index)] });
      const first = firstPage(sections);
      return {
        id: folder.$id ?? text(folder.name),
        name: text(folder.name),
        description: text(folder.description) || undefined,
        url: first?.url ?? "/",
        sections,
      };
    });
  return cached;
}
