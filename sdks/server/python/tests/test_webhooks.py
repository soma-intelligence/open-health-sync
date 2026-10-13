# SPDX-License-Identifier: AGPL-3.0-only
"""verify_webhook against the vectors the server's Go code signs (sdks/server/testdata)."""

import base64
import json
import pathlib
import unittest

from open_health_sync import WebhookVerificationError, verify_webhook

VECTORS = json.loads(
    (pathlib.Path(__file__).resolve().parents[2] / "testdata" / "webhook-vectors.json").read_text("utf-8")
)["vectors"]


def _secret(key_text):
    return "whsec_" + base64.b64encode(key_text.encode("utf-8")).decode("ascii")


def _headers(v):
    return {"webhook-id": v["id"], "webhook-timestamp": v["timestamp"], "webhook-signature": v["signature"]}


class VectorsTest(unittest.TestCase):
    def test_vectors(self):
        for v in VECTORS:
            with self.subTest(v["name"]):
                if v["valid"]:
                    event = verify_webhook(_secret(v["key_text"]), _headers(v), v["body"].encode(), now=v["now"])
                    self.assertEqual(event["id"], json.loads(v["body"])["id"])
                else:
                    with self.assertRaises(WebhookVerificationError):
                        verify_webhook(_secret(v["key_text"]), _headers(v), v["body"].encode(), now=v["now"])

    def test_header_names_in_any_case_and_text_body(self):
        v = next(x for x in VECTORS if x["valid"])
        headers = {k.title(): val for k, val in _headers(v).items()}
        event = verify_webhook(_secret(v["key_text"]), headers, v["body"], now=v["now"])
        self.assertEqual(event["type"], "data.written")

    def test_malformed_secret(self):
        v = VECTORS[0]
        with self.assertRaises(WebhookVerificationError):
            verify_webhook("not-a-secret", _headers(v), v["body"], now=v["now"])


if __name__ == "__main__":
    unittest.main()
