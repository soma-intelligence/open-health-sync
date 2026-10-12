#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Connect your accounts (Oura, WHOOP, Fitbit, Withings, ...) and see how fresh each one is.

    python3 connect.py              # what is connected, and what can be
    python3 connect.py oura         # opens the Oura sign in page

Each provider needs its keys set on the server first: see docs/connectors.
Apple Health has no sign in: import an export with `ohs import apple-health`.
Needs Python 3.9+ and nothing else. Recipe page: docs/recipes/connect.md
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
import webbrowser


# Connection: OHS_URL, OHS_KEY and OHS_USER from the environment, or from the
# file `ohs admin personal setup` writes (~/.ohs.env, or the path in OHS_ENV_FILE).
def settings():
    path = os.environ.get("OHS_ENV_FILE") or os.path.expanduser("~/.ohs.env")
    if os.path.isfile(path):
        with open(path, encoding="utf-8") as f:
            for line in f:
                name, sep, value = line.strip().partition("=")
                if sep and not name.startswith("#"):
                    os.environ.setdefault(name.strip(), value.strip())
    missing = [n for n in ("OHS_URL", "OHS_KEY", "OHS_USER") if not os.environ.get(n)]
    if missing:
        sys.exit(f"missing {', '.join(missing)}: run `ohs admin personal setup > ~/.ohs.env` first")
    return os.environ["OHS_URL"].rstrip("/"), os.environ["OHS_KEY"], os.environ["OHS_USER"]


URL, KEY, USER = settings()


def call(method, path, body=None, raw=False, **params):
    query = urllib.parse.urlencode({k: v for k, v in params.items() if v is not None})
    req = urllib.request.Request(
        URL + path + ("?" + query if query else ""),
        method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + KEY, "Content-Type": "application/json"},
    )
    try:
        resp = urllib.request.urlopen(req, timeout=120)
    except urllib.error.HTTPError as e:
        try:
            detail = json.load(e).get("detail", "")
        except ValueError:
            detail = ""
        sys.exit(f"{method} {path}: {e.code} {detail}".rstrip())
    except urllib.error.URLError as e:
        sys.exit(f"cannot reach {URL}: {e.reason}")
    if raw:
        return resp
    with resp:
        return json.load(resp)


def records(resource, **params):
    """Every record of one of the user's lists, page after page."""
    cursor = None
    while True:
        page = call("GET", f"/v1/users/{USER}/{resource}", limit=1000, cursor=cursor, **params)
        yield from page["data"]
        cursor = page.get("next_cursor")
        if not cursor:
            return


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("provider", nargs="?", help="slug of the provider to connect, as listed")
    a = p.parse_args()
    providers = call("GET", "/v1/providers")["data"]
    oauth = sorted(x["slug"] for x in providers if "oauth" in x["kinds"])

    if a.provider:
        if a.provider not in oauth:
            sys.exit(f"{a.provider} cannot be connected on this server; it can connect: {', '.join(oauth) or 'nothing yet'}")
        link = call("POST", f"/v1/users/{USER}/connections/{a.provider}", body={})["authorize_url"]
        print(f"Sign in to {a.provider} and allow access:\n\n  {link}\n")
        webbrowser.open(link)
        print("Data starts arriving a few seconds after you allow it. Run python3 connect.py to check.")
        return

    fresh = {f["provider"]: f for f in call("GET", f"/v1/users/{USER}/freshness")["data"]}
    if fresh:
        print(f"{'Source':<16}{'Status':<14}{'Synced through':<22}Last error")
        for slug, f in sorted(fresh.items()):
            through = (f.get("synced_through") or "-")[:16].replace("T", " ")
            print(f"{slug:<16}{f['state']:<14}{through:<22}{f.get('last_error') or ''}")
        print()
    todo = [s for s in oauth if s not in fresh]
    if todo:
        print("Can connect: " + ", ".join(todo) + "  (python3 connect.py <name>)")
    if not oauth:
        print("No provider keys are set on this server yet: see docs/connectors to add one.")


if __name__ == "__main__":
    main()
