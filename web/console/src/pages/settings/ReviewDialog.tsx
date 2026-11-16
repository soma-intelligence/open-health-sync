import { Button } from "../../ui/Button";
import { Dialog } from "../../ui/Dialog";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { ApplyBadge } from "./FieldMeta";
import { restartCount, type DiffRow } from "./model";

export interface ReviewDialogProps {
  rows: readonly DiffRow[];
  /** Said above the rows, after a reload. */
  note: string | null;
  error: unknown;
  busy: boolean;
  onApply: () => void;
  onClose: () => void;
}

/** The diff the PATCH will make. A secret shows as set or not set, then new value: never the typed text. */
export function ReviewDialog({ rows, note, error, busy, onApply, onClose }: ReviewDialogProps) {
  const restarts = restartCount(rows);
  return (
    <Dialog
      title="Review changes"
      open
      onClose={onClose}
      footer={
        <>
          <Button kind="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button kind="primary" disabled={busy || rows.length === 0} onClick={onApply}>
            Apply
          </Button>
        </>
      }
    >
      {note ? <p className="notice">{note}</p> : null}
      {rows.length === 0 ? (
        <p className="muted">Nothing left to apply.</p>
      ) : (
        <table className="set-table set-diff">
          <thead>
            <tr>
              <th scope="col">Setting</th>
              <th scope="col">Now</th>
              <th scope="col">After</th>
              <th scope="col">Applies</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.name}>
                <td>
                  <span>{r.label}</span> <code className="mono small">{r.name}</code>
                </td>
                <td className="mono">{r.before}</td>
                <td className="mono">{r.after}</td>
                <td>
                  <ApplyBadge apply={r.apply} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {restarts > 0 ? (
        <p className="muted small">
          {restarts === 1 ? "1 change applies" : `${String(restarts)} changes apply`} when each process next starts. Nothing is restarted
          for you.
        </p>
      ) : null}
      {error ? <ErrorPanel error={error} /> : null}
    </Dialog>
  );
}
