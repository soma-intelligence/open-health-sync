import { Component, type ReactNode } from "react";
import { useLocation } from "react-router";

import { Button } from "../ui/Button";
import { ErrorPanel } from "../ui/ErrorPanel";

interface Props {
  pathname: string;
  children: ReactNode;
}

interface State {
  error: unknown;
  failed: boolean;
  /** The address the page failed at; null until it fails. */
  failedAt: string | null;
}

class Boundary extends Component<Props, State> {
  override state: State = { error: null, failed: false, failedAt: null };

  static getDerivedStateFromError(error: unknown): Partial<State> {
    return { error, failed: true };
  }

  override componentDidCatch() {
    this.setState({ failedAt: this.props.pathname });
  }

  override componentDidUpdate() {
    const { failed, failedAt } = this.state;
    if (failed && failedAt !== null && this.props.pathname !== failedAt) {
      this.setState({ error: null, failed: false, failedAt: null });
    }
  }

  override render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="page">
        <h1 className="page-title">Something went wrong on this page</h1>
        <ErrorPanel error={this.state.error} />
        <p className="row-actions">
          <Button
            kind="primary"
            onClick={() => {
              window.location.reload();
            }}
          >
            Reload page
          </Button>
          <Button
            onClick={() => {
              this.setState({ error: null, failed: false, failedAt: null });
            }}
          >
            Try again
          </Button>
        </p>
      </div>
    );
  }
}

/**
 * Catches an exception of the page below it; the shell around it stays.
 * Navigating away from the failed address clears it. One instance for every
 * address, so a page keeps its state while its own path changes (a settings
 * section, a user tab).
 */
export function PageBoundary({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  return <Boundary pathname={pathname}>{children}</Boundary>;
}
