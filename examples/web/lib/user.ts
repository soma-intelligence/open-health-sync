// SPDX-License-Identifier: AGPL-3.0-only

import { notFound } from "next/navigation";
import { OhsError, type User } from "@open-health-sync/server";
import { tenantClient } from "./ohs.ts";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** GET /v1/users/{id}, or the 404 page for an id that is not this tenant's user. */
export async function loadUser(id: string): Promise<User> {
  if (!UUID.test(id)) notFound();
  try {
    return await tenantClient().getUser(id);
  } catch (err) {
    if (err instanceof OhsError && err.status === 404) notFound();
    throw err;
  }
}
