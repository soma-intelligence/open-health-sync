import { llms, loader } from "fumadocs-core/source";
import { lucideIconsPlugin } from "fumadocs-core/source/plugins/lucide-icons";
import { statusBadgesPlugin } from "fumadocs-core/source/plugins/status-badges";
import { metaSchema, pageSchema } from "fumadocs-core/source/schema";
import { defineDocs } from "fumadocs-mdx/macro";
import { openapiPlugin } from "fumadocs-openapi/server";
import { z } from "zod";
import { absoluteUrl } from "./asset";
import { docsHref, docsRoute } from "./shared";

const docs = defineDocs({
  dir: "content/docs",
  docs: {
    schema: pageSchema.extend({
      /** the file under the repository's docs/ this page is synced from */
      source: z.string().optional(),
      /** a sidebar badge, such as "planned" or "soon" */
      status: z.string().optional(),
    }),
    postprocess: {
      includeProcessedMarkdown: true,
    },
  },
  meta: {
    schema: metaSchema,
  },
});

export const source = loader({
  baseUrl: docsRoute,
  source: docs.toFumadocsSource(),
  plugins: [
    lucideIconsPlugin(),
    openapiPlugin(),
    statusBadgesPlugin({
      renderBadge: (status) => (
        <span className="ms-auto rounded-full border px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-fd-muted-foreground">
          {status}
        </span>
      ),
    }),
  ],
});

export const docsLlms = llms(source, {
  renderPage: async (page) => `# ${page.data.title} (${absoluteUrl(page.url)})

${(await page.data.getText("processed"))
  .replace(/\]\((\/documentation[^)\s]*)\)/g, (_, href: string) => `](${absoluteUrl(docsHref(href))})`)
  .replace(/href="(\/documentation[^"]*)"/g, (_, href: string) => `href="${absoluteUrl(docsHref(href))}"`)}`,
});
