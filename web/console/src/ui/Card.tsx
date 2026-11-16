import type { ReactNode } from "react";

export function Card({ title, actions, children }: { title?: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      {title ? (
        <header className="card-h">
          <h3>{title}</h3>
          {actions ? <div className="card-r">{actions}</div> : null}
        </header>
      ) : null}
      <div className="card-b">{children}</div>
    </section>
  );
}
