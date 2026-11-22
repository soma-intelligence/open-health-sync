import type { ReactNode } from "react";

// Stroke icons on a 24 px grid, drawn from the mockup's symbols.
const PATHS = {
  home: <path d="M3 12h4l3-8 4 16 3-8h4" />,
  grid: (
    <>
      <rect x="3.5" y="3.5" width="7" height="7" rx="2" />
      <rect x="13.5" y="3.5" width="7" height="7" rx="2" />
      <rect x="3.5" y="13.5" width="7" height="7" rx="2" />
      <rect x="13.5" y="13.5" width="7" height="7" rx="2" />
    </>
  ),
  users: (
    <>
      <circle cx="9" cy="8" r="3.5" />
      <path d="M2.5 20c.8-3.6 3.4-5.5 6.5-5.5s5.7 1.9 6.5 5.5" />
      <path d="M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14.8c1.9.7 3.1 2.4 3.5 5.2" />
    </>
  ),
  plug: <path d="M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0V8zM12 17v4" />,
  sliders: (
    <>
      <path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0" />
      <circle cx="16" cy="6" r="2" />
      <circle cx="10" cy="12" r="2" />
      <circle cx="18" cy="18" r="2" />
    </>
  ),
  hook: (
    <>
      <path d="M18 16.5a3.5 3.5 0 1 1-3.3 4.6H9" />
      <path d="M8.5 9.4A3.5 3.5 0 1 1 12 4c1.9 0 3.5 1.6 3.5 3.5 0 .9-.3 1.7-.9 2.3L11 16" />
      <path d="M6 21a3.5 3.5 0 0 1-2-6.4L7.3 9" />
    </>
  ),
  key: (
    <>
      <circle cx="8" cy="15" r="4" />
      <path d="M11 12l9-9M17 6l3 3M14 9l2 2" />
    </>
  ),
  list: <path d="M9 6h11M9 12h11M9 18h11M4.5 6h0M4.5 12h0M4.5 18h0" />,
  cpu: (
    <>
      <rect x="6" y="6" width="12" height="12" rx="2" />
      <path d="M9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4" />
    </>
  ),
  search: (
    <>
      <circle cx="11" cy="11" r="6.5" />
      <path d="M20 20l-4.2-4.2" />
    </>
  ),
  panel: (
    <>
      <rect x="3" y="4" width="18" height="16" rx="3" />
      <path d="M15 4v16" />
    </>
  ),
  menu: <path d="M4 7h16M4 12h16M4 17h16" />,
  x: <path d="M6 6l12 12M18 6L6 18" />,
  check: <path d="M5 12.5l4.5 4.5L19 7.5" />,
  alert: (
    <>
      <path d="M12 4l9 16H3z" />
      <path d="M12 10v4M12 17h0" />
    </>
  ),
  copy: (
    <>
      <rect x="8" y="8" width="12" height="12" rx="2.5" />
      <path d="M16 8V6.5A2.5 2.5 0 0 0 13.5 4h-7A2.5 2.5 0 0 0 4 6.5v7A2.5 2.5 0 0 0 6.5 16H8" />
    </>
  ),
  lock: (
    <>
      <rect x="5" y="11" width="14" height="9" rx="2" />
      <path d="M8 11V8a4 4 0 0 1 8 0v3" />
    </>
  ),
  refresh: <path d="M20 12a8 8 0 1 1-2.3-5.7M20 4v5h-5" />,
  chevron: <path d="M9 6l6 6-6 6" />,
  out: <path d="M10 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h4M15 8l4 4-4 4M19 12H9" />,
} satisfies Record<string, ReactNode>;

export type IconName = keyof typeof PATHS;

export function Icon({ name, size }: { name: IconName; size?: "sm" }) {
  return (
    <svg className={size === "sm" ? "icon icon-sm" : "icon"} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      {PATHS[name]}
    </svg>
  );
}

/** The product mark: a pulse line on the OHS green square. */
export function BrandMark() {
  return (
    <svg className="brand-mark" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
      <rect className="brand-mark-bg" width="32" height="32" rx="9" />
      <path
        className="brand-mark-line"
        d="M5 16.5h5.5l2.5-6 5 12 2.5-6H27"
        fill="none"
        strokeWidth="2.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
