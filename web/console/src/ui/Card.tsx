import type { ReactNode } from "react";

export interface CardProps {
  title?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  /** The heading level: h2 under a page's h1, h3 for a card nested under another heading. */
  level?: 2 | 3;
}

/** A card: no border, radius 18, its title at 17/600. */
export function Card({ title, actions, children, className, level = 2 }: CardProps) {
  const Heading = level === 3 ? "h3" : "h2";
  return (
    <section className={className === undefined ? "card" : `card ${className}`}>
      {title ? (
        <header className="card-h">
          <Heading>{title}</Heading>
          {actions ? <div className="card-r">{actions}</div> : null}
        </header>
      ) : null}
      <div className="card-b">{children}</div>
    </section>
  );
}
