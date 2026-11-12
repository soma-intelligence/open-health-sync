// SPDX-License-Identifier: AGPL-3.0-only

import type { NativeHealthModule } from "../src/health/sync.ts";

/** One call the fake native module received. */
export interface NativeCall {
  method: string;
  args: unknown[];
}

/** What the fake answers, by method. A function is called with the arguments; an `Error` rejects. */
export type NativeReplies = Partial<Record<keyof NativeHealthModule, unknown>>;

/** A native rejection as React Native delivers it: an `Error` with a `code`. */
export function nativeError(code: string, message: string): Error {
  return Object.assign(new Error(message), { code });
}

/**
 * A fake of the native module: records every call, answers from `replies`, and lets a test
 * emit a background sync result.
 */
export function fakeNative(replies: NativeReplies = {}) {
  const calls: NativeCall[] = [];
  const handlers = new Set<(payload: { json: string }) => void>();

  function answer<T>(method: keyof NativeHealthModule, args: unknown[], fallback: T): Promise<T> {
    calls.push({ method, args });
    const reply = replies[method];
    if (reply instanceof Error) return Promise.reject(reply);
    if (typeof reply === "function") return Promise.resolve((reply as (...a: unknown[]) => T)(...args));
    return Promise.resolve(reply === undefined ? fallback : (reply as T));
  }

  const native: NativeHealthModule = {
    availability: () => answer("availability", [], "available"),
    configure: (...args) => answer("configure", args, undefined),
    clearCredentials: () => answer("clearCredentials", [], undefined),
    isConfigured: () => answer("isConfigured", [], true),
    requestAuthorization: (...args) => answer("requestAuthorization", args, "{}"),
    sync: (...args) => answer("sync", args, "{}"),
    flush: (...args) => answer("flush", args, "{}"),
    enableBackgroundSync: (...args) => answer("enableBackgroundSync", args, undefined),
    disableBackgroundSync: () => answer("disableBackgroundSync", [], undefined),
    pendingParts: () => answer("pendingParts", [], 0),
    lastBackgroundSync: () => answer("lastBackgroundSync", [], ""),
    onBackgroundSync(handler) {
      handlers.add(handler);
      return { remove: () => void handlers.delete(handler) };
    },
  };

  return {
    native,
    calls,
    /** Delivers a background sync result to every listener. */
    emit(json: string) {
      for (const h of [...handlers]) h({ json });
    },
    get listenerCount() {
      return handlers.size;
    },
  };
}
