import { Bot, Braces, GraduationCap, Plug, Rocket, Server } from "lucide-react";
import Link from "next/link";
import type { ReactNode } from "react";
import { MethodBadge } from "@/components/method-badge";
import { docsHref, signupUrl } from "@/lib/shared";
import { source } from "@/lib/source";

const tiles: { href: string; icon: ReactNode; title: string; text: string }[] = [
  {
    href: "/documentation/quickstart",
    icon: <Rocket aria-hidden="true" />,
    title: "Quickstart",
    text: "Run an instance, upload a reading and read it back, in five minutes.",
  },
  {
    href: "/documentation/connectors",
    icon: <Plug aria-hidden="true" />,
    title: "Sources",
    text: "Apple Health, Health Connect, Oura, WHOOP, Garmin and more, in one model.",
  },
  {
    href: "/documentation/api-reference",
    icon: <Braces aria-hidden="true" />,
    title: "API reference",
    text: "Every route, generated from the OpenAPI 3.1 file that leads the code.",
  },
  {
    href: "/documentation/self-hosting",
    icon: <Server aria-hidden="true" />,
    title: "Self-hosting",
    text: "One Go binary and Postgres: Docker Compose, a Raspberry Pi 5 or any cloud.",
  },
  {
    href: "/documentation/learn",
    icon: <GraduationCap aria-hidden="true" />,
    title: "Learn by building",
    text: "Short projects on your own data: a sleep report, an HRV trend, a dashboard.",
  },
  {
    href: "/documentation/guides/ai-agents",
    icon: <Bot aria-hidden="true" />,
    title: "AI agents",
    text: "A read-only MCP server, with a token that reads one person's data.",
  },
];

// The routes most integrations start with, linked to their reference pages.
const popular: [method: string, route: string][] = [
  ["GET", "/v1/users/{id}/samples"],
  ["POST", "/v1/users"],
  ["GET", "/v1/users/{id}/sleep"],
  ["POST", "/v1/webhooks"],
];

function endpointUrl(method: string, route: string) {
  return source.getPages().find((page) => {
    const data = page.data as { route?: string; _openapi?: { method?: string } };
    return data.route === route && data._openapi?.method?.toUpperCase() === method;
  })?.url;
}

/** The top of the documentation home: two ways in, six tiles and the routes people start with. */
export function DocsHome() {
  const routes = popular.flatMap(([method, route]) => {
    const url = endpointUrl(method, route);
    return url ? [{ method, route, url }] : [];
  });
  return (
    <div className="ohs-docs-home not-prose">
      <div className="ohs-actions">
        <Link href={docsHref("/documentation/quickstart")} className="ohs-btn ohs-btn-primary">
          Start the quickstart
        </Link>
        <Link href={docsHref("/documentation/api-reference")} className="ohs-btn ohs-btn-secondary">
          Browse the API
        </Link>
        <a
          href={`${signupUrl}?utm_source=docs&utm_medium=docs&utm_campaign=docs-home`}
          className="ohs-btn ohs-btn-ghost"
        >
          Or use the hosted beta
        </a>
      </div>
      <div className="ohs-tiles">
        {tiles.map((tile) => (
          <Link key={tile.href} href={docsHref(tile.href)} className="ohs-tile">
            <span className="ohs-tile-icon">{tile.icon}</span>
            <span className="ohs-tile-title">{tile.title}</span>
            <span className="ohs-tile-text">{tile.text}</span>
          </Link>
        ))}
      </div>
      {routes.length ? (
        <section aria-labelledby="popular-routes">
          <h2 id="popular-routes" className="ohs-subhead">
            Popular routes
          </h2>
          <ul className="ohs-routes">
            {routes.map((r) => (
              <li key={r.url}>
                <Link href={r.url}>
                  <MethodBadge method={r.method} />
                  <code>{r.route}</code>
                </Link>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}
