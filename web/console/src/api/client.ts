import { filenameFrom, withQuery, type Query } from "./paths";

export type Method = "GET" | "HEAD" | "POST" | "PUT" | "PATCH" | "DELETE";

export interface RequestOptions {
  body?: unknown;
  query?: Query;
  headers?: Record<string, string>;
  as?: "json" | "text" | "blob";
  /** Send no cookie and no CSRF token. */
  anonymous?: boolean;
}

export interface ApiResponse<T> {
  data: T;
  status: number;
  headers: Headers;
}

/** What `as: "blob"` answers with. filename is null when the answer names none. */
export interface Download {
  blob: Blob;
  filename: string | null;
}

export interface ApiClient {
  request: <T>(method: Method, path: string, opts?: RequestOptions) => Promise<ApiResponse<T>>;
  get: <T>(path: string, query?: Query) => Promise<T>;
  send: <T>(method: Method, path: string, body?: unknown) => Promise<T>;
}

export interface ClientDeps {
  fetch: typeof fetch;
  csrf: () => string | null;
  refreshCsrf: () => Promise<string | null>;
  readOnly: () => boolean;
  onUnauthorized: () => void;
}

export const SESSION_PATH = "/console/session";
export const CSRF_HEADER = "X-OHS-CSRF";
export const CSRF_REFUSED = "this request needs the X-OHS-CSRF token of the console session; reload the page";

const TENANT_PATH = `${SESSION_PATH}/tenant`;

/** A request the mode or the origin rule refuses, thrown before anything is sent. */
export class ReadOnlyError extends Error {}

/** A non-2xx answer; detail comes from the problem body. */
export class ApiError extends Error {
  readonly status: number;
  readonly detail: string;
  readonly requestId: string | null;
  readonly body: unknown;

  constructor(status: number, detail: string, requestId: string | null, body: unknown) {
    super(`HTTP ${String(status)}${detail ? ": " + detail : ""}`);
    this.name = "ApiError";
    this.status = status;
    this.detail = detail;
    this.requestId = requestId;
    this.body = body;
  }
}

/** The window's own fetch, for the one place that may call it. */
export const browserFetch: typeof fetch = (input, init) => fetch(input, init);

const safeMethod = (method: Method): boolean => method === "GET" || method === "HEAD";

// A path on this origin starts with one slash and holds no backslash.
function samePath(path: string): boolean {
  return path.startsWith("/") && !path.startsWith("//") && !path.includes("\\");
}

// The session's own calls stay open to a read only session: nothing is written.
function sessionCall(method: Method, path: string): boolean {
  const bare = path.split("?")[0];
  return (
    (bare === SESSION_PATH && (method === "POST" || method === "DELETE")) || (bare === TENANT_PATH && method === "PUT")
  );
}

async function readProblem(res: Response): Promise<{ body: unknown; detail: string; requestId: string | null }> {
  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  let detail = "";
  let requestId: string | null = res.headers.get("X-Request-Id");
  if (body !== null && typeof body === "object") {
    const p = body as { detail?: unknown; title?: unknown; request_id?: unknown };
    if (typeof p.detail === "string" && p.detail) detail = p.detail;
    else if (typeof p.title === "string") detail = p.title;
    if (typeof p.request_id === "string") requestId = p.request_id;
  }
  return { body, detail, requestId };
}

export function createClient(deps: ClientDeps): ApiClient {
  function send(method: Method, path: string, opts: RequestOptions, csrf: string | null): Promise<Response> {
    const headers = new Headers({ Accept: opts.as === "json" || opts.as === undefined ? "application/json" : "*/*" });
    const hasBody = opts.body !== undefined && opts.body !== null;
    if (hasBody) headers.set("Content-Type", "application/json");
    if (!safeMethod(method) && !opts.anonymous && csrf) headers.set(CSRF_HEADER, csrf);
    for (const [k, v] of Object.entries(opts.headers ?? {})) headers.set(k, v);
    const init: RequestInit = {
      method,
      headers,
      credentials: opts.anonymous ? "omit" : "same-origin",
      cache: "no-store",
      redirect: "error",
      referrerPolicy: "no-referrer",
    };
    if (hasBody) init.body = JSON.stringify(opts.body);
    return deps.fetch(path, init);
  }

  async function request<T>(method: Method, rawPath: string, opts: RequestOptions = {}): Promise<ApiResponse<T>> {
    const path = withQuery(rawPath, opts.query);
    if (!samePath(path)) throw new ReadOnlyError("the console only calls paths on this server");
    if (safeMethod(method) && opts.body !== undefined && opts.body !== null) {
      throw new ReadOnlyError(`a ${method} carries no body`);
    }
    if (!safeMethod(method) && deps.readOnly() && !sessionCall(method, path)) {
      throw new ReadOnlyError(`the console is read only: ${method} refused`);
    }

    let res = await send(method, path, opts, deps.csrf());
    if (res.status === 403 && !safeMethod(method) && !opts.anonymous) {
      const first = await readProblem(res.clone());
      if (first.detail === CSRF_REFUSED) {
        const fresh = await deps.refreshCsrf();
        if (fresh) res = await send(method, path, opts, fresh);
      }
    }

    if (!res.ok) {
      const p = await readProblem(res);
      const signIn = method === "POST" && path === SESSION_PATH;
      // An anonymous call carries no session, so its 401 says nothing about it.
      if (res.status === 401 && !signIn && !opts.anonymous) deps.onUnauthorized();
      throw new ApiError(res.status, p.detail, p.requestId, p.body);
    }

    let data: unknown;
    if (res.status === 204) {
      data = null;
    } else if (opts.as === "text") {
      data = await res.text();
    } else if (opts.as === "blob") {
      const disposition = res.headers.get("Content-Disposition");
      const download: Download = { blob: await res.blob(), filename: disposition ? filenameFrom(disposition) : null };
      data = download;
    } else {
      const text = await res.text();
      data = text === "" ? null : (JSON.parse(text) as unknown);
    }
    return { data: data as T, status: res.status, headers: res.headers };
  }

  return {
    request,
    get: async <T,>(path: string, query?: Query) => (await request<T>("GET", path, query ? { query } : {})).data,
    send: async <T,>(method: Method, path: string, body?: unknown) =>
      (await request<T>(method, path, body === undefined ? {} : { body })).data,
  };
}
