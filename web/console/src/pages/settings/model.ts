/** The address segment of a settings section: lower case, spaces as hyphens. */
export function sectionSlug(section: string): string {
  return section.toLowerCase().replace(/\s+/g, "-");
}
