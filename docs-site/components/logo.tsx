/** The mark the console and the portal show: a pulse line on the brand green. */
export function BrandMark({ className = "ohs-mark" }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 32 32" aria-hidden="true" focusable="false">
      <rect width="32" height="32" rx="9" fill="var(--op-brand)" />
      <path
        d="M5 16.5h5.5l2.5-6 5 12 2.5-6H27"
        fill="none"
        stroke="var(--op-accent-fg)"
        strokeWidth="2.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

/** The mark, the name and a "Docs" chip, as in the header of the portal and the console. */
export function Logo({ chip = true }: { chip?: boolean }) {
  return (
    <span className="ohs-brand">
      <BrandMark />
      <span className="ohs-brand-name">Open Health Sync</span>
      {chip ? <span className="ohs-brand-chip">Docs</span> : null}
    </span>
  );
}
