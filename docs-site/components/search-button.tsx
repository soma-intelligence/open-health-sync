"use client";
import { useSearchContext } from "fumadocs-ui/contexts/search";
import { Search } from "lucide-react";

/** A button that opens the search dialog, for pages that have no search field of their own. */
export function SearchButton() {
  const { enabled, setOpenSearch } = useSearchContext();
  if (!enabled) return null;
  return (
    <button
      type="button"
      onClick={() => setOpenSearch(true)}
      className="inline-flex items-center gap-2 rounded-full bg-fd-primary px-5 py-2.5 text-sm font-medium text-fd-primary-foreground transition-opacity hover:opacity-90"
    >
      <Search className="size-4" />
      Search the docs
      <kbd className="ms-1 rounded border border-current/40 px-1.5 font-mono text-xs">⌘ K</kbd>
    </button>
  );
}
