import { ApiError } from "../api/client";

/** What an operator reads for a failed request: what happened and what to do, in words. */
export interface ErrorText {
  /** One sentence for the page. */
  message: string;
  /** The server's own words, when they add something to the message; shown smaller. */
  detail: string | null;
}

// Server details that say nothing the message does not already say.
const BLAND = new Set(["internal error", "internal server error", "service unavailable", "bad gateway", "gateway timeout"]);

export function errorText(error: unknown): ErrorText {
  if (error instanceof ApiError) {
    const detail = error.detail.trim();
    const extra = detail !== "" && !BLAND.has(detail.toLowerCase()) ? detail : null;
    if (error.status === 503) return { message: "The server is not ready yet. Try again in a moment.", detail: extra };
    if (error.status >= 500) {
      return { message: "The server had a problem with this request. Try again; if it keeps failing, quote the request id.", detail: extra };
    }
    if (error.status === 429) return { message: "Too many requests. Wait a moment, then try again.", detail: extra };
    return { message: detail !== "" ? detail : `The server refused this request (HTTP ${String(error.status)}).`, detail: null };
  }
  // fetch rejects with a TypeError when no answer came back at all.
  if (error instanceof TypeError) return { message: "Cannot reach the server. Check your connection, then try again.", detail: null };
  if (error instanceof Error && error.message !== "") return { message: error.message, detail: null };
  return { message: "Something went wrong.", detail: null };
}

/** errorText in one line, for a toast. */
export function errorLine(error: unknown, fallback: string): string {
  if (!(error instanceof Error)) return fallback;
  const { message, detail } = errorText(error);
  return detail ? `${message} (${detail})` : message;
}
