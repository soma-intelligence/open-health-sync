import { DocsRoutePage, docsMetadata, docsStaticParams } from "@/components/docs-route";

// The guides under /documentation, next to the home page (the default build).
type Props = { params: Promise<{ slug?: string[] }> };

export default async function Page({ params }: Props) {
  return <DocsRoutePage slug={(await params).slug} />;
}

export const generateStaticParams = docsStaticParams;

export async function generateMetadata({ params }: Props) {
  return docsMetadata((await params).slug);
}
