import type { components } from "../../api/schema";

type Schemas = components["schemas"];

/** SETUP_PATH is the only URL the setup page calls. */
export const SETUP_PATH = "/v1/setup";

/** MAX_TENANT_NAME mirrors the server's limit, in bytes. */
export const MAX_TENANT_NAME = 100;

/** A refused or failed setup call; message is for the operator. */
export class SetupError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "SetupError";
    this.status = status;
  }
}

/** Explains a non-2xx answer of /v1/setup. */
export function setupMessage(status: number, detail: string): string {
  switch (status) {
    case 401:
      return "This setup token was refused. It is the OHS_SETUP_TOKEN of this instance.";
    case 409:
      return "This instance is already set up.";
    case 404:
      return "The first-run setup is off on this instance (OHS_SETUP_TOKEN is not set).";
    case 400:
      return `The request was refused${detail ? ": " + detail : "."}`;
    case 413:
      return "The tenant name is too long.";
    default:
      return `Setup failed (HTTP ${String(status)})${detail ? ": " + detail : ""}. Try again.`;
  }
}

/**
 * Checks the form values and returns the request body, or throws a SetupError naming the problem.
 * Surrounding spaces are trimmed (pasted tokens often carry some); an empty name is left out, so the
 * server picks "default".
 */
export function buildSetupRequest(token: string, name: string): Schemas["SetupRequest"] {
  const t = token.trim();
  if (!t) throw new SetupError(0, "Enter the setup token.");
  const n = name.trim();
  if (new TextEncoder().encode(n).length > MAX_TENANT_NAME) {
    throw new SetupError(0, `The tenant name must be at most ${String(MAX_TENANT_NAME)} bytes.`);
  }
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(n)) {
    throw new SetupError(0, "The tenant name must not contain control characters.");
  }
  const body: Schemas["SetupRequest"] = { setup_token: t };
  if (n) body.tenant_name = n;
  return body;
}

/** Accepts what the server returns as a tenant key, nothing else. */
export function isApiKey(v: unknown): v is string {
  return typeof v === "string" && /^ohs_sk_[A-Za-z0-9_-]{16,}$/.test(v);
}

/** Reads GET /v1/setup: "needed", "done", or "off" (404, the route is not mounted). Anything else throws. */
export function setupState(status: number, body: unknown): "needed" | "done" | "off" {
  if (status === 404) return "off";
  const needed = body !== null && typeof body === "object" ? (body as { needed?: unknown }).needed : undefined;
  if (status !== 200 || typeof needed !== "boolean") throw new SetupError(status, setupMessage(status, ""));
  return needed ? "needed" : "done";
}
