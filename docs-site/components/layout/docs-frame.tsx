"use client";
import { usePathname } from "next/navigation";
import { type ReactNode, useEffect, useId, useState } from "react";
import type { NavRoot } from "@/lib/nav-model";
import { siteUrl } from "@/lib/shared";
import { Header, headerLinks, signupHref } from "./header";
import { Sidebar } from "./sidebar";

/**
 * Header, sidebar and page. From 1024 px the sidebar sits beside the page; under it, the header's
 * menu button opens it as a drawer, with the header links that no longer fit.
 */
export function DocsFrame({ nav, children }: { nav: NavRoot[]; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const drawerId = useId();
  const pathname = usePathname();

  // biome-ignore lint/correctness/useExhaustiveDependencies: a new page closes the drawer
  useEffect(() => setOpen(false), [pathname]);

  useEffect(() => {
    if (!open) return;
    const { overflow } = document.body.style;
    document.body.style.overflow = "hidden";
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("keydown", onKey);
    const close = () => window.innerWidth >= 1024 && setOpen(false);
    window.addEventListener("resize", close);
    return () => {
      document.body.style.overflow = overflow;
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("resize", close);
    };
  }, [open]);

  return (
    <div className="ohs-docs">
      <Header menu={{ open, controls: drawerId, toggle: () => setOpen(!open) }} />
      <div className="ohs-docs-body">
        <aside className="ohs-sidebar">
          <Sidebar nav={nav} />
          <SidebarFoot />
        </aside>
        <div className="ohs-docs-main">{children}</div>
      </div>
      {open ? (
        // biome-ignore lint/a11y/noStaticElementInteractions: a click on the scrim closes, Escape does it from the keyboard
        <div className="ohs-drawer-scrim" onMouseDown={(e) => e.target === e.currentTarget && setOpen(false)}>
          <div className="ohs-drawer" id={drawerId} role="dialog" aria-modal="true" aria-label="Menu">
            <nav className="ohs-drawer-links" aria-label="Site">
              {headerLinks.map((link) => (
                <a key={link.id} href={link.href} className="ohs-drawer-link">
                  {link.label}
                </a>
              ))}
            </nav>
            <a href={signupHref} className="ohs-btn ohs-btn-primary ohs-btn-block">
              Sign up
            </a>
            <Sidebar nav={nav} onNavigate={() => setOpen(false)} />
            <SidebarFoot />
          </div>
        </div>
      ) : null}
    </div>
  );
}

function SidebarFoot() {
  return (
    <p className="ohs-sidebar-foot">
      <a href={siteUrl}>openhealthsync.com</a>
    </p>
  );
}
