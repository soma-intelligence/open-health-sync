import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { ApiError } from "../api/client";
import { errorText } from "../lib/errorText";
import { Button } from "./Button";
import { Icon } from "./Icon";

export interface ErrorPanelProps {
  error: unknown;
  /**
   * A way out. A function is called as is; true reloads every query on the
   * page that failed. Left out for a write: the form that sent it stays open
   * and its own button sends it again.
   */
  retry?: boolean | (() => unknown);
}

/** A failed request or render: what happened in words, the server's detail and request id when there is one, and a retry. */
export function ErrorPanel({ error, retry }: ErrorPanelProps) {
  const { message, detail } = errorText(error);
  return (
    <div className="error-panel" role="alert">
      <Icon name="alert" />
      <div className="error-text">
        <p className="error-detail">{message}</p>
        {detail ? <p className="error-id">{detail}</p> : null}
        {error instanceof ApiError && error.requestId ? (
          <p className="error-id">
            Request <span className="mono">{error.requestId}</span>
          </p>
        ) : null}
      </div>
      {retry === true ? <RetryFailedQueries /> : null}
      {typeof retry === "function" ? <RetryButton onRetry={retry} /> : null}
    </div>
  );
}

function RetryButton({ onRetry }: { onRetry: () => unknown }) {
  const [busy, setBusy] = useState(false);
  return (
    <Button
      size="sm"
      className="error-retry"
      disabled={busy}
      aria-busy={busy}
      onClick={() => {
        setBusy(true);
        void Promise.resolve(onRetry()).finally(() => {
          setBusy(false);
        });
      }}
    >
      <Icon name="refresh" size="sm" />
      {busy ? "Trying again" : "Try again"}
    </Button>
  );
}

function RetryFailedQueries() {
  const queryClient = useQueryClient();
  return (
    <RetryButton
      onRetry={() => queryClient.refetchQueries({ type: "active", predicate: (q) => q.state.status === "error" })}
    />
  );
}
