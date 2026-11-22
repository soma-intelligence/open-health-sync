// A fake server for component tests: a fetch that answers by "METHOD /path"
// and records every call.

export interface FakeCall {
  method: string;
  /** Path with its query string, as sent. */
  path: string;
  /** Header names are lower case. */
  headers: Record<string, string>;
  /** The parsed JSON body, or undefined. */
  body: unknown;
  /** The credentials mode the call was sent with, if any. */
  credentials: RequestCredentials | undefined;
}

export interface FakeReply {
  status?: number;
  body?: unknown;
  headers?: Record<string, string>;
}

export type FakeHandler = FakeReply | ((call: FakeCall) => FakeReply | Promise<FakeReply>);

export interface FakeApi {
  fetch: typeof fetch;
  calls: FakeCall[];
  /** The recorded calls of one "METHOD /path" key (query ignored). */
  callsTo: (key: string) => FakeCall[];
}

function parseBody(body: unknown): unknown {
  if (typeof body !== "string" || body === "") return undefined;
  try {
    return JSON.parse(body) as unknown;
  } catch {
    return body;
  }
}

export function fakeApi(routes: Record<string, FakeHandler>): FakeApi {
  const calls: FakeCall[] = [];
  const fakeFetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const path = typeof input === "string" ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const method = (init?.method ?? "GET").toUpperCase();
    const call: FakeCall = {
      method,
      path,
      headers: Object.fromEntries(new Headers(init?.headers).entries()),
      body: parseBody(init?.body),
      credentials: init?.credentials,
    };
    calls.push(call);
    const bare = path.split("?")[0] ?? path;
    const handler = routes[`${method} ${path}`] ?? routes[`${method} ${bare}`];
    if (handler === undefined) {
      return new Response(JSON.stringify({ title: "Not Found", status: 404, detail: `no fake route for ${method} ${path}` }), {
        status: 404,
        headers: { "Content-Type": "application/problem+json" },
      });
    }
    const reply = typeof handler === "function" ? await handler(call) : handler;
    const status = reply.status ?? 200;
    const text = reply.body === undefined ? null : typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body);
    const contentType = typeof reply.body === "string" ? "text/plain" : status >= 400 ? "application/problem+json" : "application/json";
    return new Response(status === 204 ? null : text, {
      status,
      headers: { "Content-Type": contentType, ...reply.headers },
    });
  };
  return {
    fetch: fakeFetch as typeof fetch,
    calls,
    callsTo: (key) => calls.filter((c) => `${c.method} ${c.path.split("?")[0] ?? c.path}` === key),
  };
}
