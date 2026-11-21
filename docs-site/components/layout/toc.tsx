"use client";
import { AnchorProvider, type TOCItemType, useActiveAnchor } from "fumadocs-core/toc";
import { ChevronRight } from "lucide-react";
import { useEffect, useRef } from "react";

function Items({ toc, onPick }: { toc: TOCItemType[]; onPick?: () => void }) {
  const active = useActiveAnchor();
  const list = useRef<HTMLOListElement>(null);
  useEffect(() => {
    if (!active) return;
    const item = list.current?.querySelector<HTMLElement>(`a[href="#${CSS.escape(active)}"]`);
    const box = list.current?.parentElement;
    if (item && box && box.scrollHeight > box.clientHeight) item.scrollIntoView({ block: "nearest" });
  }, [active]);
  return (
    <ol ref={list} className="ohs-toc-list">
      {toc.map((item) => (
        <li key={item.url} data-depth={Math.min(item.depth, 4)}>
          <a
            href={item.url}
            aria-current={active && item.url === `#${active}` ? "location" : undefined}
            onClick={onPick}
          >
            {item.title}
          </a>
        </li>
      ))}
    </ol>
  );
}

/** "On this page", beside the page from 1280 px. */
export function TocAside({ toc }: { toc: TOCItemType[] }) {
  return (
    <AnchorProvider toc={toc} single>
      <div className="ohs-toc">
        <p className="ohs-toc-title">On this page</p>
        <Items toc={toc} />
      </div>
    </AnchorProvider>
  );
}

/** "On this page" as a fold under the title, under 1280 px. */
export function TocInline({ toc }: { toc: TOCItemType[] }) {
  const details = useRef<HTMLDetailsElement>(null);
  return (
    <AnchorProvider toc={toc} single>
      <details ref={details} className="ohs-toc-inline">
        <summary>
          <span>On this page</span>
          <ChevronRight aria-hidden="true" className="ohs-caret" />
        </summary>
        <Items
          toc={toc}
          onPick={() => {
            if (details.current) details.current.open = false;
          }}
        />
      </details>
    </AnchorProvider>
  );
}
