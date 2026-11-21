import { ArrowRight, BookOpen, Braces, Rocket } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import type { ReactNode } from "react";
import { DocsShell } from "@/components/layout/docs-shell";
import { SearchButton } from "@/components/search-button";
import { docsHref } from "@/lib/shared";

export const metadata: Metadata = { title: "Page not found", robots: { index: false } };

const links: { href: string; icon: ReactNode; title: string; text: string }[] = [
  {
    href: docsHref("/documentation"),
    icon: <BookOpen aria-hidden="true" />,
    title: "Documentation",
    text: "Start from the introduction and the table of contents.",
  },
  {
    href: docsHref("/documentation/quickstart"),
    icon: <Rocket aria-hidden="true" />,
    title: "Quickstart",
    text: "Run an instance and read your first data in a few minutes.",
  },
  {
    href: docsHref("/documentation/api-reference"),
    icon: <Braces aria-hidden="true" />,
    title: "API reference",
    text: "Every route, generated from the OpenAPI document.",
  },
];

/** Any address the export has no page for, inside the docs layout so the sidebar and search stay. */
export default function NotFound() {
  return (
    <DocsShell>
      <article className="ohs-page">
        <div className="ohs-page-main">
          <header className="ohs-page-head">
            <p className="ohs-eyebrow">404</p>
            <h1>This page doesn't exist</h1>
            <p className="ohs-page-lede">
              It may have moved when the docs were reorganized. Search for it, or start from one of these pages.
            </p>
          </header>
          <div className="ohs-stack">
            <div>
              <SearchButton />
            </div>
            <div className="ohs-tiles">
              {links.map((link) => (
                <Link key={link.href} href={link.href} className="ohs-tile">
                  <span className="ohs-tile-icon">{link.icon}</span>
                  <span className="ohs-tile-title">
                    {link.title} <ArrowRight aria-hidden="true" className="ohs-tile-arrow" />
                  </span>
                  <span className="ohs-tile-text">{link.text}</span>
                </Link>
              ))}
            </div>
          </div>
        </div>
      </article>
    </DocsShell>
  );
}
