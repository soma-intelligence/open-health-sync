import { DocsRoutePage, docsMetadata, docsStaticParams } from "@/components/docs-route";

// The guides at the root of the site, with no home page (DOCS_AT_ROOT=true, next.config.mjs).
type Props = { params: Promise<{ slug?: string[] }> };

export default async function Page({ params }: Props) {
  return <DocsRoutePage slug={(await params).slug} />;
}

export const generateStaticParams = docsStaticParams;

export async function generateMetadata({ params }: Props) {
  return docsMetadata((await params).slug);
}
