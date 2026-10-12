import { absoluteUrl } from "@/lib/asset";
import { docsLlms } from "@/lib/source";

export const revalidate = false;

export async function GET() {
  // The index links pages by their path inside the site; readers need the address they are served at.
  const index = await docsLlms.index();
  return new Response(index.replace(/\]\((\/[^)\s]*)\)/g, (_, path: string) => `](${absoluteUrl(path)})`));
}
