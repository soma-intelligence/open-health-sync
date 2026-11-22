"use client";
import { useSearchContext } from "fumadocs-ui/contexts/search";
import { Search } from "lucide-react";

/** A button that opens the search dialog, for pages that have no search field of their own. */
export function SearchButton() {
  const { enabled, setOpenSearch } = useSearchContext();
  if (!enabled) return null;
  return (
    <button type="button" onClick={() => setOpenSearch(true)} className="ohs-btn ohs-btn-primary">
      <Search aria-hidden="true" />
      Search the docs
    </button>
  );
}
