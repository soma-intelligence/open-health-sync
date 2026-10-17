// SPDX-License-Identifier: AGPL-3.0-only

import type { Metadata, Viewport } from "next";
import Link from "next/link";
import type { ReactNode } from "react";
import { Icon } from "../components/ui.tsx";
import { readConfig } from "../lib/config.ts";
import "./globals.css";

export const metadata: Metadata = {
  title: { default: "Open Health Sync web example", template: "%s · Open Health Sync web example" },
  description: "Create a user, get data in, read it back as JSON, FHIR R4 and Open mHealth, and receive signed webhooks.",
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#efeee9" },
    { media: "(prefers-color-scheme: dark)", color: "#0a0a0a" },
  ],
};

export default function RootLayout({ children }: { children: ReactNode }) {
  const { config } = readConfig();
  return (
    <html lang="en">
      <body>
        <header className="topbar">
          <div className="topbar-inner">
            <Link className="brand" href="/">
              <span className="logo">
                <Icon name="pulse" />
              </span>
              Open Health Sync
            </Link>
            <span className="tag">Web example</span>
            <span className="spacer" />
            <span className="instance" title="OHS_URL">
              <span className="label">Instance</span>
              <code>{config?.ohsUrl ?? "not set"}</code>
            </span>
          </div>
        </header>
        {children}
        <footer className="site-foot">
          <span>The tenant API key stays on this server: the browser only sees pages and form results.</span>
          <span>AGPL-3.0-only</span>
        </footer>
      </body>
    </html>
  );
}
