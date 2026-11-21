import type { ReactNode } from "react";

export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <h4>{title}</h4>
      {children ? <p>{children}</p> : null}
    </div>
  );
}
