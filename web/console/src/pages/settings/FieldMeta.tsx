import type { Setting } from "../../api/types";

const SOURCE: Record<Setting["source"], string> = { env: "Environment", console: "Console", default: "Default" };
const APPLY: Record<Setting["apply"], string> = { live: "Live", restart: "Restart", bootstrap: "Bootstrap" };

export function SourceBadge({ source }: { source: Setting["source"] }) {
  return <span className={`set-badge set-source ${source}`}>{SOURCE[source]}</span>;
}

export function ApplyBadge({ apply }: { apply: Setting["apply"] }) {
  return <span className={`set-badge set-apply ${apply}`}>{APPLY[apply]}</span>;
}

function bounds(s: Setting): string | null {
  if (s.min !== null && s.max !== null) return `${String(s.min)} to ${String(s.max)}`;
  if (s.min !== null) return `at least ${String(s.min)}`;
  if (s.max !== null) return `at most ${String(s.max)}`;
  return null;
}

/** The variable, where its value comes from, when a change applies, and the default. */
export function FieldMeta({ setting: s, changed }: { setting: Setting; changed: boolean }) {
  const range = s.type === "int" ? bounds(s) : null;
  return (
    <>
      <div className="set-meta">
        <code className="mono">{s.name}</code>
        <SourceBadge source={s.source} />
        <ApplyBadge apply={s.apply} />
        {changed ? <span className="set-badge set-edited">Edited</span> : null}
      </div>
      {s.help ? <p className="set-help">{s.help}</p> : null}
      <p className="set-default">
        {s.default !== "" ? (
          <>
            Default <code className="mono">{s.default}</code>
          </>
        ) : s.default_note ? (
          <>
            Default: <span>{s.default_note}</span>
          </>
        ) : (
          "No default"
        )}
        {range ? (
          <>
            {" "}
            <span className="set-sep">|</span> Range <span className="mono">{range}</span>
          </>
        ) : null}
        {s.type === "list" ? <span className="set-sep"> | comma separated</span> : null}
      </p>
    </>
  );
}
