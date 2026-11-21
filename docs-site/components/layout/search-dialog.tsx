"use client";
import { type SearchResultItem, type SearchResultRecord, useDocsSearch } from "fumadocs-core/search/client";
import type { SharedProps } from "fumadocs-ui/contexts/search";
import { CornerDownLeft, FileText, Hash, Search, TextIcon, X } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { type KeyboardEvent, type ReactNode, useEffect, useId, useMemo, useRef, useState } from "react";
import { asset } from "@/lib/asset";
import { type SearchTag, searchTags } from "@/lib/search-tags";

const icons = { page: FileText, heading: Hash, text: TextIcon } as const;

/** Rows of a table result come back one by one, under the page they belong to. */
function flatten(items: SearchResultItem[]): SearchResultRecord[] {
  return items
    .flatMap((item) => ("rows" in item ? item.rows : [item]))
    .filter((item) => /[a-z0-9]/i.test(plain(item.content).replace(/-{3,}/g, "")) && !/-{3,}/.test(item.content));
}

/** Plain text of a result: the index keeps a little Markdown. */
function plain(content: string): string {
  return content
    .replace(/<\/?mark>/g, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/[*`]/g, "")
    .replace(/\s*\|\s*/g, " · ")
    .trim();
}

/** Pages whose title has the words come first, each with its headings and passages. */
function titlesFirst(items: SearchResultRecord[], query: string): SearchResultRecord[] {
  const groups: SearchResultRecord[][] = [];
  for (const item of items) {
    if (item.type === "page" || groups.length === 0) groups.push([item]);
    else groups[groups.length - 1].push(item);
  }
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const hit = (g: SearchResultRecord[]) =>
    g[0].type === "page" && words.every((w) => plain(g[0].content).toLowerCase().includes(w)) ? 0 : 1;
  return groups
    .map((g, i) => ({ g, i }))
    .sort((a, b) => hit(a.g) - hit(b.g) || a.i - b.i)
    .flatMap(({ g }) => g);
}

function highlight(text: string, query: string): ReactNode {
  const words = query.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return text;
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})`, "gi");
  return text.split(re).map((part, i) =>
    // biome-ignore lint/suspicious/noArrayIndexKey: parts of one string, in order
    i % 2 === 1 ? <mark key={i}>{part}</mark> : <span key={i}>{part}</span>,
  );
}

/** The search dialog: one field, a filter per part of the site, results by page and heading. */
export function SearchDialog({ open, onOpenChange }: SharedProps) {
  const [tag, setTag] = useState<SearchTag | undefined>(undefined);
  const { search, setSearch, query } = useDocsSearch({ type: "static", from: asset("/api/search"), tag }, [tag]);
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const router = useRouter();
  const listId = useId();

  const results = useMemo(
    () => (query.data === "empty" ? [] : titlesFirst(flatten(query.data ?? []), search).slice(0, 40)),
    [query.data, search],
  );

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    const { overflow } = document.body.style;
    document.body.style.overflow = "hidden";
    input.current?.focus();
    return () => {
      document.body.style.overflow = overflow;
      previous?.focus?.();
    };
  }, [open]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: a new list starts at its first result
  useEffect(() => setActive(0), [results]);

  useEffect(() => {
    list.current?.querySelector(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  if (!open) return null;

  function go(url: string) {
    onOpenChange(false);
    router.push(url);
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      onOpenChange(false);
    } else if (e.key === "ArrowDown" && results.length) {
      e.preventDefault();
      setActive((i) => (i + 1) % results.length);
    } else if (e.key === "ArrowUp" && results.length) {
      e.preventDefault();
      setActive((i) => (i - 1 + results.length) % results.length);
    } else if (e.key === "Enter" && results[active]) {
      e.preventDefault();
      go(results[active].url);
    } else if (e.key === "Tab") {
      // Keep focus inside the dialog.
      const focusable = Array.from(
        (e.currentTarget as HTMLElement).querySelectorAll<HTMLElement>("input, button, a[href]"),
      );
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last?.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first?.focus();
      }
    }
  }

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: a click on the scrim closes, Escape does it from the keyboard
    <div className="ohs-search-scrim" onMouseDown={(e) => e.target === e.currentTarget && onOpenChange(false)}>
      <div
        className="ohs-search"
        role="dialog"
        aria-modal="true"
        aria-label="Search the documentation"
        onKeyDown={onKeyDown}
      >
        <div className="ohs-search-field">
          <Search aria-hidden="true" />
          <input
            ref={input}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search docs and API"
            aria-label="Search docs and API"
            aria-controls={listId}
            autoComplete="off"
            spellCheck={false}
          />
          <button type="button" className="ohs-icon-btn" aria-label="Close search" onClick={() => onOpenChange(false)}>
            <X aria-hidden="true" />
          </button>
        </div>
        <fieldset className="ohs-search-tags">
          <legend className="ohs-sr-only">Search in</legend>
          {[{ value: undefined, label: "All" } as const, ...searchTags].map((t) => (
            <button
              key={t.label}
              type="button"
              aria-pressed={tag === t.value}
              className="ohs-seg"
              onClick={() => setTag(t.value)}
            >
              {t.label}
            </button>
          ))}
        </fieldset>
        <div className="ohs-search-results">
          {search.trim() === "" ? (
            <p className="ohs-search-empty">Search guides, connectors and every API route.</p>
          ) : query.isLoading && results.length === 0 ? (
            <p className="ohs-search-empty">Searching…</p>
          ) : results.length === 0 ? (
            <p className="ohs-search-empty">No results for “{search}”.</p>
          ) : (
            <ul ref={list} id={listId} aria-label="Results">
              {results.map((item, i) => {
                const Icon = icons[item.type];
                return (
                  <li key={item.id} data-index={i}>
                    <Link
                      href={item.url}
                      className="ohs-search-hit"
                      data-type={item.type}
                      data-active={i === active ? "" : undefined}
                      onMouseMove={() => setActive(i)}
                      onClick={() => onOpenChange(false)}
                    >
                      <Icon aria-hidden="true" />
                      <span className="ohs-search-hit-text">
                        {item.type !== "page" && item.breadcrumbs?.length ? (
                          <small>{item.breadcrumbs.map(plain).join(" › ")}</small>
                        ) : null}
                        <span>{highlight(plain(item.content), search)}</span>
                      </span>
                      {i === active ? <CornerDownLeft aria-hidden="true" className="ohs-search-enter" /> : null}
                    </Link>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
        <div className="ohs-search-foot" aria-hidden="true">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> to move
          </span>
          <span>
            <kbd>↵</kbd> to open
          </span>
          <span>
            <kbd>esc</kbd> to close
          </span>
        </div>
      </div>
    </div>
  );
}
