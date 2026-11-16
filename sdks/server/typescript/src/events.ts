// SPDX-License-Identifier: AGPL-3.0-only
//
// The event stream (GET /v1/events/stream, GET /v1/users/{id}/events/stream): the events
// webhooks carry, as Server-Sent Events, with reconnection and Last-Event-ID resume.
// Written by hand (not generated): the generated client leaves the stream routes out.

import { OhsError } from "./client.ts";
import type { WebhookEvent, WebhookEventType } from "./types.ts";

/** The event the server sends when it could not resume where the client left off. */
export const STREAM_RESET_EVENT = "stream.reset";

export interface StreamEventsOptions {
  /** Base URL of the instance, for example https://health.example.com. */
  baseUrl: string;
  /** Tenant API key (ohs_sk_...), or the user's token (ohs_ut_..., scope read) with `user`. */
  apiKey: string;
  /** Streams the events of this user only, from GET /v1/users/{user}/events/stream. */
  user?: string;
  /** On the tenant stream, only the events about this user. */
  userId?: string;
  /** Only these event types; every type when absent. */
  types?: WebhookEventType[];
  /** Resume after this event id (the id of the last event handled). */
  lastEventId?: string;
  /** Reconnect when the stream ends (true by default); false returns instead. */
  reconnect?: boolean;
  /** Ends the stream. */
  signal?: AbortSignal;
  /** The fetch to call; the global fetch when absent. */
  fetch?: typeof fetch;
}

/** One message of the stream: an event, or a reset. */
export type StreamMessage =
  | { kind: "event"; id: string; type: WebhookEventType; event: WebhookEvent }
  | { kind: "reset"; reason: string; windowHours: number };

/** One block of a Server-Sent Events stream. */
export interface SSEBlock {
  id?: string;
  event?: string;
  data?: string;
  retry?: number;
}

/** Parses Server-Sent Events from text chunks; comments are skipped. */
export async function* parseSSE(chunks: AsyncIterable<string>): AsyncGenerator<SSEBlock> {
  let buffer = "";
  let block: SSEBlock = {};
  let data: string[] = [];
  let seen = false;
  for await (const chunk of chunks) {
    buffer += chunk;
    let end: number;
    while ((end = buffer.search(/\r\n|\r|\n/)) >= 0) {
      const line = buffer.slice(0, end);
      buffer = buffer.slice(end + (buffer.startsWith("\r\n", end) ? 2 : 1));
      if (line === "") {
        if (seen) {
          if (data.length > 0) block.data = data.join("\n");
          yield block;
        }
        block = {};
        data = [];
        seen = false;
        continue;
      }
      if (line.startsWith(":")) continue;
      const colon = line.indexOf(":");
      const name = colon < 0 ? line : line.slice(0, colon);
      let value = colon < 0 ? "" : line.slice(colon + 1);
      if (value.startsWith(" ")) value = value.slice(1);
      seen = true;
      if (name === "id") block.id = value;
      else if (name === "event") block.event = value;
      else if (name === "data") data.push(value);
      else if (name === "retry" && /^[0-9]+$/.test(value)) block.retry = Number(value);
    }
  }
}

async function* text(body: ReadableStream<Uint8Array>): AsyncGenerator<string> {
  const decoder = new TextDecoder();
  const reader = body.getReader();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      yield decoder.decode(value, { stream: true });
    }
  } finally {
    reader.releaseLock();
  }
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(timer);
      resolve();
    }, { once: true });
  });
}

/**
 * Yields the events of the tenant (or of one user) as they happen, reconnecting with
 * Last-Event-ID when the stream ends. Deduplicate on `id`, as with webhooks. A 503 is
 * retried after Retry-After; any other refusal (401, 403, 404, 429) throws OhsError.
 *
 *     for await (const m of streamEvents({ baseUrl, apiKey, types: ["data.written"] })) {
 *       if (m.kind === "event") await refresh(m.event.data.user_id);
 *     }
 */
export async function* streamEvents(options: StreamEventsOptions): AsyncGenerator<StreamMessage> {
  if (!options.baseUrl) throw new TypeError("streamEvents: baseUrl is required");
  if (!options.apiKey) throw new TypeError("streamEvents: apiKey is required");
  const doFetch = options.fetch ?? ((input, init) => fetch(input, init));
  const base = options.baseUrl.replace(/\/+$/, "");
  const path = options.user ? `/v1/users/${encodeURIComponent(options.user)}/events/stream` : "/v1/events/stream";
  let lastEventId = options.lastEventId;
  let retry = 3000;
  while (!options.signal?.aborted) {
    const url = new URL(base + path);
    if (options.userId && !options.user) url.searchParams.set("user_id", options.userId);
    if (options.types?.length) url.searchParams.set("types", options.types.join(","));
    const headers: Record<string, string> = { Authorization: `Bearer ${options.apiKey}`, Accept: "text/event-stream" };
    if (lastEventId) headers["Last-Event-ID"] = lastEventId;
    let res: Response;
    try {
      res = await doFetch(url, { headers, signal: options.signal });
    } catch {
      if (options.signal?.aborted || options.reconnect === false) return;
      await sleep(retry, options.signal);
      continue;
    }
    if (res.status === 503) {
      await res.body?.cancel();
      if (options.reconnect === false) throw new OhsError(503, "Service Unavailable");
      await sleep(Number(res.headers.get("Retry-After") ?? "1") * 1000 || retry, options.signal);
      continue;
    }
    if (!res.ok) throw await OhsError.fromResponse(res);
    if (!res.body) return;
    try {
      for await (const block of parseSSE(text(res.body))) {
        if (block.retry !== undefined) retry = block.retry;
        if (block.event === STREAM_RESET_EVENT) {
          const reset = JSON.parse(block.data ?? "{}") as { reason?: string; window_hours?: number };
          yield { kind: "reset", reason: reset.reason ?? "", windowHours: reset.window_hours ?? 0 };
          continue;
        }
        if (block.data === undefined || !block.id) continue;
        lastEventId = block.id;
        const event = JSON.parse(block.data) as WebhookEvent;
        yield { kind: "event", id: block.id, type: event.type as WebhookEventType, event };
      }
    } catch (err) {
      if (!options.signal?.aborted && options.reconnect === false) throw err;
    }
    if (options.reconnect === false) return;
    await sleep(retry, options.signal);
  }
}
