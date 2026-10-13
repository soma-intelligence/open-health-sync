#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Your last nights of sleep in the terminal, whatever app or ring measured them.

    python3 sleep-report.py            # the last 14 nights
    python3 sleep-report.py --days 60

Needs Python 3.9+ and nothing else. Recipe page: docs/recipes/sleep-report.md
"""

import argparse
from collections import defaultdict
from datetime import date, datetime, timedelta, timezone
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from zoneinfo import ZoneInfo


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


def nights_by_app(start, end=None):
    """Hours slept per night, for each app: {app: {date: hours}}.

    A night is named by the morning it ends on: sleep that ends between 14:00
    the day before and 14:00 counts for that date, naps included, so a night
    split in two sessions is added up. A session without a sleep total counts
    its time in bed, less the time awake when the app gives it.
    """
    def utc(s):  # the API writes UTC; [:19] drops fractions Python 3.9 cannot parse
        return datetime.fromisoformat(s[:19]).replace(tzinfo=timezone.utc)

    by_app = defaultdict(lambda: defaultdict(float))
    for s in records("sleep", start=start.isoformat(), end=end and end.isoformat()):
        if not s["bedtime_end"]:
            continue
        t = utc(s["bedtime_end"])
        slept = s["total_sleep_seconds"]
        if slept is None:
            slept = (t - utc(s["bedtime_start"])).total_seconds() - (s["awake_seconds"] or 0)
        if slept <= 0:
            continue
        try:
            t = t.astimezone(ZoneInfo((s.get("metadata") or {}).get("timezone") or "UTC"))
        except (KeyError, ValueError):
            pass
        by_app[s["provider"]][(t + timedelta(hours=10)).date()] += slept / 3600
    return {app: dict(v) for app, v in by_app.items()}


def nights(start, end=None):
    """Hours slept per night ({date: hours}) from the app that reports the most nights."""
    by_app = nights_by_app(start, end)
    if not by_app:
        return {}, None
    app = max(by_app, key=lambda a: len(by_app[a]))
    return by_app[app], app


def hours(h):
    m = round(h * 60)
    return f"{m // 60}h{m % 60:02d}"


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--days", type=int, default=14, help="how many nights (default 14)")
    days = p.parse_args().days
    first = date.today() - timedelta(days=days - 1)
    slept, app = nights(first - timedelta(days=1))
    rows = sorted((d, v) for d, v in slept.items() if d >= first)
    if not rows:
        sys.exit(f"no sleep recorded since {first}: connect a ring, a watch or a phone first")

    print(f"Sleep, last {len(rows)} night{'s' * (len(rows) > 1)} ({app})\n")
    for d, v in rows:
        bar = "█" * round(v / 10 * 40)  # 40 characters is 10 hours
        print(f"{d:%a %d %b}  {hours(v):>5}  {bar}")
    values = [v for _, v in rows]
    short = min(rows, key=lambda r: r[1])
    long = max(rows, key=lambda r: r[1])
    print(f"\nAverage {hours(sum(values) / len(values))}, shortest {hours(short[1])} ({short[0]:%a %d %b}),"
          f" longest {hours(long[1])} ({long[0]:%a %d %b})")
    print(f"Nights under 7 hours: {sum(v < 7 for v in values)} of {len(values)}")
    print("Measured by more than one app? python3 compare-sources.py --metric sleep")


if __name__ == "__main__":
    main()
