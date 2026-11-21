import { useEffect, useState } from "react";
import { Outlet, useLocation } from "react-router";

import { InspectorPane } from "../inspector/InspectorPane";
import { InspectorProvider } from "../inspector/InspectorProvider";
import { useSession } from "../session/SessionProvider";
import { ToastProvider } from "../ui/Toast";
import { CommandPalette } from "../palette/CommandPalette";
import { NavPane } from "./NavPane";
import { OfflineNotice } from "./OfflineNotice";
import { PageBoundary } from "./PageBoundary";
import { TopBar } from "./TopBar";
import { useMediaQuery } from "./useMediaQuery";

export const NARROW_QUERY = "(max-width: 1023px)";
const NAV_ID = "console-nav";

/** The pages whose inspector opens by default: the Overview and a user's page. Elsewhere it starts closed. */
export function inspectorOpensOn(pathname: string): boolean {
  return pathname === "/overview" || /^\/users\/[^/]+/.test(pathname);
}

/** Top bar, navigation, the page and the inspector slot. Wraps every signed in page. */
export function AppShell() {
  const narrow = useMediaQuery(NARROW_QUERY);
  const { pathname } = useLocation();
  const { session } = useSession();
  // The menu is open for the page it was opened on: moving to another page closes it.
  const [menuAt, setMenuAt] = useState<string | null>(null);
  const menuOpen = narrow && menuAt === pathname;
  // Wide screens: open on the Overview and a user's page, closed elsewhere, until the operator
  // toggles it; that choice then holds on every page for the rest of the session. Under 1024 px
  // the pane is an overlay: it starts closed on every visit and never changes the wide choice.
  const [wideChoice, setWideChoice] = useState<boolean | null>(null);
  const wideOpen = wideChoice ?? inspectorOpensOn(pathname);
  const [narrowOpen, setNarrowOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const inspectorOpen = narrow ? narrowOpen : wideOpen;

  useEffect(() => {
    if (!menuOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMenuAt(null);
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
    };
  }, [menuOpen]);

  useEffect(() => {
    if (!narrow || !narrowOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setNarrowOpen(false);
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
    };
  }, [narrow, narrowOpen]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "k") {
        // Over another open dialog (a one-time secret, a confirmation) the palette stays shut.
        if (document.querySelector("dialog[open]") !== null) return;
        e.preventDefault();
        setPaletteOpen(true);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
    };
  }, []);

  const toggleInspector = () => {
    const next = !inspectorOpen;
    if (narrow) {
      setNarrowOpen(next);
      return;
    }
    setWideChoice(next);
  };

  return (
    <ToastProvider>
      <InspectorProvider>
        <div className="app">
          <div className={inspectorOpen ? "shell" : "shell no-inspector"}>
            {!narrow || menuOpen ? <NavPane id={NAV_ID} /> : null}
            {menuOpen ? (
              <button
                type="button"
                className="scrim"
                aria-label="Close navigation"
                onClick={() => {
                  setMenuAt(null);
                }}
              />
            ) : null}
            <div className="shell-col">
              <TopBar
                narrow={narrow}
                menuOpen={menuOpen}
                navId={NAV_ID}
                onMenu={() => {
                  setMenuAt(menuOpen ? null : pathname);
                }}
                inspectorOpen={inspectorOpen}
                onInspector={toggleInspector}
                onSearch={() => {
                  setPaletteOpen(true);
                }}
              />
              {/* Another acting tenant remounts the page, so none of it keeps the old tenant's data. */}
              <main key={session?.acting_tenant.id ?? ""} className="shell-main">
                <OfflineNotice />
                <PageBoundary>
                  <Outlet />
                </PageBoundary>
              </main>
            </div>
            {inspectorOpen ? (
              <aside className="insp" aria-label="Inspector">
                <InspectorPane onClose={narrow ? toggleInspector : undefined} />
              </aside>
            ) : null}
          </div>
          <CommandPalette
            open={paletteOpen}
            onClose={() => {
              setPaletteOpen(false);
            }}
          />
        </div>
      </InspectorProvider>
    </ToastProvider>
  );
}
