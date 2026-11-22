import { Link } from "react-router";

import { routerPath } from "../../lib/basename";
import type { ChecklistItem } from "../../lib/pipeline";
import { Card } from "../../ui/Card";
import { Icon } from "../../ui/Icon";

/** The words of the one button, on the next step to do. */
const ACTIONS: Record<ChecklistItem["id"], string> = {
  provider: "Open providers",
  user: "Add a user",
  connection: "Open users",
  data: "Open users",
  webhook: "Add an endpoint",
};

/**
 * The five steps to a working install, one line each: done steps greyed with a check, the next step
 * with its button. The page hides the card once every step is done.
 */
export function GettingStarted({ items }: { items: ChecklistItem[] }) {
  const done = items.filter((i) => i.done).length;
  const next = items.find((i) => !i.done);
  return (
    <Card title="Get started" actions={<span className="muted">{`${String(done)} of ${String(items.length)} done`}</span>}>
      <ol className="steps">
        {items.map((item) => {
          const isNext = item === next;
          return (
            <li key={item.id} className={item.done ? "step done" : isNext ? "step next" : "step"} aria-current={isNext ? "step" : undefined}>
              <span className="step-mark" aria-hidden="true">
                {item.done ? <Icon name="check" size="sm" /> : null}
              </span>
              <span className="step-title">
                {item.title}
                {item.done ? <span className="sr-only"> (done)</span> : null}
              </span>
              {isNext ? (
                <Link className="btn sm primary" to={routerPath(item.to)} aria-label={`${ACTIONS[item.id]}: ${item.title}`}>
                  {ACTIONS[item.id]}
                </Link>
              ) : null}
            </li>
          );
        })}
      </ol>
    </Card>
  );
}
