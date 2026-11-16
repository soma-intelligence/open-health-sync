import { BrandMark, Icon } from "../ui/Icon";

export interface TopBarProps {
  /** Under 1024 px: the button that opens the navigation. */
  narrow: boolean;
  menuOpen: boolean;
  navId: string;
  onMenu: () => void;
  inspectorOpen: boolean;
  onInspector: () => void;
  /** The jump to search; wired by the command palette. */
  onSearch?: () => void;
}

export function TopBar({ narrow, menuOpen, navId, onMenu, inspectorOpen, onInspector, onSearch }: TopBarProps) {
  return (
    <header className="top">
      {narrow ? (
        <button type="button" className="icon-btn" aria-label="Menu" aria-expanded={menuOpen} aria-controls={navId} onClick={onMenu}>
          <Icon name="menu" />
        </button>
      ) : null}
      <span className="brand">
        <BrandMark />
        <span className="brand-name">Open Health Sync</span>
      </span>
      <button type="button" className="search" onClick={onSearch}>
        <Icon name="search" />
        <span>Search or jump to</span>
        <kbd>⌘K</kbd>
      </button>
      <button
        type="button"
        className={inspectorOpen ? "icon-btn on" : "icon-btn"}
        aria-label="Inspector"
        aria-pressed={inspectorOpen}
        onClick={onInspector}
      >
        <Icon name="panel" />
      </button>
    </header>
  );
}
