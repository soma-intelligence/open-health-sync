import { useEffect, useId, useRef, type ReactNode } from "react";

export interface DialogProps {
  title: string;
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
}

/** A modal over a native <dialog>: focus trap, Escape and the backdrop come from the platform. */
export function Dialog({ title, open, onClose, children, footer, className }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();

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

  return (
    <dialog
      ref={ref}
      className={className === undefined ? "dialog" : `dialog ${className}`}
      aria-labelledby={titleId}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        if (e.target === ref.current) onClose();
      }}
    >
      {open ? (
        <div className="dialog-body">
          <h2 id={titleId}>{title}</h2>
          <div className="dialog-content">{children}</div>
          {footer ? <footer className="dialog-footer">{footer}</footer> : null}
        </div>
      ) : null}
    </dialog>
  );
}
