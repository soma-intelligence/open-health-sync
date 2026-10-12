import type { MetadataRoute } from "next";
import { absoluteUrl } from "@/lib/asset";
import { source } from "@/lib/source";

export const dynamic = "force-static";

// Every page at the address it is served at, so search engines find the guides and the API
// reference without following links from the home page.
export default function sitemap(): MetadataRoute.Sitemap {
  return [
    { url: absoluteUrl(""), changeFrequency: "weekly", priority: 1 },
    ...source.getPages().map((page) => ({
      url: absoluteUrl(page.url),
      changeFrequency: "weekly" as const,
      priority: page.slugs.length === 0 ? 0.9 : 0.7,
    })),
  ];
}
