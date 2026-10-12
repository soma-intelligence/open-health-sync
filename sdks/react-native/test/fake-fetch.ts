// SPDX-License-Identifier: MIT

import type { FetchLike, FetchRequestInit, FetchResponse } from "../src/index.ts";

export interface Recorded {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: string | Uint8Array;
  path: string;
  query: Record<string, string>;
}

export interface Reply {
  status?: number;
  body?: unknown;
  /** A raw text body, sent as is. */
  text?: string;
  headers?: Record<string, string>;
  /** Reject the fetch call, as a network error does. */
  networkError?: boolean;
}

/** A fake `fetch` answering from a queue (or a function) and recording every request. */
export function fakeFetch(replies: Reply[] | ((req: Recorded) => Reply)) {
  const requests: Recorded[] = [];
  const queue = Array.isArray(replies) ? [...replies] : null;
  const fetch: FetchLike = async (url: string, init: FetchRequestInit): Promise<FetchResponse> => {
    const u = new URL(url);
    const query: Record<string, string> = {};
    u.searchParams.forEach((v, k) => {
      query[k] = v;
    });
    const rec: Recorded = {
      url,
      method: init.method,
      headers: { ...init.headers },
      path: u.pathname,
      query,
    };
    if (init.body !== undefined) rec.body = init.body;
    requests.push(rec);
    const reply = queue ? queue.shift() : (replies as (r: Recorded) => Reply)(rec);
    if (!reply) throw new Error(`fake fetch: no reply queued for ${init.method} ${u.pathname}`);
    if (reply.networkError) throw new TypeError("fetch failed");
    const headers = new Map<string, string>();
    for (const [k, v] of Object.entries(reply.headers ?? {})) headers.set(k.toLowerCase(), v);
    const text =
      reply.text !== undefined ? reply.text : reply.body === undefined ? "" : JSON.stringify(reply.body);
    if (reply.body !== undefined && !headers.has("content-type")) {
      headers.set("content-type", "application/json");
    }
    return {
      status: reply.status ?? 200,
      headers: { get: (name: string) => headers.get(name.toLowerCase()) ?? null },
      text: async () => text,
    };
  };
  return { fetch, requests };
}
