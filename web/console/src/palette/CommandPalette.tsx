import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useNavigate } from "react-router";

import { routerPath } from "../lib/basename";
import { useSettingsQuery } from "../pages/settings/api";
import { useCanWrite, useIsAdmin, useSession } from "../session/SessionProvider";
import { THEME_CHOICES, useTheme } from "../shell/theme";
import { Dialog } from "../ui/Dialog";
import { paletteItems, searchPalette, type PaletteItem } from "./search";

export interface CommandPaletteProps {
  open: boolean;
  onClose: () => void;
}

/** Jump to a page or a setting, or run an action. Hosted by the shell; Ctrl or Meta plus K opens it. */
export function CommandPalette({ open, onClose }: CommandPaletteProps) {
  // Focus goes back to what had it before the palette opened. A layout effect runs before
  // any passive effect, so the opener still has focus when it is read; the dialog closes in
  // its own passive effect, which runs before the one that restores focus.
  const returnTo = useRef<HTMLElement | null>(null);
  useLayoutEffect(() => {
    if (open) returnTo.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  }, [open]);
  const wasOpen = useRef(false);
  useEffect(() => {
    if (wasOpen.current && !open) returnTo.current?.focus();
    wasOpen.current = open;
  }, [open]);

  return (
    <Dialog title="Command palette" open={open} onClose={onClose} className="palette">
      <PaletteBody onClose={onClose} />
    </Dialog>
  );
}

function PaletteBody({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const admin = useIsAdmin();
  const canWrite = useCanWrite();
  const { signOut } = useSession();
  const [theme, setTheme] = useTheme();
  const settings = useSettingsQuery().data?.data;
  const listId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);

  const all = useMemo(() => paletteItems({ admin, canWrite, settings: settings ?? [] }), [admin, canWrite, settings]);
  const found = useMemo(() => searchPalette(all, query), [all, query]);
  const at = Math.min(active, Math.max(found.length - 1, 0));

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const choose = (item: PaletteItem) => {
    onClose();
    if (item.run === "theme") {
      const next = THEME_CHOICES[(THEME_CHOICES.indexOf(theme) + 1) % THEME_CHOICES.length];
      if (next !== undefined) setTheme(next);
    } else if (item.run === "sign-out") {
      void signOut();
    } else if (item.to !== undefined) {
      void navigate(routerPath(item.to), item.open === undefined ? undefined : { state: { open: item.open } });
    }
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = found.length;
      if (n > 0) setActive((at + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
    } else if (e.key === "Escape") {
      // The platform closes a modal on Escape too; this keeps it working where it does not.
      e.preventDefault();
      onClose();
    } else if (e.key === "Enter") {
      e.preventDefault();
      const item = found[at];
      if (item) choose(item);
    }
  };

  const optionId = (i: number) => `${listId}-${String(i)}`;

  return (
    <div className="palette-body">
      <input
        ref={inputRef}
        className="palette-input"
        role="combobox"
        aria-expanded="true"
        aria-controls={listId}
        aria-activedescendant={found.length > 0 ? optionId(at) : undefined}
        aria-label="Search pages, settings and actions"
        placeholder="Search pages, settings and actions"
        autoComplete="off"
        spellCheck={false}
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setActive(0);
        }}
        onKeyDown={onKey}
      />
      <ul id={listId} role="listbox" aria-label="Results" className="palette-list">
        {found.map((item, i) => {
          const heading = found[i - 1]?.group !== item.group;
          return (
            <li key={`${item.group}/${item.title}/${item.to ?? ""}`} role="presentation">
              {heading ? <div className="palette-group">{item.group}</div> : null}
              <div
                id={optionId(i)}
                role="option"
                aria-selected={i === at}
                className={i === at ? "palette-item on" : "palette-item"}
                onMouseMove={() => {
                  if (i !== at) setActive(i);
                }}
                onClick={() => {
                  choose(item);
                }}
              >
                <span className={item.group === "Settings" ? "palette-title mono" : "palette-title"}>{item.title}</span>
                {item.hint !== undefined ? <span className="palette-hint">{item.hint}</span> : null}
              </div>
            </li>
          );
        })}
      </ul>
      {found.length === 0 ? <p className="palette-empty">Nothing matches.</p> : null}
      <footer className="palette-foot">
        <span>↑ ↓ move</span>
        <span>↵ open</span>
        <span>esc close</span>
      </footer>
    </div>
  );
}
