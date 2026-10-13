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
  "docs/connectors/recording.md": [
    {
      page: "(guides)/connectors/recording",
      title: "Record a connector",
      description: "Record a real account's answers, anonymized on your machine, so CI replays them on every change.",
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
  "docs/console.md": [
    {
      page: "(guides)/self-hosting/console",
      title: "Admin console",
      description: "A read-only page for operators: users, connections, freshness and webhook deliveries.",
    },
  ],
  "docs/backup-and-restore.md": [
    {
      page: "(guides)/self-hosting/backup",
      title: "Backup and restore",
      description: "The database, the raw store and the encryption key, taken and restored together.",
    },
  ],
  "docs/helm.md": [
    {
      page: "(guides)/self-hosting/kubernetes",
      title: "Kubernetes with Helm",
      description: "The Helm chart: an external Postgres, the migration Job, the raw store, values, upgrades.",
    },
  ],
  "docs/observability.md": [
    {
      page: "(guides)/self-hosting/observability",
      title: "Observability",
      description: "Logs, Prometheus metrics and OpenTelemetry traces: what each carries and how to turn traces on.",
    },
  ],
  "docs/deploy-platforms.md": [
    {
      page: "(guides)/self-hosting/platforms",
      title: "Render, Fly.io and Railway",
      description: "Deploy without a server of your own: templates with TimescaleDB and a persistent raw store.",
    },
  ],
  "docs/fhir.md": [
    {
      page: "(guides)/guides/fhir",
      title: "FHIR R4 export",
      description: "Read records as FHIR R4 Observations, with the Patient, its Devices and Provenance.",
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
  "docs/connect-page.md": [
    {
      page: "(guides)/guides/connect-page",
      title: "Hosted connection page",
      description: "Send users to a page served by Open Health Sync to connect their sources, no OAuth UI to build.",
    },
  ],
  "docs/sync-health.md": [
    {
      page: "(guides)/guides/sync-health",
      title: "Sync history and connection health",
      description: "Why a user's data is not arriving: connection health, the last pulls, and sync now.",
    },
  ],
  "docs/demo.md": [
    {
      page: "(guides)/guides/demo",
      title: "Try it without a wearable",
      description: "Fill an instance with synthetic data from six sources and try every read route.",
    },
  ],
  "docs/webhooks.md": [
    {
      page: "(guides)/guides/webhooks",
      title: "Outgoing webhooks",
      description: "Be told when data changes: events, signature verification, retries and the routes.",
    },
  ],
  "docs/hrv.md": [
    {
      page: "(guides)/guides/hrv",
      title: "RR intervals and HRV",
      description: "Read beat-to-beat intervals and the HRV derived from each heartbeat series.",
    },
  ],
  "docs/menstrual-cycles.md": [
    {
      page: "(guides)/guides/menstrual-cycles",
      title: "Menstrual cycles",
      description: "Cycles derived at read time from the recorded menstrual flow, and the rules they follow.",
    },
  ],
  "docs/time-tags.md": [
    {
      page: "(guides)/guides/time-tags",
      title: "Time tags",
      description: "Mark the periods that give context to the data: illness, travel, jet lag and more.",
    },
  ],
  "docs/audit-log.md": [
    {
      page: "(guides)/guides/audit-log",
      title: "Audit log",
      description: "Who read, exported or deleted which user's data, when and with which key.",
    },
  ],
  "docs/consent.md": [
    {
      page: "(guides)/guides/consent",
      title: "Consent",
      description: "Per user, per source and per data category: what is stored, revocation and its purge.",
    },
  ],
  "docs/server-sdks.md": [
    {
      page: "(guides)/guides/server-sdks",
      title: "Server SDKs",
      description: "TypeScript and Python clients for your backend, generated from the OpenAPI document.",
    },
  ],
  "docs/api-collections.md": [
    {
      page: "(guides)/guides/api-collections",
      title: "API collections",
      description: "Try the API from Postman, Bruno or Insomnia with the generated collection.",
    },
  ],
};

// The SDK overview, then one page per SDK guide under docs/sdks.
sources["docs/sdks/README.md"] = [
  {
    page: "(guides)/sdks/index",
    title: "Client SDKs",
    description: "Kotlin, Swift, React Native and Flutter clients: what they share and which credential goes where.",
  },
];
for (const file of readdirSync(join(repo, "docs", "sdks")).sort()) {
  if (!file.endsWith(".md") || file === "README.md") continue;
  sources[`docs/sdks/${file}`] = [{ page: `(guides)/sdks/${file.replace(/\.md$/, "")}` }];
}

// Each provider guide under docs/connectors becomes a page of its own.
for (const file of readdirSync(join(repo, "docs", "connectors")).sort()) {
  if (!file.endsWith(".md") || file === "README.md" || sources[`docs/connectors/${file}`]) continue;
  sources[`docs/connectors/${file}`] = [{ page: `(guides)/connectors/${file.replace(/\.md$/, "")}` }];
}

// The recipes: the index page, then one page per recipe.
sources["docs/recipes/README.md"] = [
  {
    page: "(guides)/recipes/index",
    title: "Build with your data",
    description: "Small tools on top of all your health data: set up a personal instance and run a recipe.",
  },
];
for (const file of readdirSync(join(repo, "docs", "recipes")).sort()) {
  if (!file.endsWith(".md") || file === "README.md") continue;
  sources[`docs/recipes/${file}`] = [{ page: `(guides)/recipes/${file.replace(/\.md$/, "")}` }];
}

// Learn by building: the index page, then one page per project.
sources["docs/learn/README.md"] = [
  {
    page: "(guides)/learn/index",
    title: "Learn by building",
    description: "Seven short projects on your own health data, from a first sync to a webhook, plus a prompt library.",
  },
];
for (const file of readdirSync(join(repo, "docs", "learn")).sort()) {
  if (!file.endsWith(".md") || file === "README.md") continue;
  sources[`docs/learn/${file}`] = [{ page: `(guides)/learn/${file.replace(/\.md$/, "")}` }];
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
  "(guides)/connectors/recording": "Disc",
  "(guides)/connectors/apple-health": "Smartphone",
  "(guides)/self-hosting/kubernetes": "Ship",
  "(guides)/self-hosting/platforms": "Cloud",
  "(guides)/self-hosting/doctor": "Stethoscope",
  "(guides)/self-hosting/configuration": "Settings",
  "(guides)/self-hosting/upgrading": "CircleArrowUp",
  "(guides)/self-hosting/backup": "DatabaseBackup",
  "(guides)/self-hosting/rotating-secrets": "RotateCw",
  "(guides)/self-hosting/operating-notes": "NotebookPen",
  "(guides)/self-hosting/console": "LayoutDashboard",
  "(guides)/self-hosting/observability": "Telescope",
  "(guides)/guides/fhir": "HeartPulse",
  "(guides)/sdks/index": "Package",
  "(guides)/sdks/kotlin": "Smartphone",
  "(guides)/sdks/swift": "Tablet",
  "(guides)/sdks/react-native": "Atom",
  "(guides)/sdks/flutter": "Feather",
  "(guides)/guides/open-mhealth": "Activity",
  "(guides)/guides/exports": "FileDown",
  "(guides)/guides/webhooks": "Webhook",
  "(guides)/guides/menstrual-cycles": "CalendarDays",
  "(guides)/guides/time-tags": "Tag",
  "(guides)/recipes/index": "Sparkles",
  "(guides)/recipes/sleep-report": "Moon",
  "(guides)/recipes/heart-trend": "HeartPulse",
  "(guides)/recipes/compare-sources": "GitCompare",
  "(guides)/recipes/weekly-digest": "CalendarDays",
  "(guides)/recipes/dashboard": "ChartLine",
  "(guides)/recipes/export-all": "FileDown",
  "(guides)/recipes/ai-agent": "Bot",
  "(guides)/recipes/connect": "Link",
  "(guides)/guides/hrv": "Waves",
  "(guides)/learn/index": "GraduationCap",
  "(guides)/learn/first-sync": "Plug",
  "(guides)/learn/read-your-sleep": "Moon",
  "(guides)/learn/hrv-trend": "HeartPulse",
  "(guides)/learn/ask-an-ai": "Bot",
  "(guides)/learn/training-vs-sleep": "Dumbbell",
  "(guides)/learn/your-dashboard": "ChartLine",
  "(guides)/learn/sleep-alert": "BellRing",
  "(guides)/learn/prompts": "MessageSquareText",
  "(guides)/guides/connect-page": "MousePointerClick",
  "(guides)/guides/sync-health": "HeartHandshake",
  "(guides)/guides/demo": "FlaskConical",
  "(guides)/guides/server-sdks": "Package",
  "(guides)/guides/api-collections": "FolderTree",
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
  "docs/self-hosting.md#backup-and-restore": "/documentation/self-hosting/backup",
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
