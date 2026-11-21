import type { Metadata } from "next";
import { Geist_Mono, Inter, Inter_Tight } from "next/font/google";
import { Provider } from "@/components/provider";
import { absoluteUrl } from "@/lib/asset";
import { appName, latestVersion, repoUrl } from "@/lib/shared";
import "./global.css";

// The three families of the design tokens (app/ohs-tokens.css), served by next/font. Their
// variables feed --op-font, --op-font-display and --op-font-mono in app/global.css.
const inter = Inter({ subsets: ["latin"], variable: "--font-inter" });
const interTight = Inter_Tight({ subsets: ["latin"], weight: ["600", "700"], variable: "--font-inter-tight" });
const geistMono = Geist_Mono({ subsets: ["latin"], variable: "--font-geist-mono" });

// NEXT_PUBLIC_SITE_URL is the address the site is served at, for social preview images.
export const metadata: Metadata = {
  metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL ?? "http://localhost:3000"),
  title: { default: "Open Health Sync", template: "%s · Open Health Sync" },
  description:
    "Open source, self-hosted health data aggregator and API: data from wearables and health apps in one model, over REST, FHIR R4, Open mHealth and a read-only MCP server.",
};

// What the software is, in schema.org terms, so search engines and AI assistants can describe it
// without guessing from the page text.
const softwareJsonLd = {
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  name: appName,
  description:
    "Open source, self-hosted health data aggregator: pulls data from wearables and health apps, normalizes it to one model, and serves it as REST, FHIR R4, Open mHealth, CSV, Parquet and a read-only MCP server.",
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
    <html
      lang="en"
      className={`${inter.variable} ${interTight.variable} ${geistMono.variable}`}
      suppressHydrationWarning
    >
      <head>
        <script
          type="application/ld+json"
          // biome-ignore lint/security/noDangerouslySetInnerHtml: static JSON written above, no user input
          dangerouslySetInnerHTML={{ __html: JSON.stringify(softwareJsonLd) }}
        />
      </head>
      <body>
        <Provider>{children}</Provider>
      </body>
    </html>
  );
}
