// SPDX-License-Identifier: MIT

import type { Page } from "./models.ts";

/**
 * Walks every page of a list call and yields each record. `fetchPage` is called with
 * `undefined` first, then with each `nextCursor` until it is null.
 *
 * ```ts
 * for await (const s of paginate((cursor) => client.listSamples(userId, { type: "heart_rate", cursor }))) {
 *   // ...
 * }
 * ```
 */
export async function* paginate<T>(
  fetchPage: (cursor?: string) => Promise<Page<T>>,
): AsyncGenerator<T, void, undefined> {
  let cursor: string | undefined;
  for (;;) {
    const page = await fetchPage(cursor);
    for (const item of page.data) yield item;
    if (page.nextCursor === null || page.nextCursor === undefined) return;
    if (page.nextCursor === cursor) {
      throw new Error("Open Health Sync: the server returned the same cursor twice");
    }
    cursor = page.nextCursor;
  }
}

/** Collects every record of a list call into an array. Mind the memory for long ranges. */
export async function collectAll<T>(fetchPage: (cursor?: string) => Promise<Page<T>>): Promise<T[]> {
  const out: T[] = [];
  for await (const item of paginate(fetchPage)) out.push(item);
  return out;
}
