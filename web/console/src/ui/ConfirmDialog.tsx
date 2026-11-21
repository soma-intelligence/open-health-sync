import { useId, useState, type ReactNode } from "react";

import { ApiError } from "../api/client";
import { confirmMatches } from "../lib/confirm";
import { Button } from "./Button";
import { Dialog } from "./Dialog";
import { ErrorPanel } from "./ErrorPanel";

export interface ConfirmDialogProps {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  /** The text the operator has to type before the button works. */
  typed?: string;
  /** The write. A rejection is shown in the dialog, which stays open; a result closes it. */
  onConfirm: () => Promise<unknown>;
  onClose: () => void;
  /** The body holds input the operator typed: Escape and a click outside ask before dropping it. */
  dirty?: boolean;
}

/** Every write goes through one of these: it says what happens, and names what goes when it is a delete. */
export function ConfirmDialog({ title, body, confirmLabel, danger, typed, onConfirm, onClose, dirty = false }: ConfirmDialogProps) {
  const formId = useId();
  const inputId = useId();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const ready = typed === undefined || confirmMatches(text, typed);

  const submit = async () => {
    if (!ready || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
      onClose();
    } catch (e) {
      setError(e instanceof ApiError || e instanceof Error ? e : new Error("Something went wrong."));
      setBusy(false);
    }
  };

  return (
    <Dialog
      title={title}
      open
      onClose={onClose}
      busy={busy}
      dirty={dirty || text !== ""}
      footer={
        <>
          {/* While the write is in flight the dialog stays: its answer, or its error, needs a place to land. */}
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form={formId} kind={danger ? "danger" : "primary"} disabled={!ready || busy} aria-busy={busy}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="confirm-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <div className="confirm-body">{body}</div>
        {typed === undefined ? null : (
          <div className="field">
            <label htmlFor={inputId}>
              Type <span className="mono">{typed}</span> to confirm
            </label>
            <input
              id={inputId}
              className="input mono"
              autoComplete="off"
              spellCheck={false}
              value={text}
              onChange={(e) => {
                setText(e.target.value);
              }}
            />
          </div>
        )}
        {error === null ? null : <ErrorPanel error={error} />}
      </form>
    </Dialog>
  );
}
