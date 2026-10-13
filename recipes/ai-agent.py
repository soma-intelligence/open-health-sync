#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Let an AI assistant read your health data, read only, through MCP.

    python3 ai-agent.py              # a token valid 30 days, and the setup lines

Then ask it things like "how did I sleep the weeks I trained the most?". The
token can read, never write or delete, and stops working after 30 days.
Needs Python 3.9+ and nothing else. Recipe page: docs/recipes/ai-agent.md
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


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
    p.add_argument("--days", type=int, default=30, help="token lifetime in days, at most 30")
    a = p.parse_args()
    t = call("POST", f"/v1/users/{USER}/tokens", body={"scopes": ["read"], "ttl_seconds": a.days * 86400})
    token = t["token"]
    print(f"Read only token, valid until {t['expires_at'][:10]}. Keep it private.\n")
    print("Claude Code:\n")
    print(f"  claude mcp add --transport http open-health-sync {URL}/mcp \\\n"
          f"    --header \"Authorization: Bearer {token}\"\n")
    print("Claude Desktop, or any client that starts a local command (claude_desktop_config.json):\n")
    print(json.dumps({"mcpServers": {"open-health-sync": {
        "command": "ohs", "args": ["mcp"],
        "env": {"OHS_MCP_URL": URL, "OHS_MCP_KEY": token}}}}, indent=2))
    print("\nOther clients: docs/mcp.md")


if __name__ == "__main__":
    main()
