import { ApiError, CSRF_HEADER, CSRF_REFUSED, ReadOnlyError, SESSION_PATH, createClient, type ApiClient } from "./client";

interface Call {
  path: string;
  init: RequestInit;
}

function problem(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/problem+json", ...headers } });
}

function setup(
  answers: (() => Response)[],
  over: { readOnly?: boolean; csrf?: string | null; refresh?: string | null } = {},
) {
  const calls: Call[] = [];
  let i = 0;
  const fetchImpl = ((path: string, init: RequestInit) => {
    calls.push({ path, init });
    const make = answers[Math.min(i, answers.length - 1)];
    i += 1;
    if (!make) throw new Error("no answer");
    return Promise.resolve(make());
  }) as unknown as typeof fetch;
  const refreshCsrf = vi.fn(() => Promise.resolve(over.refresh ?? null));
  const onUnauthorized = vi.fn();
  const api: ApiClient = createClient({
    fetch: fetchImpl,
    csrf: () => (over.csrf === undefined ? "tok" : over.csrf),
    refreshCsrf,
    readOnly: () => over.readOnly ?? false,
    onUnauthorized,
  });
  return { api, calls, refreshCsrf, onUnauthorized };
}

const ok = () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });

function header(call: Call | undefined, name: string): string | null {
  return new Headers(call?.init.headers).get(name);
}

describe("createClient", () => {
  it("sends cookies on this origin and never an Authorization header", async () => {
    const { api, calls } = setup([ok]);
    await api.get("/v1/users");
    const init = calls[0]?.init;
    expect(init?.credentials).toBe("same-origin");
    expect(init?.cache).toBe("no-store");
    expect(init?.redirect).toBe("error");
    expect(init?.referrerPolicy).toBe("no-referrer");
    expect(header(calls[0], "Authorization")).toBeNull();
  });

  it("adds X-OHS-CSRF to every method but GET and HEAD", async () => {
    const { api, calls } = setup([ok]);
    for (const method of ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"] as const) {
      await api.request(method, "/v1/users");
    }
    const sent = calls.map((c) => header(c, CSRF_HEADER));
    expect(sent).toEqual([null, null, "tok", "tok", "tok", "tok"]);
  });

  it.each(["//evil.example/x", "https://x/y", "\\x", "v1/users"])("refuses %s before any request", async (path) => {
    const { api, calls } = setup([ok]);
    await expect(api.get(path)).rejects.toBeInstanceOf(ReadOnlyError);
    expect(calls).toHaveLength(0);
  });

  it("read only refuses writes but lets the session in, out and across tenants", async () => {
    const { api, calls } = setup([ok], { readOnly: true });
    await expect(api.send("POST", "/v1/users", {})).rejects.toBeInstanceOf(ReadOnlyError);
    expect(calls).toHaveLength(0);
    await api.send("POST", SESSION_PATH, { key: "k" });
    await api.send("DELETE", SESSION_PATH);
    await api.send("PUT", `${SESSION_PATH}/tenant`, { tenant_id: "t" });
    expect(calls.map((c) => `${String(c.init.method)} ${c.path}`)).toEqual([
      "POST /console/session",
      "DELETE /console/session",
      "PUT /console/session/tenant",
    ]);
  });

  it("turns a problem answer into an ApiError", async () => {
    const body = { title: "Unprocessable", detail: "bad", request_id: "r1" };
    const { api } = setup([() => problem(422, body)]);
    const err = await api.get("/v1/users").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect(e.status).toBe(422);
    expect(e.detail).toBe("bad");
    expect(e.requestId).toBe("r1");
    expect(e.body).toEqual(body);
  });

  it("calls onUnauthorized on a 401 except for the sign in itself", async () => {
    const { api, onUnauthorized } = setup([() => problem(401, { title: "Unauthorized", detail: "no" })]);
    await expect(api.get("/v1/users")).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
    await expect(api.send("POST", SESSION_PATH, { key: "k" })).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it("a stale CSRF token is refreshed once and the write retried once", async () => {
    const stale = () => problem(403, { title: "Forbidden", detail: CSRF_REFUSED });
    const created = () => new Response('{"id":"u1"}', { status: 201, headers: { "Content-Type": "application/json" } });
    const first = setup([stale, created], { refresh: "new" });
    await expect(first.api.send("POST", "/v1/users", { a: 1 })).resolves.toEqual({ id: "u1" });
    expect(first.calls).toHaveLength(2);
    expect(header(first.calls[0], CSRF_HEADER)).toBe("tok");
    expect(header(first.calls[1], CSRF_HEADER)).toBe("new");

    const second = setup([stale, stale], { refresh: "new" });
    await expect(second.api.send("POST", "/v1/users", { a: 1 })).rejects.toMatchObject({ status: 403 });
    expect(second.calls).toHaveLength(2);
    expect(second.refreshCsrf).toHaveBeenCalledTimes(1);
  });

  it("other 403 answers are not retried", async () => {
    const { api, calls, refreshCsrf } = setup([() => problem(403, { title: "Forbidden", detail: "the console is read only" })]);
    await expect(api.send("POST", "/v1/users", {})).rejects.toMatchObject({ status: 403 });
    expect(calls).toHaveLength(1);
    expect(refreshCsrf).not.toHaveBeenCalled();
  });

  it("anonymous requests send no cookie and no CSRF", async () => {
    const { api, calls } = setup([ok]);
    await api.request("POST", "/v1/setup", { body: {}, anonymous: true });
    expect(calls[0]?.init.credentials).toBe("omit");
    expect(header(calls[0], CSRF_HEADER)).toBeNull();
  });

  it("reads 204 as null, text as text, blob with its file name", async () => {
    const { api } = setup([
      () => new Response(null, { status: 204 }),
      () => new Response("A=1\n", { status: 200, headers: { "Content-Type": "text/plain" } }),
      () => new Response("zip", { status: 200, headers: { "Content-Disposition": 'attachment; filename="ohs.env"' } }),
      () => new Response("raw", { status: 200 }),
    ]);
    expect((await api.request("DELETE", "/v1/users/u1")).data).toBeNull();
    expect((await api.request<string>("GET", "/v1/admin/settings/export", { as: "text" })).data).toBe("A=1\n");
    const blob = (await api.request<{ blob: Blob; filename: string | null }>("GET", "/x", { as: "blob" })).data;
    expect(blob.filename).toBe("ohs.env");
    expect(await blob.blob.text()).toBe("zip");
    const bare = (await api.request<{ blob: Blob; filename: string | null }>("GET", "/y", { as: "blob" })).data;
    expect(bare.filename).toBeNull();
  });
});
