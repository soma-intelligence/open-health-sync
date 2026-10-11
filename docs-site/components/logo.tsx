/** The wordmark of docs/assets/wordmark.svg, drawn in the current text colour. */
export function Logo() {
  return (
    <span className="inline-flex items-center gap-2 font-semibold tracking-tight">
      <svg viewBox="0 0 100 96" className="size-6" aria-hidden="true">
        <g fill="none" strokeWidth="8" strokeLinecap="round" strokeLinejoin="round">
          <path d="M10 26 C30 26 34 48 50 48" stroke="currentColor" />
          <path d="M10 70 C30 70 34 48 50 48" stroke="currentColor" />
          <path d="M50 48 H60 L66 32 L74 64 L80 48 H90" stroke="var(--color-fd-primary)" />
        </g>
      </svg>
      Open Health Sync
    </span>
  );
}
