// SPDX-License-Identifier: AGPL-3.0-only
//
// streamEvents against a fake fetch that answers Server-Sent Events.

import assert from "node:assert/strict";
import { test } from "node:test";
import { parseSSE, streamEvents, type StreamMessage } from "../src/index.ts";

async function* chunks(...parts: string[]): AsyncGenerator<string> {
  yield* parts;
}

test("parseSSE: fields, comments, multi-line data, split chunks", async () => {
  const blocks = [];
  for await (const b of parseSSE(chunks("retry: 3000\n\n: connected\n\nid: a\nev", "ent: tag.created\ndata: {\"x\":\r\ndata: 1}\n\n: ping\n\n"))) {
    blocks.push(b);
  }
  assert.deepEqual(blocks, [{ retry: 3000 }, { id: "a", event: "tag.created", data: '{"x":\n1}' }]);
});

const payload = (id: string) => JSON.stringify({ id, type: "data.written", timestamp: "2026-10-12T08:30:12.345Z", data: { user_id: "u" } });

test("streamEvents: events, a reset, and a resume with Last-Event-ID", async () => {
  const calls: { url: string; last: string | null }[] = [];
  const bodies = [
    `retry: 1\n\nid: e1\nevent: data.written\ndata: ${payload("e1")}\n\n`,
    `event: stream.reset\ndata: {"reason":"last_event_id_unknown","window_hours":24}\n\nid: e2\nevent: data.written\ndata: ${payload("e2")}\n\n`,
  ];
  const fake: typeof fetch = async (input, init) => {
    const headers = new Headers(init?.headers);
    calls.push({ url: String(input), last: headers.get("Last-Event-ID") });
    const body = bodies.shift();
    if (body === undefined) return new Response("{}", { status: 401, headers: { "Content-Type": "application/problem+json" } });
    return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } });
  };
  const got: StreamMessage[] = [];
  await assert.rejects(async () => {
    for await (const m of streamEvents({ baseUrl: "https://ohs.test/", apiKey: "ohs_sk_x", types: ["data.written", "tag.created"], userId: "u", fetch: fake })) {
      got.push(m);
    }
  }, (err: Error) => err.name === "OhsError");
  assert.equal(calls[0].url, "https://ohs.test/v1/events/stream?user_id=u&types=data.written%2Ctag.created");
  assert.equal(calls[0].last, null);
  assert.equal(calls[1].last, "e1", "the reconnection resumes after the last event");
  assert.equal(calls[2].last, "e2");
  assert.deepEqual(got.map((m) => (m.kind === "event" ? m.id : m.reason)), ["e1", "last_event_id_unknown", "e2"]);
  const first = got[0];
  assert.ok(first.kind === "event" && first.event.data.user_id === "u" && first.type === "data.written");
});

test("streamEvents: one user's stream, no reconnection", async () => {
  let url = "";
  const fake: typeof fetch = async (input) => {
    url = String(input);
    return new Response(`id: e1\nevent: tag.created\ndata: ${payload("e1")}\n\n`, { status: 200 });
  };
  const ids = [];
  for await (const m of streamEvents({ baseUrl: "https://ohs.test", apiKey: "ohs_ut_x", user: "a b", reconnect: false, fetch: fake })) {
    if (m.kind === "event") ids.push(m.id);
  }
  assert.equal(url, "https://ohs.test/v1/users/a%20b/events/stream");
  assert.deepEqual(ids, ["e1"]);
});
