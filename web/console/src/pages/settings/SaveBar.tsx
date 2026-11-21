import { Button } from "../../ui/Button";

export interface SaveBarProps {
  count: number;
  restarts: number;
  blocked: boolean;
  onDiscard: () => void;
  onReview: () => void;
}

/** What the draft holds, Discard and Review. Rendered only for a session that can write, with a draft. */
export function SaveBar({ count, restarts, blocked, onDiscard, onReview }: SaveBarProps) {
  return (
    <section className="set-savebar" aria-label="Unsaved changes">
      <p>
        <strong>{count === 1 ? "1 unsaved change" : `${String(count)} unsaved changes`}</strong>
        {restarts > 0 ? <span className="set-badge set-apply restart">{restarts === 1 ? "1 needs a restart" : `${String(restarts)} need a restart`}</span> : null}
        {blocked ? <span className="set-error">Fix the marked fields first.</span> : null}
      </p>
      <div className="set-savebar-r">
        <Button kind="ghost" onClick={onDiscard}>
          Discard
        </Button>
        <Button kind="primary" disabled={blocked} onClick={onReview}>
          Review
        </Button>
      </div>
    </section>
  );
}
