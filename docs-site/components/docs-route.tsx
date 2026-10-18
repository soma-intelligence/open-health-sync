import {
  DocsBody,
  DocsDescription,
  DocsPage,
  DocsTitle,
  EditOnGitHub,
  MarkdownCopyButton,
  ViewOptionsPopover,
} from "fumadocs-ui/layouts/notebook/page";
import { createRelativeLink } from "fumadocs-ui/mdx";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import type { ComponentProps } from "react";
import { APIPage } from "@/components/api-page";
import { docsLink, getMDXComponents } from "@/components/mdx";
import { asset } from "@/lib/asset";
import { openapi } from "@/lib/openapi";
import { getPageEditUrl, getPageImageUrl, getPageMarkdownUrl } from "@/lib/shared";
import { source } from "@/lib/source";

/** A guide or reference page, shared by the route under /documentation and the one at the root. */
export async function DocsRoutePage({ slug }: { slug?: string[] }) {
  const page = source.getPage(slug);
  if (!page) notFound();

  const MDX = page.data.body;
  const markdownUrl = getPageMarkdownUrl(page).url;
  const editUrl = getPageEditUrl(page);
  const { preloaded } = page.data._openapi ? await openapi.preloadOpenAPIPage(page) : { preloaded: undefined };

  return (
    <DocsPage toc={page.data.toc} full={page.data.full} tableOfContent={{ style: "clerk" }}>
      <DocsTitle>{page.data.title}</DocsTitle>
      <DocsDescription className="mb-0">{page.data.description}</DocsDescription>
      <div className="flex flex-row flex-wrap gap-2 items-center border-b pb-6">
        <MarkdownCopyButton markdownUrl={markdownUrl} />
        <ViewOptionsPopover markdownUrl={markdownUrl} githubUrl={editUrl} />
      </div>
      <DocsBody>
        <MDX
          components={getMDXComponents({
            a: docsLink(createRelativeLink(source, page)),
            OpenAPIPage: (p: ComponentProps<typeof APIPage>) =>
              preloaded ? <APIPage {...p} preloaded={preloaded} /> : null,
          })}
        />
      </DocsBody>
      <EditOnGitHub href={editUrl} />
    </DocsPage>
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
