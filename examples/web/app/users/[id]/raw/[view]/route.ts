// SPDX-License-Identifier: AGPL-3.0-only
//
// GET /users/{id}/raw/{view}: one full API response, as the server sent it, for the links of
// the read-back page. The browser cannot call Open Health Sync itself (it never holds the
// tenant key), so this handler makes the call on the server and passes the JSON through.

import { OhsError } from "@open-health-sync/server";
import { tenantClient } from "../../../../../lib/ohs.ts";
import { readWindow } from "../../../../../lib/window.ts";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export async function GET(_: Request, context: { params: Promise<{ id: string; view: string }> }): Promise<Response> {
  const { id, view } = await context.params;
  if (!UUID.test(id)) return Response.json({ error: "not a user id" }, { status: 404 });
  const ohs = tenantClient();
  const range = readWindow();
  const calls: Record<string, () => Promise<unknown>> = {
    // GET /v1/users/{id}/samples?type=resting_heart_rate
    samples: () => ohs.listSamples(id, { type: "resting_heart_rate", ...range }),
    // GET /v1/users/{id}/daily
    daily: () => ohs.listDaily(id, range),
    // GET /v1/users/{id}/sleep
    sleep: () => ohs.listSleep(id, range),
    // GET /v1/users/{id}/fhir/Observation: a FHIR R4 searchset Bundle, paged by its next link
    fhir: () => ohs.searchFHIRObservations(id, { ...range, limit: 50 }),
    // GET /v1/users/{id}/omh/data-points
    omh: () => ohs.listOMHDataPoints(id, { ...range, limit: 50 }),
  };
  const call = Object.hasOwn(calls, view) ? calls[view] : undefined;
  if (!call) return Response.json({ error: `unknown view, one of ${Object.keys(calls).join(", ")}` }, { status: 404 });
  try {
    const body = await call();
    return new Response(JSON.stringify(body, null, 2), {
      headers: {
        "content-type": view === "fhir" ? "application/fhir+json; charset=utf-8" : "application/json; charset=utf-8",
        "cache-control": "no-store",
      },
    });
  } catch (err) {
    if (err instanceof OhsError) return Response.json(err.body, { status: err.status });
    throw err;
  }
}
