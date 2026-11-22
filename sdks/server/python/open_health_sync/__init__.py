# SPDX-License-Identifier: AGPL-3.0-only
"""The server-side Python SDK of Open Health Sync.

client.py and models.py are generated from api/openapi.yaml (make sdk); webhooks.py and
events.py are written by hand. Standard library only, Python 3.9 or later.
"""

from .client import OhsError, OpenHealthSync, paginate
from .events import STREAM_RESET_EVENT, StreamEvent, StreamReset, parse_sse, stream_events
from .webhooks import WEBHOOK_TOLERANCE_SECONDS, WebhookVerificationError, verify_webhook

__all__ = [
    "OhsError",
    "OpenHealthSync",
    "STREAM_RESET_EVENT",
    "StreamEvent",
    "StreamReset",
    "WEBHOOK_TOLERANCE_SECONDS",
    "WebhookVerificationError",
    "paginate",
    "parse_sse",
    "stream_events",
    "verify_webhook",
]
