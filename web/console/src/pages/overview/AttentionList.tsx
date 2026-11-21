import { Link } from "react-router";

import { routerPath } from "../../lib/basename";
import type { AttentionItem } from "../../lib/pipeline";
import { Card } from "../../ui/Card";
import { EmptyState } from "../../ui/EmptyState";
import { Pill } from "../../ui/Pill";

/**
 * Needs you: each problem the operator can act on, with its chip and one button to the page that
 * fixes it. setupLeft: the getting started steps are not all done, so an empty list says so.
 */
export function AttentionList({ items, setupLeft = false }: { items: AttentionItem[]; setupLeft?: boolean }) {
  return (
    <Card title="Needs you" actions={items.length > 0 ? <Pill tone="off">{String(items.length)}</Pill> : undefined}>
      {items.length === 0 ? (
        setupLeft ? (
          <EmptyState title="Nothing to fix.">Finish setting up with the steps under Get started.</EmptyState>
        ) : (
          <EmptyState title="Nothing needs you.">Every stage is running as it should.</EmptyState>
        )
      ) : (
        <ul className="todo">
          {items.map((item) => (
            <li key={`${item.chip} ${item.text}`} className="todo-item">
              <Pill tone={item.tone}>{item.chip}</Pill>
              <span className="todo-text">
                {item.text}
                {item.detail ? <small>{item.detail}</small> : null}
              </span>
              {item.to && item.action ? (
                <Link className="btn sm" to={routerPath(item.to)} aria-label={`${item.action}: ${item.text}`}>
                  {item.action}
                </Link>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
