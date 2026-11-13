# SPDX-License-Identifier: AGPL-3.0-only
"""The event stream of Open Health Sync, as Server-Sent Events.

``GET /v1/events/stream`` (or ``GET /v1/users/{id}/events/stream``) carries the events webhooks
carry, as they happen, with reconnection and ``Last-Event-ID`` resume. Written by hand (not
generated): the generated client leaves the stream routes out. Standard library only.
"""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import IO, Any, Callable, Dict, Iterable, Iterator, List, NamedTuple, Optional, Union

from .client import OhsError
from .models import WebhookEvent

__all__ = ["STREAM_RESET_EVENT", "SSEBlock", "StreamEvent", "StreamReset", "parse_sse", "stream_events"]

#: The event the server sends when it could not resume where the client left off.
STREAM_RESET_EVENT = "stream.reset"


class SSEBlock(NamedTuple):
    """One block of a Server-Sent Events stream."""

    id: Optional[str]
    event: Optional[str]
    data: Optional[str]
    retry: Optional[int]


class StreamEvent(NamedTuple):
    """One event: ``id`` is the event id (deduplicate on it), ``event`` the webhook body."""

    id: str
    type: str
    event: WebhookEvent


class StreamReset(NamedTuple):
    """The stream could not resume: re-read what you need, then go on from here."""

    reason: str
    window_hours: int


def parse_sse(lines: Iterable[Union[str, bytes]]) -> Iterator[SSEBlock]:
    """Parses Server-Sent Events from lines (with or without their ending); comments are skipped."""
    ident: Optional[str] = None
    event: Optional[str] = None
    retry: Optional[int] = None
    data: List[str] = []
    seen = False
    for raw in lines:
        line = raw.decode("utf-8", errors="replace") if isinstance(raw, bytes) else raw
        line = line.rstrip("\r\n")
        if line == "":
            if seen:
                yield SSEBlock(ident, event, "\n".join(data) if data else None, retry)
            ident = event = None
            retry = None
            data = []
            seen = False
            continue
        if line.startswith(":"):
            continue
        name, _, value = line.partition(":")
        if value.startswith(" "):
            value = value[1:]
        seen = True
        if name == "id":
            ident = value
        elif name == "event":
            event = value
        elif name == "data":
            data.append(value)
        elif name == "retry" and value.isdigit():
            retry = int(value)


def stream_events(
    base_url: str,
    api_key: str,
    *,
    user: Optional[str] = None,
    user_id: Optional[str] = None,
    types: Optional[Iterable[str]] = None,
    last_event_id: Optional[str] = None,
    reconnect: bool = True,
    timeout: float = 60.0,
    opener: Optional[Callable[..., IO[bytes]]] = None,
) -> Iterator[Union[StreamEvent, StreamReset]]:
    """Yields the events of the tenant, or of one ``user``, as they happen.

    ::

        for m in stream_events("https://health.example.com", key, types=["data.written"]):
            if isinstance(m, StreamEvent):
                refresh(m.event["data"]["user_id"])

    api_key is a tenant API key, or the user's token (scope read) together with ``user``.
    ``user_id`` filters the tenant stream. The stream reconnects with Last-Event-ID when it
    ends, unless ``reconnect`` is False. A 503 is retried after Retry-After; any other refusal
    raises OhsError. ``timeout`` (seconds) bounds the wait for a line: the server sends a
    heartbeat every 15 seconds. ``opener`` replaces urllib.request.urlopen (tests).
    """
    if not base_url:
        raise ValueError("base_url is required")
    if not api_key:
        raise ValueError("api_key is required")
    open_url = opener or urllib.request.urlopen
    base = base_url.rstrip("/")
    path = f"/v1/users/{urllib.parse.quote(user, safe='')}/events/stream" if user else "/v1/events/stream"
    params: Dict[str, str] = {}
    if user_id and not user:
        params["user_id"] = user_id
    type_list = list(types or [])
    if type_list:
        params["types"] = ",".join(type_list)
    url = base + path + ("?" + urllib.parse.urlencode(params) if params else "")
    retry = 3.0
    while True:
        headers = {"Authorization": f"Bearer {api_key}", "Accept": "text/event-stream"}
        if last_event_id:
            headers["Last-Event-ID"] = last_event_id
        request = urllib.request.Request(url, headers=headers, method="GET")
        try:
            response = open_url(request, timeout=timeout)
        except urllib.error.HTTPError as err:
            if err.code == 503 and reconnect:
                wait = err.headers.get("Retry-After") or ""
                err.close()
                time.sleep(float(wait) if wait.isdigit() else retry)
                continue
            raise OhsError.from_http_error(err) from None
        except OSError:
            if not reconnect:
                raise
            time.sleep(retry)
            continue
        try:
            with response:
                for block in parse_sse(response):
                    if block.retry is not None:
                        retry = block.retry / 1000
                    if block.event == STREAM_RESET_EVENT:
                        reset: Dict[str, Any] = json.loads(block.data or "{}")
                        yield StreamReset(str(reset.get("reason", "")), int(reset.get("window_hours", 0)))
                        continue
                    if block.data is None or not block.id:
                        continue
                    last_event_id = block.id
                    event = json.loads(block.data)
                    yield StreamEvent(block.id, event.get("type", ""), event)
        except OSError:
            if not reconnect:
                raise
        if not reconnect:
            return
        time.sleep(retry)
