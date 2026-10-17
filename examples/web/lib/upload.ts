// SPDX-License-Identifier: AGPL-3.0-only
//
// Uploads the synthetic data the way a phone app does: with a short-lived user token that can
// only upload, through POST /v1/ingest/apple-health, one gzip NDJSON part per call.

import { gzipSync } from "node:zlib";
import { OhsError, type OpenHealthSync } from "@open-health-sync/server";
import { tenantClient, userClient } from "./ohs.ts";
import {
  addDays,
  batchId,
  defaultEndDay,
  syntheticParts,
  toNdjson,
  type SyntheticOptions,
  type UploadPart,
} from "./synthetic.ts";

export const SAMPLE_DAYS = 14;
export const SAMPLE_SEED = 1;

export function sampleOptions(now: Date = new Date()): SyntheticOptions {
  return { endDay: defaultEndDay(now), days: SAMPLE_DAYS, seed: SAMPLE_SEED };
}

export interface UploadSummary {
  batch: string;
  firstDay: string;
  lastDay: string;
  parts: number;
  lines: number;
  stored: number;
  duplicates: number;
  /** Lines per HealthKit type. */
  types: Record<string, number>;
  /** When the upload token stops working. The token itself is never kept or shown. */
  tokenExpiresAt: string;
  /** Raw object key of the first part, as the server answered it. */
  firstKey: string;
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

// The SDK does not retry. 503 (too many uploads in flight) and 429 carry Retry-After, and
// re-sending a part is safe: the same bytes under the same key answer "duplicate".
async function uploadPart(device: OpenHealthSync, batch: string, part: UploadPart) {
  const body = gzipSync(toNdjson(part.lines));
  for (let attempt = 1; ; attempt++) {
    try {
      return await device.ingestAppleHealth(body, {
        ohsType: part.type,
        ohsDay: part.day,
        ohsBatch: batch,
        ohsPart: part.part,
      });
    } catch (err) {
      const retry = err instanceof OhsError && (err.status === 503 || err.status === 429) && attempt < 5;
      if (!retry) throw err;
      await sleep(1000 * attempt);
    }
  }
}

/** Runs fn over items, at most limit at a time, keeping the order of the results. */
async function eachLimited<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const results: R[] = new Array(items.length);
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const i = next++;
      results[i] = await fn(items[i] as T);
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}

export async function uploadSampleData(userId: string, options: SyntheticOptions = sampleOptions()): Promise<UploadSummary> {
  // 1. With the tenant key, mint a token for this user that can upload and nothing else.
  const minted = await tenantClient().mintUserToken(userId, { scopes: ["upload"], ttl_seconds: 300 });

  // 2. With that token only, upload the parts, as the user's phone would.
  const device = userClient(minted.token);
  const batch = batchId(options);
  const parts = syntheticParts(options);
  const answers = await eachLimited(parts, 4, (part) => uploadPart(device, batch, part));

  const types: Record<string, number> = {};
  for (const part of parts) types[part.type] = (types[part.type] ?? 0) + part.lines.length;
  const duplicates = answers.filter((answer) => answer.duplicate).length;
  return {
    batch,
    // The sampled days. A night that starts the evening before the first day is in a part of
    // that evening's day, so the parts reach one day earlier.
    firstDay: addDays(options.endDay, 1 - options.days),
    lastDay: options.endDay,
    parts: parts.length,
    lines: parts.reduce((sum, part) => sum + part.lines.length, 0),
    stored: answers.length - duplicates,
    duplicates,
    types,
    tokenExpiresAt: minted.expires_at,
    firstKey: answers[0]?.key ?? "",
  };
}
