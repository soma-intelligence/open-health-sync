import { BASENAME } from "./basename";

/**
 * v0.2.0 shipped hash routes (#/users/<id>, #/providers/<slug>, #/webhooks/<id>).
 * The path routes have the same shapes: an old bookmark maps to
 * BASENAME + the hash path. Returns null for any other hash, and for a
 * dot segment (plain or percent-encoded), which would resolve outside BASENAME.
 */
export function legacyHashPath(hash: string): string | null {
  if (!hash.startsWith("#/") || hash.startsWith("#//") || hash.length <= 2) return null;
  const path = hash.slice(1).split(/[?#]/)[0] ?? "";
  if (path.split("/").some((seg) => /^(\.|%2e){1,2}$/i.test(seg))) return null;
  return BASENAME + hash.slice(1);
}
