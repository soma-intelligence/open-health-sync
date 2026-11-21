import { Link } from "react-router";

import { routerPath } from "../../lib/basename";
import type { ChecklistItem } from "../../lib/pipeline";
import { Card } from "../../ui/Card";
import { Icon } from "../../ui/Icon";

/** The five steps to a working install; the page hides it once every step is done. */
export function GettingStarted({ items }: { items: ChecklistItem[] }) {
  const done = items.filter((i) => i.done).length;
  return (
    <Card title="Get started" actions={<span className="muted">{`${String(done)} of ${String(items.length)} done`}</span>}>
      <ol className="steps">
        {items.map((item) => (
          <li key={item.id} className={item.done ? "step done" : "step"}>
            <span className="step-mark" aria-hidden="true">
              {item.done ? <Icon name="check" size="sm" /> : null}
            </span>
            <Link to={routerPath(item.to)}>{item.title}</Link>
            {item.done ? <span className="sr-only"> (done)</span> : null}
          </li>
        ))}
      </ol>
    </Card>
  );
}
