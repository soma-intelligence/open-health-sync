import { ArrowRight, Bot, Braces, Database, FileArchive, Globe, Server } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import type { ReactNode } from "react";
import { asset } from "@/lib/asset";
import { latestVersion, repoUrl, signupUrl } from "@/lib/shared";

// The README's "Sources and outputs" table, in short.
const available = [
  "Eighteen sources: Apple Health, Health Connect, Withings, Oura, WHOOP, Fitbit, Garmin, Dexcom, OMRON and more",
  "REST API described by OpenAPI 3.1",
  "Read-only MCP server",
  "FHIR R4 and Open mHealth",
  "CSV and Parquet exports, outgoing webhooks",
  "Swift, Kotlin, React Native and Flutter SDKs",
];
const planned = ["Coros and a Garmin Personal API connector", "Health records (EHR) through SMART on FHIR"];

export const metadata: Metadata = { alternates: { canonical: asset("") || "/" } };

function Feature({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <div className="ohs-tile">
      <span className="ohs-tile-icon">{icon}</span>
      <h3 className="ohs-tile-title">{title}</h3>
      <p className="ohs-tile-text">{children}</p>
    </div>
  );
}

export default function HomePage() {
  return (
    <main className="ohs-home">
      <section className="ohs-home-hero">
        <span className="ohs-chip">v{latestVersion} · early preview · AGPL-3.0</span>
        <h1>Health data from devices and apps, in one clean model.</h1>
        <p className="ohs-page-lede">
          Open Health Sync pulls from providers, accepts uploads from phones, keeps every payload verbatim, and serves
          normalized data over a REST API and a read-only MCP server. Self-hosted, free, one Go binary plus Postgres.
        </p>
        <div className="ohs-actions">
          <Link href="/documentation/quickstart" className="ohs-btn ohs-btn-primary">
            Quickstart <ArrowRight aria-hidden="true" />
          </Link>
          <Link href="/documentation" className="ohs-btn ohs-btn-secondary">
            Read the docs
          </Link>
          <a href={repoUrl} className="ohs-btn ohs-btn-secondary">
            GitHub
          </a>
        </div>
        <pre className="ohs-home-cmd">
          <span aria-hidden="true">$ </span>
          docker pull ghcr.io/soma-intelligence/open-health-sync:{latestVersion}
        </pre>
      </section>

      <section className="ohs-tiles">
        <Feature icon={<Database aria-hidden="true" />} title="One canonical model">
          Samples, sleep, workouts, nutrition and daily summaries, with one unit per quantity and provenance on every
          record.
        </Feature>
        <Feature icon={<FileArchive aria-hidden="true" />} title="Raw payloads kept">
          Every upload and pull lands verbatim in the raw store first, so any user can be re-processed after a fix.
        </Feature>
        <Feature icon={<Globe aria-hidden="true" />} title="Local days, not UTC days">
          Daily totals follow each record's own time zone, and two devices are never added together.
        </Feature>
        <Feature icon={<Braces aria-hidden="true" />} title="OpenAPI first">
          An OpenAPI 3.1 file leads the code, and contract tests hold the server to it.
        </Feature>
        <Feature icon={<Bot aria-hidden="true" />} title="Read-only MCP">
          A person can point their own AI agent at their own data, with a token that can only read.
        </Feature>
        <Feature icon={<Server aria-hidden="true" />} title="Runs small">
          A Raspberry Pi 5 with 4 GB runs the whole stack. The queue lives in Postgres: no broker.
        </Feature>
      </section>

      <section className="ohs-tiles ohs-tiles-2">
        <div className="ohs-tile">
          <h2 className="ohs-tile-title">Available</h2>
          <ul className="ohs-list">
            {available.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
          <p className="ohs-tile-text">
            All of it ships in v{latestVersion}.{" "}
            <Link href="/documentation/connectors/sources" className="ohs-link">
              Every source
            </Link>
          </p>
        </div>
        <div className="ohs-tile">
          <h2 className="ohs-tile-title">Planned</h2>
          <ul className="ohs-list">
            {planned.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </div>
      </section>

      <section className="ohs-tile ohs-home-cta">
        <h2 className="ohs-tile-title">Rather not run it yourself?</h2>
        <p className="ohs-tile-text">
          The hosted Open Health Sync is in free beta with open sign-up, with the same API and nothing to run.{" "}
          <a href={`${signupUrl}?utm_source=docs&utm_medium=docs&utm_campaign=home`} className="ohs-link">
            Get started for free
          </a>{" "}
          or{" "}
          <Link href="/documentation/platform" className="ohs-link">
            read what the beta gives you
          </Link>
          .
        </p>
      </section>
    </main>
  );
}
