import { Link } from "react-router";

import { EmptyState } from "../ui/EmptyState";

export function NotFoundPage() {
  return (
    <div className="page">
      <EmptyState title="Not found">
        There is nothing at this address. <Link to="/overview">Go to the overview</Link>.
      </EmptyState>
    </div>
  );
}
