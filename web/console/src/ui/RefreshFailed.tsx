import { errorLine } from "../lib/errorText";
import { Button } from "./Button";
import { Icon } from "./Icon";

/**
 * A refresh that failed while the page still shows what it loaded before:
 * says so, and how old that is, instead of letting old figures pass as live.
 */
export function RefreshFailed({ error, updatedAt, onRetry, busy }: { error: unknown; updatedAt: number; onRetry: () => void; busy: boolean }) {
  const when = updatedAt > 0 ? new Date(updatedAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : null;
  return (
    <div className="notice-bar" role="status">
      <Icon name="alert" size="sm" />
      <p>
        {`Could not refresh: ${errorLine(error, "the request failed.")}`}
        {when ? ` What you see is from ${when}.` : null}
      </p>
      <Button size="sm" onClick={onRetry} disabled={busy} aria-busy={busy}>
        {busy ? "Trying again" : "Try again"}
      </Button>
    </div>
  );
}
