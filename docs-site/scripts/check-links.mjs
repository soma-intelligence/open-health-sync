// Checks every internal link and anchor of the static export in out/: run after npm run build.
// Usage: node scripts/check-links.mjs [out dir] [base path]
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const root = process.argv[2] ?? "out";
const base = process.argv[3] ?? process.env.NEXT_PUBLIC_BASE_PATH ?? "";
const files = [];
(function walk(d) {
  for (const e of readdirSync(d)) {
    const p = join(d, e);
    statSync(p).isDirectory() ? walk(p) : p.endsWith(".html") && files.push(p);
  }
})(root);
const ids = new Map();
const idsOf = (f) => {
  if (!ids.has(f)) {
    const h = readFileSync(f, "utf8");
    ids.set(f, new Set([...h.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])));
  }
  return ids.get(f);
};
const resolve = (path) => {
  const p = decodeURIComponent(path.slice(base.length));
  const cands = [
    join(root, p),
    join(root, p, "index.html"),
    join(root, `${p}.html`),
    join(root, `${p.replace(/\/$/, "")}.html`),
  ];
  return cands.find((c) => existsSync(c) && statSync(c).isFile());
};
let bad = 0,
  checked = 0;
for (const f of files) {
  const html = readFileSync(f, "utf8");
  for (const m of html.matchAll(/\s(?:href|src)="([^"]+)"/g)) {
    const url = m[1].replace(/&amp;/g, "&");
    if (/^(https?:|mailto:|data:|javascript:)/.test(url) || url.startsWith("//")) continue;
    if (url.startsWith("/_next/")) continue;
    const [path, hash] = url.split("#");
    let target = f;
    if (path) {
      if (!path.startsWith("/")) continue;
      target = resolve(path.split("?")[0]);
      checked++;
      if (!target) {
        console.log(`MISSING ${relative(root, f)} -> ${url}`);
        bad++;
        continue;
      }
    }
    if (hash && target.endsWith(".html") && !idsOf(target).has(decodeURIComponent(hash))) {
      console.log(`ANCHOR  ${relative(root, f)} -> ${url}`);
      bad++;
    }
  }
}
console.log(`${files.length} pages, ${checked} internal links, ${bad} broken`);
process.exit(bad ? 1 : 0);
