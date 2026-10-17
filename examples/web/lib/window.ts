// SPDX-License-Identifier: AGPL-3.0-only

import { addDays } from "./synthetic.ts";

/** The window the read-back pages ask for: the last 30 days, today included (end is exclusive). */
export function readWindow(now: Date = new Date()): { start: string; end: string } {
  const today = now.toISOString().slice(0, 10);
  return { start: addDays(today, -30), end: addDays(today, 1) };
}
