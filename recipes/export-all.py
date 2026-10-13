#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Download a copy of all your health data, every app in one place, as CSV or Parquet.

    python3 export-all.py                     # CSV, into health-export-<date>/
    python3 export-all.py --format parquet    # for pandas, Polars, DuckDB, Spark

Open the CSV files in any spreadsheet. Needs Python 3.9+ and nothing else.
Recipe page: docs/recipes/export-all.md
"""

import argparse
from datetime import date
import json
import os
import shutil
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
    # Every source's rows: the recipe picks or compares the apps itself.
    params.setdefault("view", "all")
    cursor = None
    while True:
        page = call("GET", f"/v1/users/{USER}/{resource}", limit=1000, cursor=cursor, **params)
        yield from page["data"]
        cursor = page.get("next_cursor")
        if not cursor:
            return


DATASETS = ["daily", "sleep", "workouts", "samples", "category-samples", "nutrition"]


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--format", choices=["csv", "parquet"], default="csv")
    p.add_argument("--since", help="keep records from this day on (YYYY-MM-DD); default everything")
    p.add_argument("--dir", default=f"health-export-{date.today()}")
    a = p.parse_args()
    os.makedirs(a.dir, exist_ok=True)
    for ds in DATASETS:
        path = os.path.join(a.dir, f"{ds}.{a.format}")
        with call("GET", f"/v1/users/{USER}/exports/{ds}", raw=True, format=a.format, start=a.since, view="all") as r, \
                open(path, "wb") as f:
            shutil.copyfileobj(r, f)
        print(f"{path:<48}{os.path.getsize(path):>12,} bytes")
    if a.format == "parquet":
        print(f"\nimport pandas as pd; daily = pd.read_parquet('{a.dir}/daily.parquet')")


if __name__ == "__main__":
    main()
