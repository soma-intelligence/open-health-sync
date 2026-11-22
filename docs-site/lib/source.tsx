import { llms, loader } from "fumadocs-core/source";
import { metaSchema, pageSchema } from "fumadocs-core/source/schema";
import { defineDocs } from "fumadocs-mdx/macro";
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
      /** the route of a generated endpoint page, such as "/v1/users/{id}" (scripts/generate-api.mts) */
      route: z.string().optional(),
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
  // No tree plugins: lib/nav.ts reads the method and status badges from each page's data, and
  // the sidebar draws them, so page names stay plain text.
});

export const docsLlms = llms(source, {
  renderPage: async (page) => `# ${page.data.title} (${absoluteUrl(page.url)})

${(await page.data.getText("processed"))
  .replace(/\]\((\/documentation[^)\s]*)\)/g, (_, href: string) => `](${absoluteUrl(docsHref(href))})`)
  .replace(/href="(\/documentation[^"]*)"/g, (_, href: string) => `href="${absoluteUrl(docsHref(href))}"`)}`,
});
