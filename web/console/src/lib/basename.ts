/** Where the console is mounted; the router basename and every absolute console path. */
export const BASENAME = "/console";

/** A console path ("/console/users") as the router takes it ("/users"): the router adds the basename itself. */
export function routerPath(to: string): string {
  return to.startsWith(`${BASENAME}/`) ? to.slice(BASENAME.length) : to;
}
