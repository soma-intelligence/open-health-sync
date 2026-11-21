"use client";
import { useSearchContext } from "fumadocs-ui/contexts/search";
import { Menu, Search, X } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { Logo } from "@/components/logo";
import { consoleUrl, docsRoute, latestVersion, repoUrl, signupUrl, siteUrl } from "@/lib/shared";

/**
 * Header links, the same as the portal's: the project site, the console of the hosted beta and the
 * source. Guides, API reference and hosted platform are the three roots at the top of the sidebar.
 */
export const headerLinks = [
  { id: "site", label: "Site", href: siteUrl },
  { id: "console", label: "Console", href: consoleUrl },
  { id: "github", label: "GitHub", href: repoUrl },
] as const;

export const signupHref = `${signupUrl}?utm_source=docs&utm_medium=docs&utm_campaign=header`;

/** "⌘K" on Apple devices, "Ctrl K" elsewhere; set after hydration so the server HTML stays stable. */
function useShortcutLabel() {
  const [label, setLabel] = useState("⌘K");
  useEffect(() => {
    if (!/mac|iphone|ipad/i.test(navigator.userAgent)) setLabel("Ctrl K");
  }, []);
  return label;
}

export function Header({
  menu,
}: {
  /** the drawer of the sidebar, under 1024 px */
  menu?: { open: boolean; controls: string; toggle: () => void };
}) {
  const { setOpenSearch } = useSearchContext();
  const shortcut = useShortcutLabel();

  return (
    <header className="ohs-header">
      {menu ? (
        <button
          type="button"
          className="ohs-icon-btn ohs-menu-btn"
          aria-label={menu.open ? "Close the menu" : "Open the menu"}
          aria-expanded={menu.open}
          aria-controls={menu.controls}
          onClick={menu.toggle}
        >
          {menu.open ? <X aria-hidden="true" /> : <Menu aria-hidden="true" />}
        </button>
      ) : null}
      <Link href={docsRoute || "/"} className="ohs-brand-link" aria-label="Open Health Sync documentation">
        <Logo />
      </Link>
      {latestVersion ? (
        <a href={`${repoUrl}/releases/tag/v${latestVersion}`} className="ohs-version" title="Release notes on GitHub">
          v{latestVersion}
        </a>
      ) : null}
      <button type="button" className="ohs-search-btn" onClick={() => setOpenSearch(true)}>
        <Search aria-hidden="true" />
        <span>Search docs and API</span>
        <kbd>{shortcut}</kbd>
      </button>
      <nav className="ohs-header-nav" aria-label="Site">
        {headerLinks.map((link) => (
          <a key={link.id} href={link.href} className="ohs-header-link">
            {link.label}
          </a>
        ))}
      </nav>
      <button
        type="button"
        className="ohs-icon-btn ohs-search-icon"
        aria-label="Search docs and API"
        onClick={() => setOpenSearch(true)}
      >
        <Search aria-hidden="true" />
      </button>
      <a href={signupHref} className="ohs-btn ohs-btn-primary ohs-signup">
        Sign up
      </a>
    </header>
  );
}
