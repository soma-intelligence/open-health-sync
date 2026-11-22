/** The search filters, one per part of the site. */
export const searchTags = [
  { value: "guides", label: "Guides" },
  { value: "api", label: "API reference" },
  { value: "platform", label: "Hosted beta" },
] as const;

export type SearchTag = (typeof searchTags)[number]["value"];

/** The part of the site a page belongs to, from its address inside the guides. */
export function searchTagOf(slugs: string[]): SearchTag {
  if (slugs[0] === "api-reference") return "api";
  if (slugs[0] === "platform") return "platform";
  return "guides";
}
