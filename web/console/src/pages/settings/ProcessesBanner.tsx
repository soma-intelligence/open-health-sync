import type { ProcessHeartbeat } from "../../api/types";

/** The processes that run on older settings: behind (restart) or stale (live). None: nothing. */
export function ProcessesBanner({ processes }: { processes: readonly ProcessHeartbeat[] }) {
  const late = processes.filter((p) => p.behind || p.stale);
  if (late.length === 0) return null;
  return (
    <section className="notice warn set-banner" aria-labelledby="set-banner-h">
      <h2 id="set-banner-h" className="set-banner-h">
        Processes not on the current settings
      </h2>
      <p className="muted small">
        Behind: started before the last change to a restart setting. Stale: has not picked up the last live change for over a minute.
      </p>
      <table className="set-table">
        <thead>
          <tr>
            <th scope="col">Host</th>
            <th scope="col">Roles</th>
            <th scope="col">Version</th>
            <th scope="col">State</th>
            <th scope="col">Skipped</th>
            <th scope="col">Reload error</th>
          </tr>
        </thead>
        <tbody>
          {late.map((p) => (
            <tr key={p.id}>
              <td className="mono">{p.host}</td>
              <td>{p.roles.join(", ")}</td>
              <td className="mono">{p.version}</td>
              <td>
                {p.behind ? <span className="set-badge set-apply restart">behind</span> : null}
                {p.stale ? <span className="set-badge set-apply restart">stale</span> : null}
              </td>
              <td>
                {p.skipped.map((n, i) => (
                  <span key={n}>
                    {i > 0 ? ", " : null}
                    <code className="mono">{n}</code>
                  </span>
                ))}
              </td>
              <td>{p.reload_error ?? ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
