import { useEffect, useState } from "react";
import { Outlet, useLocation } from "react-router";

import { readPref, writePref } from "../lib/prefs";
import { InspectorPane } from "../inspector/InspectorPane";
import { InspectorProvider } from "../inspector/InspectorProvider";
import { useSession } from "../session/SessionProvider";
import { ToastProvider } from "../ui/Toast";
import { CommandPalette } from "../palette/CommandPalette";
import { NavPane } from "./NavPane";
import { PageBoundary } from "./PageBoundary";
import { TopBar } from "./TopBar";
import { useMediaQuery } from "./useMediaQuery";

export const NARROW_QUERY = "(max-width: 1023px)";
const NAV_ID = "console-nav";

/** Top bar, navigation, the page and the inspector slot. Wraps every signed in page. */
export function AppShell() {
  const narrow = useMediaQuery(NARROW_QUERY);
  const { pathname } = useLocation();
  const { session } = useSession();
  // The menu is open for the page it was opened on: moving to another page closes it.
  const [menuAt, setMenuAt] = useState<string | null>(null);
  const menuOpen = narrow && menuAt === pathname;
  // Wide screens keep the stored choice. Under 1024 px the pane is an overlay: it starts
  // closed on every visit and its state is not stored, so it never changes the wide choice.
  const [wideOpen, setWideOpen] = useState(() => readPref("inspector") !== "closed");
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
    setWideOpen(next);
    writePref("inspector", next ? "open" : "closed");
  };

  return (
    <ToastProvider>
      <InspectorProvider>
        <div className="app">
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
            {/* Another acting tenant remounts the page, so none of it keeps the old tenant's data. */}
            <main key={session?.acting_tenant.id ?? ""} className="shell-main">
              <PageBoundary>
                <Outlet />
              </PageBoundary>
            </main>
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
