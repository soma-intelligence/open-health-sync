import { Button } from "../../ui/Button";

/** The names saved that a running process picks up only when it next starts. There is no restart button. */
export function RestartNotice({ names, onDismiss }: { names: readonly string[]; onDismiss: () => void }) {
  return (
    <div className="notice warn set-restart" role="status" aria-label="Restart needed">
      <p>
        Saved. These settings need a restart:{" "}
        {names.map((n, i) => (
          <span key={n}>
            {i > 0 ? ", " : null}
            <code className="mono">{n}</code>
          </span>
        ))}
        .
      </p>
      <p>They apply when each process next starts. Restart with your own tooling (Docker, systemd, Kubernetes).</p>
      <Button size="sm" kind="ghost" onClick={onDismiss}>
        Dismiss
      </Button>
    </div>
  );
}
