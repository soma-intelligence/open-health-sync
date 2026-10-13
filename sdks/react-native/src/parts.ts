// SPDX-License-Identifier: AGPL-3.0-only

import { gzipSync, strToU8 } from "fflate";
import { isDay } from "./time.ts";

/** One upload part: one HealthKit type, one local day, gzip NDJSON. */
export interface UploadPart {
  /** HealthKit type identifier, sent as `X-OHS-Type`. */
  type: string;
  /** Local day, `YYYY-MM-DD`, sent as `X-OHS-Day`. */
  day: string;
  /** Batch id, sent as `X-OHS-Batch`. */
  batch: string;
  /** Part number within the batch, sent as `X-OHS-Part`. */
  partNumber: number;
  /** gzip of the NDJSON lines. */
  body: Uint8Array;
}

/** A built part, with counts that are handy for progress reporting. */
export interface BuiltPart extends UploadPart {
  /** Number of lines in the part. */
  lineCount: number;
  /** Size of the NDJSON before compression. */
  uncompressedBytes: number;
}

/** 8 MiB, the default upper bound of a part's NDJSON before compression. */
export const DEFAULT_MAX_PART_BYTES = 8 * 1024 * 1024;

const NAME = /^[A-Za-z0-9_-]{1,128}$/;
const MAX_PART_NUMBER = 1_000_000;

function checkName(value: string, what: string): void {
  if (typeof value !== "string" || !NAME.test(value)) {
    throw new RangeError(`Open Health Sync: invalid ${what}: use 1 to 128 letters, digits, _ or -`);
  }
}

function checkDay(day: string): void {
  if (typeof day !== "string" || !isDay(day)) {
    throw new RangeError("Open Health Sync: invalid day: use YYYY-MM-DD");
  }
}

interface Group {
  type: string;
  day: string;
  lines: Uint8Array[];
}

/**
 * Turns lines in the server's line format into upload parts, the way
 * `ohs import apple-health` does: one part per (type, day) in the order each pair first
 * appears, at most `maxPartBytes` of NDJSON per part (a single larger line goes alone),
 * part numbers from 0 across the whole batch, each body gzip compressed.
 */
export class AppleHealthPartBuilder {
  readonly batch: string;
  readonly maxPartBytes: number;
  private readonly groups = new Map<string, Group>();

  constructor(batch: string, maxPartBytes: number = DEFAULT_MAX_PART_BYTES) {
    checkName(batch, "batch");
    if (!Number.isInteger(maxPartBytes) || maxPartBytes < 1) {
      throw new RangeError("Open Health Sync: maxPartBytes must be a positive integer");
    }
    this.batch = batch;
    this.maxPartBytes = maxPartBytes;
  }

  /**
   * Adds one line. `type` is the HealthKit type identifier (for example
   * `HKQuantityTypeIdentifierHeartRate`), `day` the sample's local day and `line` a JSON
   * object already in the line format. Returns the builder.
   */
  add(type: string, day: string, line: { [key: string]: unknown }): this {
    checkName(type, "type");
    checkDay(day);
    if (line === null || typeof line !== "object" || Array.isArray(line)) {
      throw new TypeError("Open Health Sync: a line must be a JSON object");
    }
    const json = JSON.stringify(line);
    if (json === undefined) throw new TypeError("Open Health Sync: a line must be a JSON object");
    const key = `${type}\u0000${day}`;
    let g = this.groups.get(key);
    if (!g) {
      g = { type, day, lines: [] };
      this.groups.set(key, g);
    }
    g.lines.push(strToU8(json + "\n"));
    return this;
  }

  /** Number of lines added so far. */
  get lineCount(): number {
    let n = 0;
    for (const g of this.groups.values()) n += g.lines.length;
    return n;
  }

  /** Builds every part. Calling it again builds the same parts. */
  build(): BuiltPart[] {
    const out: BuiltPart[] = [];
    let partNumber = 0;
    const emit = (g: Group, lines: Uint8Array[], size: number): void => {
      if (partNumber > MAX_PART_NUMBER) {
        throw new RangeError("Open Health Sync: more than 1000001 parts in one batch");
      }
      const ndjson = new Uint8Array(size);
      let off = 0;
      for (const l of lines) {
        ndjson.set(l, off);
        off += l.length;
      }
      out.push({
        type: g.type,
        day: g.day,
        batch: this.batch,
        partNumber: partNumber++,
        body: gzipSync(ndjson),
        lineCount: lines.length,
        uncompressedBytes: size,
      });
    };
    for (const g of this.groups.values()) {
      let cur: Uint8Array[] = [];
      let size = 0;
      for (const l of g.lines) {
        if (cur.length > 0 && size + l.length > this.maxPartBytes) {
          emit(g, cur, size);
          cur = [];
          size = 0;
        }
        cur.push(l);
        size += l.length;
      }
      if (cur.length > 0) emit(g, cur, size);
    }
    return out;
  }
}

const ALPHABET = "abcdefghijklmnopqrstuvwxyz0123456789";

/**
 * A fresh batch id: `prefix`, a dash, the current UTC time and 10 random characters, for
 * example `app-20260110T143000Z-k3x9q0w2ab`. Uses `crypto.getRandomValues` when present
 * (it does not need to be unguessable, only unique per run).
 */
export function newBatchId(prefix: string = "rn"): string {
  checkName(prefix, "batch prefix");
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d+/, "");
  const bytes = new Uint8Array(10);
  const c = (globalThis as { crypto?: { getRandomValues?: (a: Uint8Array) => Uint8Array } }).crypto;
  if (c && typeof c.getRandomValues === "function") {
    c.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
  }
  let rand = "";
  for (const b of bytes) rand += ALPHABET[b % ALPHABET.length];
  const id = `${prefix}-${stamp}-${rand}`;
  // Keep within 128 characters when the prefix is long.
  return id.length <= 128 ? id : `${prefix.slice(0, 128 - stamp.length - rand.length - 2)}-${stamp}-${rand}`;
}
