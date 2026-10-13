import { absoluteUrl } from "@/lib/asset";
import { appName } from "@/lib/shared";
import { docsLlms } from "@/lib/source";

export const revalidate = false;

export async function GET() {
  // The index links pages by their path inside the site; readers need the address they are served at.
  // The generated index opens with a generic "# Docs"; llms.txt wants the project's name and a summary.
  const index = (await docsLlms.index()).replace(
    /^# .*\n/,
    `# ${appName}\n\n> Self-hosted health data aggregator: pulls data from wearables and health apps, normalizes it to one model, and serves it as REST, FHIR R4, Open mHealth, CSV, Parquet and a read-only MCP server.\n`,
  );
  return new Response(index.replace(/\]\((\/[^)\s]*)\)/g, (_, path: string) => `](${absoluteUrl(path)})`));
}
