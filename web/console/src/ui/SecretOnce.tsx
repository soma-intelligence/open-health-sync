import { Button } from "./Button";
import { CopyButton } from "./CopyButton";
import { Dialog } from "./Dialog";

export interface SecretOnceProps {
  title: string;
  intro: string;
  value: string;
  onDone: () => void;
}

/**
 * Shows a secret once, with Copy. It holds nothing itself: the parent keeps the value in state and
 * drops it in onDone. Escape and a click outside do not close it, so a secret is not lost by accident.
 */
export function SecretOnce({ title, intro, value, onDone }: SecretOnceProps) {
  return (
    <Dialog
      title={title}
      open
      onClose={() => undefined}
      footer={
        <Button kind="primary" onClick={onDone}>
          Done
        </Button>
      }
    >
      <div className="field">
        <p>{intro}</p>
        <label htmlFor="secret-once">Secret</label>
        <div className="secret-row">
          <input
            id="secret-once"
            className="input mono"
            readOnly
            autoComplete="off"
            spellCheck={false}
            value={value}
            onFocus={(e) => {
              e.currentTarget.select();
            }}
          />
          <CopyButton text={value} />
        </div>
        <p className="muted">It is shown this once. Store it now: it cannot be shown again.</p>
      </div>
    </Dialog>
  );
}
