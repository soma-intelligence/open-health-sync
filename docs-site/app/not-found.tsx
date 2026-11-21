import { DocsBody, DocsDescription, DocsPage, DocsTitle } from "fumadocs-ui/layouts/notebook/page";
import { ArrowRight, BookOpen, Braces, Rocket } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import type { ReactNode } from "react";
import { DocsShell } from "@/components/docs-shell";
import { SearchButton } from "@/components/search-button";
import { docsHref } from "@/lib/shared";

export const metadata: Metadata = { title: "Page not found", robots: { index: false } };

const links: { href: string; icon: ReactNode; title: string; text: string }[] = [
  {
    href: docsHref("/documentation"),
    icon: <BookOpen className="size-4" />,
    title: "Documentation",
    text: "Start from the introduction and the table of contents.",
  },
  {
    href: docsHref("/documentation/quickstart"),
    icon: <Rocket className="size-4" />,
    title: "Quickstart",
    text: "Run an instance and read your first data in a few minutes.",
  },
  {
    href: docsHref("/documentation/api-reference"),
    icon: <Braces className="size-4" />,
    title: "API reference",
    text: "Every route, generated from the OpenAPI document.",
  },
];

/** Any address the export has no page for, inside the docs layout so the sidebar and search stay. */
export default function NotFound() {
  return (
    <DocsShell>
      <DocsPage toc={[]}>
        <p className="font-mono text-sm text-fd-muted-foreground">404</p>
        <DocsTitle>This page doesn't exist</DocsTitle>
        <DocsDescription>
          It may have moved when the docs were reorganized. Search for it, or start from one of these pages.
        </DocsDescription>
        <DocsBody>
          <div className="not-prose flex flex-col gap-6">
            <div>
              <SearchButton />
            </div>
            <div className="grid gap-3 sm:grid-cols-3">
              {links.map((link) => (
                <Link
                  key={link.href}
                  href={link.href}
                  className="group rounded-xl border bg-fd-card p-4 transition-colors hover:bg-fd-accent"
                >
                  <span className="mb-2 inline-flex items-center gap-2 font-medium text-fd-card-foreground">
                    {link.icon}
                    {link.title}
                    <ArrowRight className="size-3.5 opacity-0 transition-opacity group-hover:opacity-100" />
                  </span>
                  <span className="block text-sm text-fd-muted-foreground">{link.text}</span>
                </Link>
              ))}
            </div>
          </div>
        </DocsBody>
      </DocsPage>
    </DocsShell>
  );
}
