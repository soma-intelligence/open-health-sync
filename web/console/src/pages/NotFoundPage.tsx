import { Link } from "react-router";

import { Card } from "../ui/Card";
import { EmptyState } from "../ui/EmptyState";

export function NotFoundPage() {
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Page not found</h1>
          <p className="page-sub">There is nothing at this address in the console.</p>
        </div>
      </header>
      <Card>
        <EmptyState title="Nothing here">
          The link may be old or mistyped. <Link to="/overview">Go to the overview</Link>.
        </EmptyState>
      </Card>
    </div>
  );
}
