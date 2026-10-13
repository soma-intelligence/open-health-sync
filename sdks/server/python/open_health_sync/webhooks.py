# SPDX-License-Identifier: AGPL-3.0-only
"""Verification of the webhooks Open Health Sync sends, per Standard Webhooks.

webhook-signature is ``v1,`` + base64 HMAC-SHA256 of ``{webhook-id}.{webhook-timestamp}.{body}``,
keyed by the base64-decoded secret after ``whsec_``. Written by hand (not generated) and
tested against vectors signed by the server's own code (sdks/server/testdata).
"""

from __future__ import annotations

import base64
import binascii
import hashlib
import hmac
import json
import re
import time
from typing import Mapping, Optional, Union

from .models import WebhookEvent

__all__ = ["WEBHOOK_TOLERANCE_SECONDS", "WebhookVerificationError", "verify_webhook"]

#: How far webhook-timestamp may be from the local clock, in seconds.
WEBHOOK_TOLERANCE_SECONDS = 300

_SECRET_PREFIX = "whsec_"
_TIMESTAMP = re.compile(r"-?[0-9]+")


class WebhookVerificationError(Exception):
    """A webhook request that must be refused."""


def _secret_key(secret: str) -> bytes:
    if not secret.startswith(_SECRET_PREFIX):
        raise WebhookVerificationError("malformed secret: expected whsec_ followed by base64")
    try:
        key = base64.b64decode(secret[len(_SECRET_PREFIX) :], validate=True)
    except (binascii.Error, ValueError):
        raise WebhookVerificationError("malformed secret: expected whsec_ followed by base64") from None
    if not key:
        raise WebhookVerificationError("malformed secret: empty key")
    return key


def verify_webhook(
    secret: str,
    headers: Mapping[str, str],
    body: Union[bytes, str],
    *,
    now: Optional[float] = None,
    tolerance: int = WEBHOOK_TOLERANCE_SECONDS,
) -> WebhookEvent:
    """Checks a webhook request and returns its event.

    Pass the raw body, exactly as received: a body parsed and serialized again does not
    verify. headers is any mapping of the request headers (names in any case). now is a
    Unix time, the current time by default. Raises WebhookVerificationError when a header
    is missing, the timestamp is more than five minutes off, or no v1 signature matches.
    Deduplicate on the event id (the webhook-id header): a retry repeats it.
    """
    key = _secret_key(secret)
    lower = {name.lower(): value for name, value in headers.items()}
    msg_id = lower.get("webhook-id")
    timestamp = lower.get("webhook-timestamp")
    signatures = lower.get("webhook-signature")
    if not msg_id or not timestamp or not signatures:
        raise WebhookVerificationError("missing webhook-id, webhook-timestamp or webhook-signature")
    if not _TIMESTAMP.fullmatch(timestamp):
        raise WebhookVerificationError("webhook-timestamp is not a number of seconds")
    current = int(time.time() if now is None else now)
    if abs(current - int(timestamp)) > tolerance:
        raise WebhookVerificationError("webhook-timestamp is too far from now")

    raw = body.encode("utf-8") if isinstance(body, str) else bytes(body)
    expected = hmac.new(key, f"{msg_id}.{timestamp}.".encode("utf-8") + raw, hashlib.sha256).digest()
    for signature in signatures.split():
        version, _, value = signature.partition(",")
        if version != "v1" or not value:
            continue
        try:
            given = base64.b64decode(value, validate=True)
        except (binascii.Error, ValueError):
            continue
        if hmac.compare_digest(given, expected):
            event: WebhookEvent = json.loads(raw)
            return event
    raise WebhookVerificationError("no matching v1 signature")
