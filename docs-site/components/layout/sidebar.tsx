"use client";
import { BookOpen, Braces, ChevronRight, Cloud, ExternalLink } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import { MethodBadge } from "@/components/method-badge";
import { type NavEntry, type NavGroup, type NavPage, type NavRoot, pagesOf, rootOf } from "@/lib/nav-model";

const rootIcons = [BookOpen, Braces, Cloud];

function contains(entries: NavEntry[], url: string) {
  return pagesOf(entries).some((p) => p.url === url);
}

function PageLink({ page, pathname, onNavigate }: { page: NavPage; pathname: string; onNavigate?: () => void }) {
  const current = page.url === pathname;
  const ref = useRef<HTMLAnchorElement>(null);
  useEffect(() => {
    // Scroll the sidebar (or the drawer) only, never the page.
    const link = ref.current;
    const box = link?.closest<HTMLElement>(".ohs-sidebar, .ohs-drawer");
    if (!current || !link || !box) return;
    const top = link.getBoundingClientRect().top - box.getBoundingClientRect().top;
    if (top < 0 || top > box.clientHeight - link.offsetHeight) box.scrollTop += top - box.clientHeight / 3;
  }, [current]);
  const body = (
    <>
      {page.method ? <MethodBadge method={page.method} /> : null}
      <span className="ohs-nav-name">{page.name}</span>
      {page.status ? <span className="ohs-nav-status">{page.status}</span> : null}
      {page.external ? <ExternalLink aria-label="(opens GitHub)" className="ohs-nav-ext" /> : null}
    </>
  );
  return page.external ? (
    <a href={page.url} className="ohs-nav-item">
      {body}
    </a>
  ) : (
    <Link
      ref={ref}
      href={page.url}
      className="ohs-nav-item"
      aria-current={current ? "page" : undefined}
      onClick={onNavigate}
    >
      {body}
    </Link>
  );
}

function Group({ group, pathname, onNavigate }: { group: NavGroup; pathname: string; onNavigate?: () => void }) {
  const inside = contains(group.entries, pathname);
  const [open, setOpen] = useState(inside || Boolean(group.defaultOpen));
  useEffect(() => {
    if (inside) setOpen(true);
  }, [inside]);
  return (
    <div className="ohs-nav-group">
      <button type="button" className="ohs-nav-group-btn" aria-expanded={open} onClick={() => setOpen(!open)}>
        <ChevronRight aria-hidden="true" className="ohs-caret" />
        <span className="ohs-nav-name">{group.name}</span>
        {group.count ? <span className="ohs-nav-count">{group.count}</span> : null}
      </button>
      <div className="ohs-nav-children" hidden={!open}>
        <Entries entries={group.entries} pathname={pathname} onNavigate={onNavigate} />
      </div>
    </div>
  );
}

function Entries({
  entries,
  pathname,
  onNavigate,
}: {
  entries: NavEntry[];
  pathname: string;
  onNavigate?: () => void;
}) {
  return entries.map((entry, i) =>
    entry.type === "page" ? (
      <PageLink key={entry.url} page={entry} pathname={pathname} onNavigate={onNavigate} />
    ) : entry.type === "group" ? (
      // biome-ignore lint/suspicious/noArrayIndexKey: the tree is fixed at build time, and names can repeat
      <Group key={`${entry.name}-${i}`} group={entry} pathname={pathname} onNavigate={onNavigate} />
    ) : (
      // biome-ignore lint/suspicious/noArrayIndexKey: the tree is fixed at build time, and names can repeat
      <p key={`${entry.name}-${i}`} className="ohs-nav-label">
        {entry.name}
      </p>
    ),
  );
}

function Section({
  name,
  entries,
  first,
  pathname,
  onNavigate,
}: {
  name?: string;
  entries: NavEntry[];
  first: boolean;
  pathname: string;
  onNavigate?: () => void;
}) {
  const inside = contains(entries, pathname);
  // Sections of groups (the API endpoints) stay open: their groups fold instead.
  const onlyGroups = entries.every((e) => e.type === "group");
  const [open, setOpen] = useState(first || inside || onlyGroups);
  useEffect(() => {
    if (inside) setOpen(true);
  }, [inside]);
  if (!name) {
    return (
      <div className="ohs-nav-section">
        <Entries entries={entries} pathname={pathname} onNavigate={onNavigate} />
      </div>
    );
  }
  return (
    <div className="ohs-nav-section">
      <button type="button" className="ohs-nav-section-btn" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span>{name}</span>
        <ChevronRight aria-hidden="true" className="ohs-caret" />
      </button>
      <div className="ohs-nav-children" hidden={!open}>
        <Entries entries={entries} pathname={pathname} onNavigate={onNavigate} />
      </div>
    </div>
  );
}

/** The sidebar: the three parts of the site, then the part the page is in, folded by section. */
export function Sidebar({ nav, onNavigate }: { nav: NavRoot[]; onNavigate?: () => void }) {
  const pathname = usePathname();
  const root = rootOf(nav, pathname) ?? nav[0];
  return (
    <nav className="ohs-sidebar-nav" aria-label="Documentation">
      <ul className="ohs-roots">
        {nav.map((r, i) => {
          const Icon = rootIcons[i] ?? BookOpen;
          return (
            <li key={r.id}>
              <Link
                href={r.url}
                className="ohs-root"
                aria-current={r === root ? "true" : undefined}
                onClick={onNavigate}
              >
                <span className="ohs-root-icon">
                  <Icon aria-hidden="true" />
                </span>
                <span className="ohs-nav-name">{r.name}</span>
              </Link>
            </li>
          );
        })}
      </ul>
      {root
        ? root.sections.map((section, i) => (
            <Section
              key={`${root.id}-${section.name ?? i}`}
              name={section.name}
              entries={section.entries}
              first={i === 0}
              pathname={pathname}
              onNavigate={onNavigate}
            />
          ))
        : null}
    </nav>
  );
}
