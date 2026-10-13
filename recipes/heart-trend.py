#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Resting heart rate and HRV week by week, and how this week compares.

    python3 heart-trend.py              # the last 8 weeks
    python3 heart-trend.py --weeks 26

A rising resting heart rate with a falling HRV often goes with poor sleep,
stress, a hard training block or a cold coming. This is a trend, not a
diagnosis. Needs Python 3.9+ and nothing else. Recipe page: docs/recipes/heart-trend.md
"""

import argparse
from collections import Counter, defaultdict
from datetime import date, timedelta
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
    # Every source's rows: the recipe picks or compares the apps itself.
    params.setdefault("view", "all")
    cursor = None
    while True:
        page = call("GET", f"/v1/users/{USER}/{resource}", limit=1000, cursor=cursor, **params)
        yield from page["data"]
        cursor = page.get("next_cursor")
        if not cursor:
            return


def daily(field, start, end=None):
    """One value of a daily figure per date ({date: value}), and the app it came from.

    Several apps can report the same figure for the same day. This keeps the
    app that reports it on the most days, so a series never mixes two devices;
    compare-sources.py shows them side by side.
    """
    rows = [r for r in records("daily", start=start.isoformat(), end=end and end.isoformat())
            if r.get(field) is not None]
    if not rows:
        return {}, None
    app = Counter(r["provider"] for r in rows).most_common(1)[0][0]
    return {date.fromisoformat(r["date"]): r[field] for r in rows if r["provider"] == app}, app


def sample_means(kind, start, end=None):
    """Daily mean of a sample type ({date: value}) and its app, for apps with no daily summary."""
    by_app = defaultdict(lambda: defaultdict(list))
    for s in records("samples", type=kind, start=start.isoformat(), end=end and end.isoformat()):
        by_app[s["provider"]][date.fromisoformat(s["start_date"][:10])].append(s["value"])
    if not by_app:
        return {}, None
    app = max(by_app, key=lambda a: len(by_app[a]))
    return {d: sum(v) / len(v) for d, v in by_app[app].items()}, app


def weekly(values, first, weeks):
    """Mean per 7 day window from first, oldest first; None for a window with no value."""
    out = []
    for w in range(weeks):
        start = first + timedelta(weeks=w)
        vs = [v for d, v in values.items() if start <= d < start + timedelta(days=7)]
        out.append(sum(vs) / len(vs) if vs else None)
    return out


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--weeks", type=int, default=8, help="how many weeks (default 8)")
    weeks = max(p.parse_args().weeks, 2)
    # Windows of 7 days that end today, so the last one is always "this week".
    first = date.today() - timedelta(days=7 * weeks - 1)

    rhr, rhr_app = daily("resting_hr", first)
    if not rhr:  # Apple Health sends resting heart rate as samples, not as a daily summary
        rhr, rhr_app = sample_means("resting_heart_rate", first)
    hrv, hrv_app = daily("hrv_avg_ms", first)
    if not hrv:
        hrv, hrv_app = sample_means("hrv_sdnn", first)
        hrv_app = hrv_app and hrv_app + " (SDNN)"
    if not rhr and not hrv:
        sys.exit(f"no resting heart rate or HRV since {first}: connect a watch, a ring or a band first")

    r, h = weekly(rhr, first, weeks), weekly(hrv, first, weeks)
    fmt = lambda v, unit: f"{v:.1f} {unit}" if v is not None else "-"
    print(f"{'7 days to':<12}{'Resting HR':>14}{'HRV':>12}")
    for w in range(weeks):
        last = first + timedelta(weeks=w, days=6)
        print(f"{last:%a %d %b}  {fmt(r[w], 'bpm'):>14}{fmt(h[w], 'ms'):>12}")
    print(f"\nSources: resting HR from {rhr_app or '-'}, HRV from {hrv_app or '-'}")

    def change(series, unit, pct):
        before = [v for v in series[-5:-1] if v is not None]
        if series[-1] is None or not before:
            return None
        base = sum(before) / len(before)
        d = series[-1] - base
        return f"{d / base:+.0%}" if pct else f"{d:+.1f} {unit}"

    parts = [f"{name} {c}" for name, c in (("resting HR", change(r, "bpm", False)), ("HRV", change(h, "ms", True))) if c]
    if parts:
        print(f"Last 7 days against the {min(4, weeks - 1)} weeks before: {', '.join(parts)}")


if __name__ == "__main__":
    main()
