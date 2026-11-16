import { Link } from "react-router";

import { routerPath } from "../../lib/basename";
import type { AttentionItem } from "../../lib/pipeline";
import { Card } from "../../ui/Card";
import { EmptyState } from "../../ui/EmptyState";

export function AttentionList({ items }: { items: AttentionItem[] }) {
  return (
    <Card title="Needs attention">
      {items.length === 0 ? (
        <EmptyState title="Nothing needs you.">Every stage is running as it should.</EmptyState>
      ) : (
        <ul className="attn">
          {items.map((item) => (
            <li key={item.text} className={`attn-item ${item.tone}`}>
              <span className="attn-dot" aria-hidden="true" />
              {item.to ? <Link to={routerPath(item.to)}>{item.text}</Link> : <span>{item.text}</span>}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
