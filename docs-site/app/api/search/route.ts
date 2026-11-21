import { createFromSource } from "fumadocs-core/search/server";
import { searchTagOf } from "@/lib/search-tags";
import { source } from "@/lib/source";

export const revalidate = false;

export const { staticGET: GET } = createFromSource(source, {
  // https://docs.orama.com/docs/orama-js/supported-languages
  language: "english",
  // Each page is tagged with its part of the site, so the dialog can filter on it. An endpoint page
  // has no text of its own: its method and route are indexed, so "/v1/users" or "webhooks" finds it.
  async buildIndex(page) {
    const { route, _openapi } = page.data as { route?: string; _openapi?: { method?: string } };
    const data = page.data.structuredData as unknown;
    const structuredData = (typeof data === "function" ? await data() : data) as typeof page.data.structuredData;
    return {
      id: page.url,
      url: page.url,
      title: page.data.title ?? page.url,
      description: page.data.description,
      tag: searchTagOf(page.slugs),
      structuredData:
        route && _openapi?.method
          ? {
              ...structuredData,
              contents: [{ heading: undefined, content: `${_openapi.method} ${route}` }, ...structuredData.contents],
            }
          : structuredData,
    };
  },
});
