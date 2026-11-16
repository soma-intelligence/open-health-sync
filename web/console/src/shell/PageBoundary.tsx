import { Component, type ReactNode } from "react";
import { useLocation } from "react-router";

import { Button } from "../ui/Button";
import { ErrorPanel } from "../ui/ErrorPanel";

interface State {
  error: unknown;
  failed: boolean;
}

class Boundary extends Component<{ children: ReactNode }, State> {
  override state: State = { error: null, failed: false };

  static getDerivedStateFromError(error: unknown): State {
    return { error, failed: true };
  }

  override render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="page">
        <h1 className="page-title">This page failed</h1>
        <ErrorPanel error={this.state.error} />
        <p>
          <Button
            onClick={() => {
              this.setState({ error: null, failed: false });
            }}
          >
            Reload page
          </Button>
        </p>
      </div>
    );
  }
}

/** Catches an exception of the page below it; the shell around it stays. Navigating clears it. */
export function PageBoundary({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  return <Boundary key={pathname}>{children}</Boundary>;
}
