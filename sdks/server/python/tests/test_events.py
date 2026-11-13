# SPDX-License-Identifier: AGPL-3.0-only
"""stream_events against a fake opener that answers Server-Sent Events."""

import io
import json
import unittest
import urllib.error
from email.message import Message

from open_health_sync import OhsError, StreamEvent, StreamReset, parse_sse, stream_events


def _payload(ident):
    return json.dumps({"id": ident, "type": "data.written", "timestamp": "2026-10-12T08:30:12.345Z", "data": {"user_id": "u"}})


class _Body(io.BytesIO):
    pass


class ParseTest(unittest.TestCase):
    def test_blocks(self):
        lines = io.BytesIO(b'retry: 3000\n\n: connected\n\nid: a\nevent: tag.created\ndata: {"x":\r\ndata: 1}\n\n: ping\n\n')
        self.assertEqual(
            [tuple(b) for b in parse_sse(lines)],
            [(None, None, None, 3000), ("a", "tag.created", '{"x":\n1}', None)],
        )


class StreamTest(unittest.TestCase):
    def test_events_reset_and_resume(self):
        calls = []
        bodies = [
            "retry: 1\n\nid: e1\nevent: data.written\ndata: " + _payload("e1") + "\n\n",
            'event: stream.reset\ndata: {"reason":"last_event_id_unknown","window_hours":24}\n\n'
            "id: e2\nevent: data.written\ndata: " + _payload("e2") + "\n\n",
        ]

        def opener(request, timeout):
            calls.append((request.full_url, request.get_header("Last-event-id"), timeout))
            if not bodies:
                raise urllib.error.HTTPError(request.full_url, 401, "Unauthorized", Message(), io.BytesIO(b"{}"))
            return _Body(bodies.pop(0).encode())

        got = []
        with self.assertRaises(OhsError) as caught:
            for m in stream_events("https://ohs.test/", "ohs_sk_x", user_id="u", types=["data.written", "tag.created"], opener=opener):
                got.append(m)
        self.assertEqual(caught.exception.status, 401)
        self.assertEqual(calls[0][0], "https://ohs.test/v1/events/stream?user_id=u&types=data.written%2Ctag.created")
        self.assertEqual([c[1] for c in calls], [None, "e1", "e2"])
        self.assertEqual(calls[0][2], 60.0)
        self.assertEqual(got[0].id, "e1")
        self.assertIsInstance(got[0], StreamEvent)
        self.assertEqual(got[0].event["data"]["user_id"], "u")
        self.assertEqual(got[1], StreamReset("last_event_id_unknown", 24))
        self.assertEqual(got[2].id, "e2")

    def test_one_user_without_reconnect(self):
        urls = []

        def opener(request, timeout):
            urls.append(request.full_url)
            return _Body(("id: e1\nevent: tag.created\ndata: " + _payload("e1") + "\n\n").encode())

        ids = [m.id for m in stream_events("https://ohs.test", "ohs_ut_x", user="a b", reconnect=False, opener=opener)]
        self.assertEqual(urls, ["https://ohs.test/v1/users/a%20b/events/stream"])
        self.assertEqual(ids, ["e1"])


if __name__ == "__main__":
    unittest.main()
