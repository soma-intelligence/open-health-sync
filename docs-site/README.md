# Documentation site

The documentation website for Open Health Sync: guides, the self-hosting handbook, connector
setup, and an API reference generated from [`api/openapi.yaml`](../api/openapi.yaml). It is a
static site built with [Fumadocs](https://fumadocs.dev) (MIT) on Next.js, so any static host
can serve it.

```sh
cd docs-site
npm ci
npm run dev      # http://localhost:3000, reloads on change
npm run build    # static site in out/
npm start        # serves out/ on http://localhost:3000
npm run check:links   # every internal link and anchor of out/ resolves
```

Node 22 or later.

## Where the content comes from

| Path | Source | Edit |
| --- | --- | --- |
| `content/docs/**/*.mdx` | written for the site: introduction, quickstart, concepts, install guides, tutorials | here |
| `content/docs/**/*.md` | copied from `../docs` by `scripts/sync-docs.mts` (connector guides, data model, architecture, API guide, MCP reference, self-hosting sections) | in `../docs`, never here: the copies are git-ignored and rebuilt by `npm run dev` and `npm run build` |
| `content/docs/api-reference/endpoints/` | generated from `../api/openapi.yaml` by `scripts/generate-api.mts` | in the OpenAPI file |

So the repository's Markdown under `docs/` stays the single source for reference pages: a new
file under `docs/connectors/` becomes a page without touching this folder. Relative links
between those files are rewritten to site URLs, and links to other repository files point to
GitHub. `sync-docs` warns about a link it cannot resolve.

The release shown in the header and on the home page is read from the default `OHS_VERSION` of
`deploy/docker-compose.yml`, which the release workflow checks against each tag.

## Writing pages

Pages are MDX. Besides Markdown, these components are available without an import: `Callout`,
`Cards` and `Card`, `Steps` and `Step`, `Tabs` and `Tab`, `Accordions` and `Accordion`,
`TypeTable`, `Files`, and three of this site's own: `Status` (`<Status is="planned" />`),
`Shot` (a light and dark capture from `docs/assets/readme`) and `Pipeline` (the data flow
diagram). Sidebar order lives in each folder's `meta.json`. A page's `icon` is a
[Lucide](https://lucide.dev/icons) name; `status: planned` or `status: soon` adds a sidebar
badge.

Same rules as the rest of the repository: no health values, keys or tokens in examples, say what
ships and what is planned, and run `npm run lint` before a pull request.

## Deploying

`npm run build` writes a static site to `out/`. Set `NEXT_PUBLIC_SITE_URL` to the public
address so canonical links and social previews resolve, and `NEXT_PUBLIC_BASE_PATH` to serve it
under a path. The API reference has no "try it" console: an instance only answers cross-origin
requests for `/openapi.yaml`.

The guides live under `/documentation`. The public site forwards `/open-health-sync/documentation`
and the other paths under `/open-health-sync` (assets, search, `llms.txt`) to a Vercel project
built from this directory, so the docs deploy with every change to `main`. The forwarding site
shows its own page at `/open-health-sync` itself, in place of this site's home page:

- Vercel project: this repository, Root Directory `docs-site`, framework Next.js, with
  "Include files outside the root directory" on (the build reads `docs/`, `api/` and `deploy/`).
- Environment: `NEXT_PUBLIC_BASE_PATH=/open-health-sync`, and `NEXT_PUBLIC_SITE_URL` set to the
  forwarding site's address.

A second layout serves the guides at the root of the site, for a domain whose own site shows the
project page at `/` and forwards `/docs` here. Build with `DOCS_AT_ROOT=true` and
`NEXT_PUBLIC_BASE_PATH=/docs`: the guides are then at `/docs/quickstart` and so on, this site's
home page is left out, and assets, search, `llms.txt`, preview images and the sitemap all live
under `/docs`. Links written as `/documentation/...` in pages are moved to `/docs/...` when they
render, so the same pages work in both layouts. CI builds both.

Every page names its address on the forwarding site as canonical, so search engines index that
one and not the Vercel address. Pages have no trailing slash, like most sites that forward: with
one, each side would redirect to the other's form and loop.
