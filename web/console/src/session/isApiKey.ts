// Moves with the setup page in Task 16.

/** True for what the server returns as a tenant API key. */
export function isApiKey(v: unknown): v is string {
  return typeof v === "string" && /^ohs_sk_[A-Za-z0-9_-]{16,}$/.test(v);
}
