import { ApiError } from "../api/client";

/** The message to show when a sign out did not go through. */
export function signOutFailure(e: unknown): string {
  return e instanceof ApiError ? e.detail || e.message : "Sign out failed: the server could not be reached.";
}
