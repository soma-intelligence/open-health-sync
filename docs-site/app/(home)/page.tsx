import { ArrowRight, Bot, Braces, Database, FileArchive, Globe, Server } from "lucide-react";
import Link from "next/link";
import type { ReactNode } from "react";
import { latestVersion, repoUrl } from "@/lib/shared";

function Feature({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <div className="rounded-xl border bg-fd-card p-5">
      <div className="mb-3 inline-flex size-9 items-center justify-center rounded-lg border bg-fd-background text-fd-primary">
        {icon}
      </div>
      <h3 className="mb-1 font-medium">{title}</h3>
      <p className="text-sm leading-relaxed text-fd-muted-foreground">{children}</p>
    </div>
  );
}

export default function HomePage() {
  return (
    <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col px-4 py-16 md:py-24">
      <section className="flex flex-col items-center text-center">
        <span className="mb-6 rounded-full border border-fd-primary/30 bg-fd-primary/10 px-3 py-1 text-xs font-medium text-fd-primary">
          v{latestVersion} · early preview · AGPL-3.0
        </span>
        <h1 className="max-w-3xl text-4xl font-semibold tracking-tight md:text-6xl">
          Health data from devices and apps, in one clean model.
        </h1>
        <p className="mt-6 max-w-2xl text-lg text-fd-muted-foreground">
          Open Health Sync pulls from providers, accepts uploads from phones, keeps every payload verbatim, and serves
          normalized data over a REST API and a read-only MCP server. Self-hosted, free, one Go binary plus Postgres.
        </p>
        <div className="mt-8 flex flex-wrap justify-center gap-3">
          <Link
            href="/docs/quickstart"
            className="inline-flex items-center gap-2 rounded-full bg-fd-primary px-5 py-2.5 text-sm font-medium text-fd-primary-foreground transition-opacity hover:opacity-90"
          >
            Quickstart <ArrowRight className="size-4" />
          </Link>
          <Link
            href="/docs"
            className="inline-flex items-center gap-2 rounded-full border bg-fd-background px-5 py-2.5 text-sm font-medium transition-colors hover:bg-fd-accent"
          >
            Read the docs
          </Link>
          <a
            href={repoUrl}
            className="inline-flex items-center gap-2 rounded-full border bg-fd-background px-5 py-2.5 text-sm font-medium transition-colors hover:bg-fd-accent"
          >
            GitHub
          </a>
        </div>
        <pre className="mt-10 w-full max-w-xl overflow-x-auto rounded-xl border bg-fd-card px-5 py-4 text-left font-mono text-sm">
          <span className="select-none text-fd-muted-foreground">$ </span>
          docker pull ghcr.io/soma-intelligence/open-health-sync:{latestVersion}
        </pre>
      </section>

      <section className="mt-20 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Feature icon={<Database className="size-4" />} title="One canonical model">
          Samples, sleep, workouts, nutrition and daily summaries, with one unit per quantity and provenance on every
          record.
        </Feature>
        <Feature icon={<FileArchive className="size-4" />} title="Raw payloads kept">
          Every upload and pull lands verbatim in the raw store first, so any user can be re-processed after a fix.
        </Feature>
        <Feature icon={<Globe className="size-4" />} title="Local days, not UTC days">
          Daily totals follow each record's own time zone, and two devices are never added together.
        </Feature>
        <Feature icon={<Braces className="size-4" />} title="OpenAPI first">
          An OpenAPI 3.1 file leads the code, and contract tests hold the server to it.
        </Feature>
        <Feature icon={<Bot className="size-4" />} title="Read-only MCP">
          A person can point their own AI agent at their own data, with a token that can only read.
        </Feature>
        <Feature icon={<Server className="size-4" />} title="Runs small">
          A Raspberry Pi 5 with 4 GB runs the whole stack. The queue lives in Postgres: no broker.
        </Feature>
      </section>

      <section className="mt-16 flex flex-col items-center gap-2 rounded-2xl border border-dashed p-8 text-center">
        <h2 className="text-lg font-medium">Rather not run it yourself?</h2>
        <p className="max-w-xl text-sm text-fd-muted-foreground">
          A hosted Open Health Sync is in preparation, with the same API.{" "}
          <Link href="/docs/platform" className="font-medium text-fd-primary underline-offset-4 hover:underline">
            Read what is planned
          </Link>
          .
        </p>
      </section>
    </main>
  );
}
