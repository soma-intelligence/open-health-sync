// Copies the Markdown under ../docs into the site, so the repository's docs stay the single
// source for reference pages. Every file this writes ends in .md and is git-ignored; pages
// written for the site itself end in .mdx. Run by `npm run dev` and `npm run build`.

import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, posix, relative, resolve } from "node:path";
import GithubSlugger from "github-slugger";

const site = resolve(import.meta.dirname, "..");
const repo = resolve(site, "..");
const content = join(site, "content", "docs");
const githubBlob = "https://github.com/soma-intelligence/open-health-sync/blob/main";

interface Target {
  /** page path under content/docs, without extension */
  page: string;
  title?: string;
  description?: string;
  /** keep only the part of the file under this `## ` heading */
  section?: string;
}

// Source file (relative to the repository root) -> pages on the site.
const sources: Record<string, Target[]> = {
  "docs/api.md": [
    {
      page: "api-reference/index",
      title: "Overview",
      description: "Credentials, conventions, pagination, errors and the routes, in one read.",
    },
  ],
  "docs/mcp.md": [
    {
      page: "api-reference/mcp",
      title: "MCP server reference",
      description: "Transports, credentials, the read-only tools and their arguments.",
    },
  ],
  "docs/data-model.md": [
    {
      page: "api-reference/data-model",
      title: "Data model",
      description: "Every canonical record, field and quantity name, with its unit, UCUM and LOINC codes.",
    },
  ],
  "docs/architecture.md": [
    {
      page: "(guides)/concepts/architecture",
      title: "Architecture",
      description: "Roles, the ingestion pipeline, storage, tenancy and reliability.",
    },
  ],
  "docs/background-sync.md": [
    {
      page: "(guides)/concepts/background-sync",
      title: "Background sync",
      description: "How each user's data stays current without a sync button, and how fresh it is.",
    },
  ],
  "docs/writing-a-connector.md": [
    {
      page: "(guides)/connectors/writing-a-connector",
      title: "Write a connector",
      description: "The connector contract, a minimal device connector, registration and fixture tests.",
    },
  ],
  "docs/connectors/README.md": [
    {
      page: "(guides)/connectors/sources",
      title: "Supported and planned sources",
      description: "Every source, its status, integration path, data types and tracking issue.",
    },
  ],
  "docs/self-hosting.md": [
    {
      page: "(guides)/self-hosting/doctor",
      section: "ohs doctor",
      title: "ohs doctor",
      description: "Check a configuration, the database, the raw store and every enabled connector.",
    },
    {
      page: "(guides)/self-hosting/configuration",
      section: "Configuration",
      title: "Configuration",
      description: "Every environment variable, the database pool size and provider keys per role.",
    },
    {
      page: "(guides)/self-hosting/upgrading",
      section: "Upgrading",
      title: "Upgrading",
      description: "Move to a new release safely, with a backup first.",
    },
    {
      page: "(guides)/self-hosting/backup",
      section: "Backup and restore",
      title: "Backup and restore",
      description: "The database, the raw store and the encryption key, taken and restored together.",
    },
    {
      page: "(guides)/self-hosting/rotating-secrets",
      section: "Rotating secrets",
      title: "Rotating secrets",
      description: "Tenant keys, the encryption key, provider secrets and the database password.",
    },
    {
      page: "(guides)/self-hosting/operating-notes",
      section: "Operating notes",
      title: "Operating notes",
      description:
        "Metrics exposure, crashes, long outages, re-processing a user and other things to know in production.",
    },
  ],
  "docs/fhir.md": [
    {
      page: "(guides)/guides/fhir",
      title: "FHIR R4 export",
      description: "Read quantity samples as FHIR R4 Observations.",
    },
  ],
  "docs/open-mhealth.md": [
    {
      page: "(guides)/guides/open-mhealth",
      title: "Open mHealth output",
      description: "Read records as Open mHealth data points, checked against the official schemas.",
    },
  ],
  "docs/exports.md": [
    {
      page: "(guides)/guides/exports",
      title: "CSV and Parquet exports",
      description: "Download every record of a list as one CSV or Parquet file.",
    },
  ],
};

// Each provider guide under docs/connectors becomes a page of its own.
for (const file of readdirSync(join(repo, "docs", "connectors")).sort()) {
  if (!file.endsWith(".md") || file === "README.md") continue;
  sources[`docs/connectors/${file}`] = [{ page: `(guides)/connectors/${file.replace(/\.md$/, "")}` }];
}

// Sidebar icons (lucide names) of the synced pages, by page path.
const icons: Record<string, string> = {
  "api-reference/index": "BookOpen",
  "api-reference/mcp": "Bot",
  "api-reference/data-model": "Shapes",
  "(guides)/concepts/architecture": "Network",
  "(guides)/concepts/background-sync": "RefreshCw",
  "(guides)/connectors/writing-a-connector": "Code",
  "(guides)/connectors/sources": "List",
  "(guides)/connectors/apple-health": "Smartphone",
  "(guides)/self-hosting/doctor": "Stethoscope",
  "(guides)/self-hosting/configuration": "Settings",
  "(guides)/self-hosting/upgrading": "CircleArrowUp",
  "(guides)/self-hosting/backup": "DatabaseBackup",
  "(guides)/self-hosting/rotating-secrets": "RotateCw",
  "(guides)/self-hosting/operating-notes": "NotebookPen",
  "(guides)/guides/fhir": "HeartPulse",
  "(guides)/guides/open-mhealth": "Activity",
  "(guides)/guides/exports": "FileDown",
};

// Anchors of a source file that live on a page written for the site (.mdx).
const handWritten: Record<string, string> = {
  "docs/self-hosting.md": "/documentation/self-hosting",
  "docs/self-hosting.md#quick-start": "/documentation/self-hosting/docker-compose",
  "docs/self-hosting.md#port-and-a-second-install":
    "/documentation/self-hosting/docker-compose#port-and-a-second-install",
  "docs/self-hosting.md#images-and-architectures":
    "/documentation/self-hosting/docker-compose#images-and-architectures",
  "docs/self-hosting.md#build-from-source": "/documentation/self-hosting/binaries#build-from-source",
  "docs/self-hosting.md#raspberry-pi-5-4-gb": "/documentation/self-hosting/raspberry-pi",
  "docs/self-hosting.md#s3-compatible-raw-store": "/documentation/self-hosting/s3",
  "README.md": "/documentation",
  "README.md#quickstart": "/documentation/quickstart",
};

function pageUrl(page: string): string {
  const slugs = page.split("/").filter((s) => !/^\(.*\)$/.test(s) && s !== "index");
  return `/documentation${slugs.length ? `/${slugs.join("/")}` : ""}`;
}

function slugsOf(markdown: string): string[] {
  const slugger = new GithubSlugger();
  const out: string[] = [];
  for (const line of stripCode(markdown).split("\n")) {
    const m = /^#{1,6}\s+(.*)$/.exec(line);
    if (m) out.push(slugger.slug(m[1].trim()));
  }
  return out;
}

function stripCode(markdown: string): string {
  return markdown.replace(/^```[\s\S]*?^```/gm, "");
}

function splitSection(markdown: string, heading: string): string {
  const lines = markdown.split("\n");
  let start = -1;
  let fenced = false;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].startsWith("```")) fenced = !fenced;
    if (fenced) continue;
    if (start < 0 && lines[i] === `## ${heading}`) {
      start = i + 1;
    } else if (start >= 0 && lines[i].startsWith("## ")) {
      return lines.slice(start, i).join("\n");
    }
  }
  if (start < 0) throw new Error(`section "## ${heading}" not found`);
  return lines.slice(start).join("\n");
}

// Where each anchor of each source file ends up: "docs/x.md#anchor" -> URL.
const anchors = new Map<string, string>(Object.entries(handWritten));
const bodies = new Map<string, { target: Target; body: string }[]>();

for (const [src, targets] of Object.entries(sources)) {
  const abs = join(repo, src);
  if (!existsSync(abs)) throw new Error(`sync-docs: ${src} is gone; update the sources map`);
  const raw = readFileSync(abs, "utf8");
  const out: { target: Target; body: string }[] = [];
  for (const target of targets) {
    let body = target.section ? splitSection(raw, target.section) : raw;
    let title = target.title;
    if (!target.section) {
      const h1 = /^# (.+)\n+/m.exec(body);
      if (h1) {
        title ??= h1[1].trim();
        body = body.replace(h1[0], "");
      }
      // A first heading that repeats the page title would show it twice.
      if (title) body = body.replace(new RegExp(`^## ${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\n+`, "mi"), "");
    } else {
      // The section's own `### ` headings become the page's `## ` headings.
      body = body.replace(/^###(#*) /gm, "##$1 ");
    }
    const url = pageUrl(target.page);
    if (!target.section && !anchors.has(src)) anchors.set(src, url);
    if (target.section) {
      // The section heading itself is the page; the headings under it are anchors on it.
      anchors.set(`${src}#${new GithubSlugger().slug(target.section)}`, url);
    }
    for (const slug of slugsOf(target.section ? splitSection(raw, target.section) : raw)) {
      if (!anchors.has(`${src}#${slug}`)) anchors.set(`${src}#${slug}`, `${url}#${slug}`);
    }
    out.push({ target: { ...target, title: title ?? target.page }, body });
  }
  bodies.set(src, out);
}

function rewriteLinks(markdown: string, src: string): string {
  const dir = posix.dirname(src);
  const rewrite = (href: string): string => {
    if (/^[a-z]+:|^#|^\//i.test(href)) return href;
    const [path, hash] = href.split("#");
    const target = posix.normalize(posix.join(dir, path));
    if (target.startsWith("docs/assets/")) return `/${target.slice("docs/".length)}`;
    const key = hash ? `${target}#${hash}` : target;
    const known = anchors.get(key) ?? (hash ? undefined : anchors.get(target));
    if (known) return known;
    if (hash && anchors.has(target)) return `${anchors.get(target)}#${hash}`;
    if (existsSync(join(repo, target))) return `${githubBlob}/${target}${hash ? `#${hash}` : ""}`;
    console.warn(`sync-docs: ${src}: unresolved link ${href}`);
    return href;
  };
  // Inline links and images, outside code fences and code spans.
  return markdown
    .split(/(^```[\s\S]*?^```)/m)
    .map((chunk, i) =>
      i % 2 === 1
        ? chunk
        : chunk
            .split(/(`[^`\n]*`)/)
            .map((part, j) =>
              j % 2 === 1 ? part : part.replace(/\]\(([^)\s]+)\)/g, (_, href) => `](${rewrite(href)})`),
            )
            .join(""),
    )
    .join("");
}

function firstParagraph(markdown: string): string {
  const para = stripCode(markdown)
    .split(/\n\s*\n/)
    .map((p) => p.trim())
    .find((p) => p && !p.startsWith("#") && !p.startsWith("|") && !p.startsWith("<") && !p.startsWith("-"));
  if (!para) return "";
  const text = para
    .replace(/\s+/g, " ")
    .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
    .replace(/`/g, "");
  // The first sentences that say something: "Slug `x`." alone does not.
  const sentences = text.replace(/^Slug \S+\.\s*/, "").match(/[^.]+(\.|$)/g) ?? [text];
  let out = "";
  for (const sentence of sentences) {
    out = `${out} ${sentence.trim()}`.trim();
    if (out.length >= 60) break;
  }
  return out.length > 200 ? `${out.slice(0, 197)}...` : out;
}

function yamlString(s: string): string {
  return JSON.stringify(s);
}

// Remove what the previous run wrote, then write again.
function removeSynced(dir: string) {
  if (!existsSync(dir)) return;
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name);
    if (entry.isDirectory()) removeSynced(p);
    else if (entry.name.endsWith(".md")) rmSync(p);
  }
}
removeSynced(content);

let written = 0;
for (const [src, pages] of bodies) {
  for (const { target, body } of pages) {
    const file = join(content, `${target.page}.md`);
    mkdirSync(dirname(file), { recursive: true });
    const description = target.description ?? firstParagraph(body);
    const front = [
      "---",
      `title: ${yamlString(target.title ?? "")}`,
      `icon: ${icons[target.page] ?? (target.page.includes("/connectors/") ? "Watch" : "FileText")}`,
      description ? `description: ${yamlString(description)}` : "",
      `source: ${yamlString(src)}`,
      "---",
      "",
    ]
      .filter((l) => l !== "")
      .join("\n");
    writeFileSync(file, `${front}\n\n${rewriteLinks(body, src).trim()}\n`);
    written++;
  }
}

// Images the docs and the pages use.
rmSync(join(site, "public", "assets"), { recursive: true, force: true });
cpSync(join(repo, "docs", "assets"), join(site, "public", "assets"), { recursive: true });

console.log(`sync-docs: ${written} pages from ${relative(site, join(repo, "docs"))}`);
