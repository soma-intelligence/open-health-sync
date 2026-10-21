// SPDX-License-Identifier: AGPL-3.0-only
//
// The Open Health Sync client of this app, holding the tenant API key. Imported by Server
// Components, Server Actions and Route Handlers only.

import { OhsError, OpenHealthSync } from "@open-health-sync/server";
import { requireConfig } from "./config.ts";

/** A client authenticated with the tenant API key (ohs_sk_...). */
export function tenantClient(): OpenHealthSync {
  const { ohsUrl, apiKey } = requireConfig();
  return new OpenHealthSync({ baseUrl: ohsUrl, apiKey });
}

/** A client authenticated with a user token (ohs_ut_...), as a phone app would hold one. */
export function userClient(token: string): OpenHealthSync {
  return new OpenHealthSync({ baseUrl: requireConfig().ohsUrl, apiKey: token });
}

export interface ShownError {
  message: string;
  status?: number;
  requestId?: string;
}

/** An error as the pages show it: the problem's title and detail, and the request id to quote. */
export function showError(err: unknown): ShownError {
  if (err instanceof OhsError) {
    return {
      message: err.detail ? `${err.title}: ${err.detail}` : err.title,
      status: err.status,
      requestId: err.requestId,
    };
  }
  if (err instanceof Error) {
    // fetch reports a refused connection as "fetch failed" with the reason in cause.
    const cause = err.cause instanceof Error ? ` (${err.cause.message})` : "";
    return { message: `${err.message}${cause}` };
  }
  return { message: String(err) };
}

export type Result<T> = { ok: true; value: T } | { ok: false; error: ShownError };

/** Runs a call and keeps its error instead of throwing, so one failed call does not blank a page. */
export async function attempt<T>(call: () => Promise<T>): Promise<Result<T>> {
  try {
    return { ok: true, value: await call() };
  } catch (err) {
    return { ok: false, error: showError(err) };
  }
}
