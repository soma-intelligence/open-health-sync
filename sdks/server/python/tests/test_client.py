# SPDX-License-Identifier: AGPL-3.0-only
"""The generated client against a local HTTP server that records each request and answers
what the test queued."""

import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

from open_health_sync import OhsError, OpenHealthSync

USER_ID = "3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11"


class _Handler(BaseHTTPRequestHandler):
    def _serve(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        self.server.seen.append(
            {"method": self.command, "path": self.path, "headers": dict(self.headers), "body": body}
        )
        status, headers, payload = (
            self.server.answers.pop(0) if self.server.answers else (500, {}, b"no answer queued")
        )
        self.send_response(status)
        for name, value in headers.items():
            self.send_header(name, value)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    do_GET = do_POST = do_PATCH = do_DELETE = _serve

    def log_message(self, format, *args):  # noqa: A002 - quiet test output
        pass


def _json(status, value, headers=None):
    return status, {"Content-Type": "application/json", **(headers or {})}, json.dumps(value).encode()


class ClientTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        cls.server.seen = []
        cls.server.answers = []
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base_url = "http://127.0.0.1:%d/" % cls.server.server_address[1]

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def setUp(self):
        self.server.seen.clear()
        self.server.answers.clear()
        self.client = OpenHealthSync(self.base_url, "ohs_sk_EXAMPLE")

    def seen(self, i=0):
        req = self.server.seen[i]
        url = urlsplit(req["path"])
        headers = {k.lower(): v for k, v in req["headers"].items()}
        return req["method"], url.path, parse_qs(url.query), headers, req["body"]

    def test_bearer_and_json(self):
        self.server.answers.append(_json(200, {"data": [{"slug": "withings", "kinds": ["oauth"]}]}))
        providers = self.client.list_providers()
        self.assertEqual(providers["data"][0]["slug"], "withings")
        method, path, _, headers, _ = self.seen()
        self.assertEqual((method, path), ("GET", "/v1/providers"))
        self.assertEqual(headers["authorization"], "Bearer ohs_sk_EXAMPLE")
        self.assertEqual(headers["accept"], "application/json")

    def test_json_body_and_idempotency_key(self):
        user = {"id": USER_ID, "external_ref": "customer-42", "status": "active", "created_at": "2026-01-01T00:00:00Z"}
        self.server.answers.append(_json(201, user))
        created = self.client.create_user({"external_ref": "customer-42"}, idempotency_key="k-1")
        self.assertEqual(created, user)
        method, _, _, headers, body = self.seen()
        self.assertEqual(method, "POST")
        self.assertEqual(headers["content-type"], "application/json")
        self.assertEqual(headers["idempotency-key"], "k-1")
        self.assertEqual(json.loads(body), {"external_ref": "customer-42"})

    def test_optional_body_left_out(self):
        self.server.answers.append(_json(200, {"authorize_url": "https://provider.example/consent"}))
        res = self.client.start_connection(USER_ID, "withings")
        self.assertEqual(res["authorize_url"], "https://provider.example/consent")
        _, path, _, headers, body = self.seen()
        self.assertEqual(path, "/v1/users/%s/connections/withings" % USER_ID)
        self.assertEqual(body, b"")
        self.assertNotIn("content-type", headers)

    def test_path_encoding_and_query(self):
        self.server.answers.append(_json(200, {"data": [], "next_cursor": None}))
        self.client.list_samples("a/b", type="heart_rate", start="2026-01-01", limit=10)
        _, path, query, _, _ = self.seen()
        self.assertEqual(path, "/v1/users/a%2Fb/samples")
        self.assertEqual(query, {"type": ["heart_rate"], "start": ["2026-01-01"], "limit": ["10"]})

    def test_problem_is_ohs_error(self):
        problem = {"type": "about:blank", "title": "Not Found", "status": 404, "detail": "no such user", "request_id": "req-123"}
        self.server.answers.append(
            (404, {"Content-Type": "application/problem+json", "X-Request-Id": "req-123"}, json.dumps(problem).encode())
        )
        with self.assertRaises(OhsError) as ctx:
            self.client.get_user(USER_ID)
        err = ctx.exception
        self.assertEqual((err.status, err.title, err.detail, err.request_id), (404, "Not Found", "no such user", "req-123"))
        self.assertEqual(str(err), "404 Not Found: no such user")

    def test_error_that_is_not_a_problem(self):
        self.server.answers.append((502, {"Content-Type": "text/plain"}, b"bad gateway"))
        with self.assertRaises(OhsError) as ctx:
            self.client.list_providers()
        self.assertEqual(ctx.exception.status, 502)
        self.assertEqual(ctx.exception.body, "bad gateway")

    def test_no_content(self):
        self.server.answers.append((204, {}, b""))
        self.assertIsNone(self.client.delete_webhook("w1"))
        self.assertEqual(self.seen()[0], "DELETE")

    def test_iterator_follows_next_cursor(self):
        self.server.answers.append(_json(200, {"data": [{"id": 1}, {"id": 2}], "next_cursor": "c2"}))
        self.server.answers.append(_json(200, {"data": [{"id": 3}], "next_cursor": None}))
        ids = [d["id"] for d in self.client.iter_daily(USER_ID, start="2026-01-01", limit=2)]
        self.assertEqual(ids, [1, 2, 3])
        self.assertNotIn("cursor", self.seen(0)[2])
        self.assertEqual(self.seen(1)[2]["cursor"], ["c2"])
        self.assertEqual(self.seen(1)[2]["start"], ["2026-01-01"])

    def test_export_returns_bytes(self):
        self.server.answers.append((200, {"Content-Type": "text/csv"}, b"id,type\n1,heart_rate\n"))
        data = self.client.export_dataset(USER_ID, "samples", format="csv")
        self.assertEqual(data, b"id,type\n1,heart_rate\n")
        self.assertEqual(self.seen()[1], "/v1/users/%s/exports/samples" % USER_ID)

    def test_upload_headers_and_bytes(self):
        self.server.answers.append(_json(200, {"key": "raw/key", "duplicate": False}))
        res = self.client.ingest_apple_health(
            b"\x1f\x8b\x08",
            ohs_type="HKQuantityTypeIdentifierHeartRate",
            ohs_day="2026-01-01",
            ohs_batch="b1",
            ohs_part=0,
        )
        self.assertFalse(res["duplicate"])
        _, _, _, headers, body = self.seen()
        self.assertEqual(headers["content-type"], "application/gzip")
        self.assertEqual(headers["x-ohs-part"], "0")
        self.assertEqual(body, b"\x1f\x8b\x08")


if __name__ == "__main__":
    unittest.main()
