"use client";
import { useSearchContext } from "fumadocs-ui/contexts/search";
import { Menu, Search, X } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { Logo } from "@/components/logo";
import { consoleUrl, docsHref, docsRoute, repoUrl, signupUrl } from "@/lib/shared";

/** Header links, the same as the portal's and the console's: guides, API, console, source. */
export const headerLinks = [
  { id: "guides", label: "Guides", href: docsHref("/documentation") },
  { id: "api", label: "API", href: docsHref("/documentation/api-reference") },
  { id: "console", label: "Console", href: consoleUrl, external: true },
  { id: "github", label: "GitHub", href: repoUrl, external: true },
] as const;

export const signupHref = `${signupUrl}?utm_source=docs&utm_medium=docs&utm_campaign=header`;

/** Which header link the page is under: the API reference, or the guides around it. */
export function activeLink(pathname: string): "guides" | "api" | undefined {
  const rel = !docsRoute
    ? pathname
    : pathname === docsRoute
      ? "/"
      : pathname.startsWith(`${docsRoute}/`)
        ? pathname.slice(docsRoute.length)
        : undefined;
  if (rel === undefined || /^\/platform(\/|$)/.test(rel)) return undefined;
  return /^\/api-reference(\/|$)/.test(rel) ? "api" : "guides";
}

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
  const pathname = usePathname();
  const active = activeLink(pathname);
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
      <button type="button" className="ohs-search-btn" onClick={() => setOpenSearch(true)}>
        <Search aria-hidden="true" />
        <span>Search docs and API</span>
        <kbd>{shortcut}</kbd>
      </button>
      <nav className="ohs-header-nav" aria-label="Site">
        {headerLinks.map((link) =>
          "external" in link ? (
            <a key={link.id} href={link.href} className="ohs-header-link">
              {link.label}
            </a>
          ) : (
            <Link
              key={link.id}
              href={link.href}
              className="ohs-header-link"
              aria-current={active === link.id ? "page" : undefined}
            >
              {link.label}
            </Link>
          ),
        )}
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
