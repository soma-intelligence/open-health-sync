import { Link } from "react-router";

import type { Pipeline } from "../../api/types";
import type { Tone } from "../../lib/classify";
import { formatCount } from "../../lib/format";
import { Card } from "../../ui/Card";
import { EmptyState } from "../../ui/EmptyState";
import { Pill } from "../../ui/Pill";

type Source = Pipeline["sources"][number];

/** A source's state in a word: expired tokens and failing pulls first, then off, live or idle. */
export function sourceState(s: Source): { tone: Tone; text: string } {
  const reauth = s.connections["needs_reauth"] ?? 0;
  const active = s.connections["active"] ?? 0;
  if (reauth > 0) return { tone: "bad", text: `${formatCount(reauth)} expired` };
  if (s.broken > 0) return { tone: "warn", text: `${formatCount(s.broken)} failing` };
  if (s.paused_until) return { tone: "warn", text: "Paused" };
  if (!s.enabled) return { tone: "off", text: "Off" };
  if (active > 0) return { tone: "ok", text: "Live" };
  return { tone: "off", text: "No connection" };
}

/** Sources on this tenant: the ones turned on, and any still holding connections. */
export function SourcesCard({ sources }: { sources: readonly Source[] }) {
  const shown = sources.filter((s) => s.enabled || Object.values(s.connections).some((n) => n > 0));
  return (
    <Card title="Sources" actions={<Link to="/providers">All providers</Link>}>
      {shown.length === 0 ? (
        <EmptyState title="No source on yet">Turn on a provider so users can connect.</EmptyState>
      ) : (
        <div className="table-scroll">
          <table className="data-table sources-table" role="table">
            <thead role="rowgroup">
              <tr role="row">
                <th scope="col">Source</th>
                <th scope="col" className="num">
                  Connections
                </th>
                <th scope="col">State</th>
              </tr>
            </thead>
            <tbody role="rowgroup">
              {shown.map((s) => {
                const state = sourceState(s);
                return (
                  <tr key={s.slug} role="row">
                    <td role="cell">{s.name}</td>
                    <td role="cell" className="num" data-label="Connections">{formatCount(s.connections["active"] ?? 0)}</td>
                    <td role="cell" data-label="State">
                      <Pill tone={state.tone}>{state.text}</Pill>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
