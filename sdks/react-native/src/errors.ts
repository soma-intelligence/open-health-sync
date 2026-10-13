// SPDX-License-Identifier: AGPL-3.0-only

/**
 * A non-2xx answer of the API. It carries the RFC 9457 problem fields when the body has
 * them; a body that is not JSON still gives the status. The message never contains the
 * Authorization header or a health value.
 */
export class OhsApiError extends Error {
  /** HTTP status code. */
  readonly status: number;
  /** RFC 9457 `title`. */
  readonly title?: string;
  /** RFC 9457 `detail`. */
  readonly detail?: string;
  /** RFC 9457 `type`. */
  readonly type?: string;
  /** The request id to quote in a bug report: body `request_id`, else `X-Request-Id`. */
  readonly requestId?: string;
  /** `Retry-After` in seconds, when the server sent it. */
  readonly retryAfterSeconds?: number;

  constructor(init: {
    status: number;
    title?: string;
    detail?: string;
    type?: string;
    requestId?: string;
    retryAfterSeconds?: number;
  }) {
    super(OhsApiError.describe(init));
    this.name = "OhsApiError";
    // Keep instanceof working when classes are transpiled (Babel, older targets).
    Object.setPrototypeOf(this, OhsApiError.prototype);
    this.status = init.status;
    if (init.title !== undefined) this.title = init.title;
    if (init.detail !== undefined) this.detail = init.detail;
    if (init.type !== undefined) this.type = init.type;
    if (init.requestId !== undefined) this.requestId = init.requestId;
    if (init.retryAfterSeconds !== undefined) this.retryAfterSeconds = init.retryAfterSeconds;
  }

  private static describe(init: { status: number; title?: string; detail?: string; requestId?: string }): string {
    let msg = `Open Health Sync API error ${init.status}`;
    if (init.title) msg += ` ${init.title}`;
    if (init.detail) msg += `: ${init.detail}`;
    if (init.requestId) msg += ` (request id ${init.requestId})`;
    return msg;
  }
}

/** Parses `Retry-After`: delay seconds, or an HTTP date. Returns undefined when absent or invalid. */
export function parseRetryAfter(value: string | null, now: number = Date.now()): number | undefined {
  if (value === null) return undefined;
  const v = value.trim();
  if (/^\d+$/.test(v)) return parseInt(v, 10);
  const at = Date.parse(v);
  if (Number.isNaN(at)) return undefined;
  return Math.max(0, Math.ceil((at - now) / 1000));
}
