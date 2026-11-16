import { useCallback, useMemo, useState } from "react";

import { Button } from "./Button";

export interface PagerState {
  /** The cursor of the page on screen; undefined on the first. */
  cursor: string | undefined;
  /** Moves to the page after this one; a null cursor (the last page) is ignored. */
  next: (cursor: string | null) => void;
  prev: () => void;
  /** Back to the first page. */
  reset: () => void;
  /** 1 based. */
  page: number;
}

/** The cursors of the pages visited, so Previous can go back without the API knowing how. */
export function usePager(): PagerState {
  const [stack, setStack] = useState<(string | undefined)[]>([undefined]);
  const next = useCallback((cursor: string | null) => {
    if (cursor === null) return;
    setStack((s) => [...s, cursor]);
  }, []);
  const prev = useCallback(() => {
    setStack((s) => (s.length > 1 ? s.slice(0, -1) : s));
  }, []);
  const reset = useCallback(() => {
    setStack((s) => (s.length > 1 ? [undefined] : s));
  }, []);
  return useMemo(() => ({ cursor: stack[stack.length - 1], next, prev, reset, page: stack.length }), [stack, next, prev, reset]);
}

/** Previous and Next under a list; nextCursor is the answer's next_cursor. */
export function Pager({ pager, nextCursor }: { pager: PagerState; nextCursor: string | null | undefined }) {
  if (pager.page === 1 && !nextCursor) return null;
  return (
    <nav className="pager" aria-label="Pages">
      <Button size="sm" disabled={pager.page === 1} onClick={pager.prev}>
        Previous
      </Button>
      <span className="muted">Page {pager.page}</span>
      <Button
        size="sm"
        disabled={!nextCursor}
        onClick={() => {
          pager.next(nextCursor ?? null);
        }}
      >
        Next
      </Button>
    </nav>
  );
}
