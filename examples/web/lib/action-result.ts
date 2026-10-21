// SPDX-License-Identifier: AGPL-3.0-only

/** What a Server Action of this app answers to its form. Plain data only: it goes to the browser. */
export interface ActionResult {
  ok: boolean;
  message: string;
  /** Label and value pairs shown under the message. */
  facts?: [string, string][];
  /** A webhook signing secret, shown once after it is created. */
  secret?: string;
}
