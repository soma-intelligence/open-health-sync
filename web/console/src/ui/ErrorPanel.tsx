import { ApiError } from "../api/client";
import { Icon } from "./Icon";

/** A failed request or render, as text: the API detail and its request id when there is one. */
export function ErrorPanel({ error }: { error: unknown }) {
  const detail = error instanceof ApiError ? error.detail || error.message : error instanceof Error ? error.message : "Something went wrong.";
  return (
    <div className="error-panel" role="alert">
      <Icon name="alert" />
      <div>
        <p className="error-detail">{detail}</p>
        {error instanceof ApiError && error.requestId ? (
          <p className="error-id">
            Request <span className="mono">{error.requestId}</span>
          </p>
        ) : null}
      </div>
    </div>
  );
}
