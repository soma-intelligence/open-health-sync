import { useEffect, useId, useRef, useState, type ReactNode } from "react";

import { Button } from "./Button";

export interface DialogProps {
  title: string;
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
  /** Something was typed or picked: Escape and a click outside ask before they drop it. */
  dirty?: boolean;
  /** A request is in flight: Escape and a click outside do nothing, so its answer is never lost. */
  busy?: boolean;
}

/**
 * A modal over a native <dialog>: focus trap, Escape and the backdrop come from the platform.
 * Closing gives the focus back to what had it before the dialog opened.
 */
export function Dialog({ title, open, onClose, children, footer, className, dirty = false, busy = false }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const [askingRaw, setAsking] = useState(false);
  // Nothing typed any more, or closed: no question left to ask.
  const asking = askingRaw && open && dirty;

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) {
      // jsdom has no showModal.
      if (typeof (el as Partial<HTMLDialogElement>).showModal === "function") el.showModal();
      else el.setAttribute("open", "");
    }
    if (!open && el.open) {
      if (typeof (el as Partial<HTMLDialogElement>).close === "function") el.close();
      else el.removeAttribute("open");
    }
  }, [open]);

  // The opener gets the focus back when the dialog closes or goes away.
  // What had the focus when the dialog first rendered: an autoFocus field inside has taken it by the time effects run.
  const [opener] = useState<Element | null>(() => document.activeElement);
  useEffect(() => {
    if (!open) return;
    const active = document.activeElement;
    const before = active !== null && ref.current?.contains(active) ? opener : active;
    return () => {
      if (before instanceof HTMLElement && before.isConnected) before.focus();
    };
  }, [open, opener]);

  // Escape and a click outside: a request in flight keeps the dialog, typed input asks first.
  const dismiss = () => {
    if (busy) return;
    if (dirty) {
      setAsking(true);
      return;
    }
    onClose();
  };

  return (
    <dialog
      ref={ref}
      className={className === undefined ? "dialog" : `dialog ${className}`}
      aria-labelledby={titleId}
      aria-busy={busy || undefined}
      onCancel={(e) => {
        e.preventDefault();
        dismiss();
      }}
      onClick={(e) => {
        if (e.target === ref.current) dismiss();
      }}
    >
      {open ? (
        <div className="dialog-body">
          <h2 id={titleId}>{title}</h2>
          <div className="dialog-content">{children}</div>
          {asking ? (
            <div className="dialog-discard" role="alert">
              <p>Close and lose what you entered?</p>
              <Button
                size="sm"
                autoFocus
                onClick={() => {
                  setAsking(false);
                }}
              >
                Keep editing
              </Button>
              <Button size="sm" kind="danger" onClick={onClose}>
                Discard
              </Button>
            </div>
          ) : null}
          {footer ? <footer className="dialog-footer">{footer}</footer> : null}
        </div>
      ) : null}
    </dialog>
  );
}
