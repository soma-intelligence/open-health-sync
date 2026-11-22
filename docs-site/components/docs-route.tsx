import { createRelativeLink } from "fumadocs-ui/mdx";
import { ArrowLeft, ArrowRight } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import type { ComponentProps } from "react";
import { APIPage } from "@/components/api-page";
import { PageActions } from "@/components/layout/page-actions";
import { TocAside, TocInline } from "@/components/layout/toc";
import { docsLink, getMDXComponents } from "@/components/mdx";
import { asset } from "@/lib/asset";
import { getNav } from "@/lib/nav";
import { neighboursOf, trailOf } from "@/lib/nav-model";
import { openapi } from "@/lib/openapi";
import { getPageEditUrl, getPageImageUrl, getPageMarkdownUrl } from "@/lib/shared";
import { source } from "@/lib/source";

/** A guide or reference page, shared by the route under /documentation and the one at the root. */
export async function DocsRoutePage({ slug }: { slug?: string[] }) {
  const page = source.getPage(slug);
  if (!page) notFound();

  const MDX = page.data.body;
  const nav = getNav();
  const trail = trailOf(nav, page.url);
  const { previous, next } = neighboursOf(nav, page.url);
  const toc = page.data.full ? [] : page.data.toc;
  const endpoint = Boolean(page.data._openapi);
  const { preloaded } = endpoint ? await openapi.preloadOpenAPIPage(page) : { preloaded: undefined };

  return (
    <article className="ohs-page" data-wide={endpoint || page.data.full ? "" : undefined}>
      <div className="ohs-page-main">
        {trail.length > 1 ? (
          <nav aria-label="Breadcrumb" className="ohs-crumbs">
            <ol>
              {trail.map((name, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: a trail can repeat a name
                <li key={i}>{name}</li>
              ))}
            </ol>
          </nav>
        ) : null}
        <header className="ohs-page-head">
          <h1>{page.data.title}</h1>
          {page.data.description ? <p className="ohs-page-lede">{page.data.description}</p> : null}
        </header>
        {toc.length > 1 ? <TocInline toc={toc} /> : null}
        <div className={endpoint ? "ohs-endpoint" : "ohs-prose prose"}>
          <MDX
            components={getMDXComponents({
              a: docsLink(createRelativeLink(source, page)),
              OpenAPIPage: (p: ComponentProps<typeof APIPage>) =>
                preloaded ? <APIPage {...p} preloaded={preloaded} /> : null,
            })}
          />
        </div>
        <footer className="ohs-page-foot">
          <PageActions markdownUrl={getPageMarkdownUrl(page).url} editUrl={getPageEditUrl(page)} />
          {previous || next ? (
            <nav aria-label="Previous and next pages" className="ohs-pager">
              {previous ? (
                <Link href={previous.url} className="ohs-pager-card" rel="prev">
                  <small>
                    <ArrowLeft aria-hidden="true" /> Previous
                  </small>
                  <span>{previous.name}</span>
                </Link>
              ) : (
                <span />
              )}
              {next ? (
                <Link href={next.url} className="ohs-pager-card ohs-pager-next" rel="next">
                  <small>
                    Next <ArrowRight aria-hidden="true" />
                  </small>
                  <span>{next.name}</span>
                </Link>
              ) : null}
            </nav>
          ) : null}
        </footer>
      </div>
      {toc.length > 1 ? (
        <aside className="ohs-page-aside" aria-label="On this page">
          <TocAside toc={toc} />
        </aside>
      ) : null}
    </article>
  );
}

export function docsStaticParams() {
  return source.generateParams();
}

export function docsMetadata(slug?: string[]): Metadata {
  const page = source.getPage(slug);
  if (!page) notFound();

  return {
    title: page.data.title,
    description: page.data.description,
    alternates: { canonical: asset(page.url) },
    openGraph: {
      images: asset(getPageImageUrl(page).url),
    },
  };
}
