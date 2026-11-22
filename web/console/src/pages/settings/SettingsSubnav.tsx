import { useEffect, useState } from "react";
import { Link } from "react-router";

import { sectionSlug } from "./model";
import { sectionId } from "./SettingsSection";

/** One link per section; the one in view is marked as the page scrolls. */
export function SettingsSubnav({ sections, current, changed }: { sections: readonly string[]; current: string | null; changed: ReadonlySet<string> }) {
  const [seen, setSeen] = useState<string | null>(null);

  useEffect(() => {
    if (typeof IntersectionObserver === "undefined") return;
    const visible = new Map<string, boolean>();
    const observer = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          const slug = (e.target as HTMLElement).dataset["slug"];
          if (slug) visible.set(slug, e.isIntersecting);
        }
        const top = sections.map(sectionSlug).find((slug) => visible.get(slug));
        if (top) setSeen(top);
      },
      { rootMargin: "0px 0px -60% 0px" },
    );
    for (const section of sections) {
      const el = document.getElementById(sectionId(section));
      if (el) observer.observe(el);
    }
    return () => {
      observer.disconnect();
    };
  }, [sections]);

  const active = seen ?? current ?? (sections[0] ? sectionSlug(sections[0]) : null);
  return (
    <nav className="set-subnav" aria-label="Settings sections">
      {sections.map((section) => {
        const slug = sectionSlug(section);
        return (
          <Link key={slug} to={`/settings/${slug}`} className={slug === active ? "on" : undefined} aria-current={slug === active ? "true" : undefined}>
            {section}
            {changed.has(section) ? <i className="set-dot" aria-hidden="true" /> : null}
          </Link>
        );
      })}
    </nav>
  );
}
