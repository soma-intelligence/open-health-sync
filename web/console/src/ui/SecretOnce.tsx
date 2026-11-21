import { useId, useState } from "react";

import { Button } from "./Button";
import { CopyButton } from "./CopyButton";
import { Dialog } from "./Dialog";

export interface SecretOnceProps {
  title: string;
  intro: string;
  value: string;
  onDone: () => void;
  /** What the secret is, as the field names it: "API key", "Signing secret". */
  label?: string;
}

/**
 * Shows a secret once, whole, with Copy. It holds nothing itself: the parent keeps the value in state
 * and drops it in onDone. Escape and a click outside do not close it, and Done waits for the operator
 * to say the secret is stored, so a secret is not lost by accident.
 */
export function SecretOnce({ title, intro, value, onDone, label = "Secret" }: SecretOnceProps) {
  const fieldId = useId();
  const [stored, setStored] = useState(false);
  return (
    <Dialog
      title={title}
      open
      onClose={() => undefined}
      footer={
        <Button kind="primary" onClick={onDone} disabled={!stored}>
          Done
        </Button>
      }
    >
      <div className="field">
        <p>{intro}</p>
        <label htmlFor={fieldId}>{label}</label>
        <textarea
          id={fieldId}
          className="input mono secret-value"
          readOnly
          rows={2}
          autoComplete="off"
          spellCheck={false}
          value={value}
          onFocus={(e) => {
            e.currentTarget.select();
          }}
        />
        <div className="secret-row">
          <p className="muted small">It is shown this once and cannot be shown again.</p>
          <CopyButton text={value} />
        </div>
      </div>
      <label className="check secret-ack">
        <input
          type="checkbox"
          checked={stored}
          onChange={(e) => {
            setStored(e.target.checked);
          }}
        />
        I have stored it somewhere safe
      </label>
    </Dialog>
  );
}
