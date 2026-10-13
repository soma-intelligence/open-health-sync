import type { Metadata } from "next";
import { Inter } from "next/font/google";
import { Provider } from "@/components/provider";
import { absoluteUrl } from "@/lib/asset";
import { appName, latestVersion, repoUrl } from "@/lib/shared";
import "./global.css";

const inter = Inter({
  subsets: ["latin"],
});

// NEXT_PUBLIC_SITE_URL is the address the site is served at, for social preview images.
export const metadata: Metadata = {
  metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL ?? "http://localhost:3000"),
  title: { default: "Open Health Sync", template: "%s · Open Health Sync" },
  description:
    "Self-hosted ingestion and normalization for health data: one canonical model, a REST API and a read-only MCP server.",
};

// What the software is, in schema.org terms, so search engines and AI assistants can describe it
// without guessing from the page text.
const softwareJsonLd = {
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  name: appName,
  description:
    "Self-hosted health data aggregator: pulls data from wearables and health apps, normalizes it to one model, and serves it as REST, FHIR R4, Open mHealth, CSV, Parquet and a read-only MCP server.",
  applicationCategory: "HealthApplication",
  operatingSystem: "Linux, macOS, Docker",
  url: absoluteUrl("") || undefined,
  sameAs: [repoUrl],
  license: "https://www.gnu.org/licenses/agpl-3.0.html",
  softwareVersion: latestVersion || undefined,
  isAccessibleForFree: true,
  offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
};

export default function Layout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" className={inter.className} suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <script
          type="application/ld+json"
          // biome-ignore lint/security/noDangerouslySetInnerHtml: static JSON written above, no user input
          dangerouslySetInnerHTML={{ __html: JSON.stringify(softwareJsonLd) }}
        />
        <Provider>{children}</Provider>
      </body>
    </html>
  );
}
