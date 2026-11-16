import { useEffect, useState } from "react";
import { Outlet, useLocation } from "react-router";

import { readPref, writePref } from "../lib/prefs";
import { useSession } from "../session/SessionProvider";
import { ToastProvider } from "../ui/Toast";
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
  const [inspectorOpen, setInspectorOpen] = useState(() => readPref("inspector") !== "closed");

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

  const toggleInspector = () => {
    const next = !inspectorOpen;
    setInspectorOpen(next);
    writePref("inspector", next ? "open" : "closed");
  };

  return (
    <ToastProvider>
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
          {/* The inspector pane fills this slot (Task 4). */}
          {inspectorOpen ? <aside id="inspector-slot" className="insp" aria-label="Inspector" /> : null}
        </div>
      </div>
    </ToastProvider>
  );
}
