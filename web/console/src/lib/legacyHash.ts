import { BASENAME } from "./basename";

/**
 * v0.2.0 shipped hash routes (#/users/<id>, #/providers/<slug>, #/webhooks/<id>).
 * The path routes have the same shapes: an old bookmark maps to
 * BASENAME + the hash path. Returns null for any other hash.
 */
export function legacyHashPath(hash: string): string | null {
  if (!hash.startsWith("#/") || hash.startsWith("#//") || hash.length <= 2) return null;
  return BASENAME + hash.slice(1);
}
