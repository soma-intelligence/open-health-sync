import type { Metadata } from "next";
import { Inter } from "next/font/google";
import { Provider } from "@/components/provider";
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

export default function Layout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" className={inter.className} suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <Provider>{children}</Provider>
      </body>
    </html>
  );
}
