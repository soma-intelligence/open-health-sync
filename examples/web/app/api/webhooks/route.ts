// SPDX-License-Identifier: AGPL-3.0-only
//
// POST /api/webhooks: the endpoint to register with POST /v1/webhooks (see the README).

import { receiveWebhook, webhookSecret } from "../../../lib/webhooks.ts";

export async function POST(request: Request): Promise<Response> {
  return receiveWebhook(request, { secret: webhookSecret()?.secret });
}
