#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""The same figure from every app that measures it, day by day, and how far apart they are.

    python3 compare-sources.py                    # steps, last 14 days
    python3 compare-sources.py --metric sleep     # also: resting_hr, hrv, steps
    python3 compare-sources.py --metric resting_hr --days 30

Wear a ring and a watch, or log on a phone and a band, and they never agree.
Needs Python 3.9+ and nothing else. Recipe page: docs/recipes/compare-sources.md
"""

import argparse
from collections import defaultdict
from datetime import date, datetime, timedelta, timezone
from itertools import combinations
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


METRICS = {  # name: (daily summary field, unit, format)
    "steps": ("step_count", "steps", "{:.0f}"),
    "resting_hr": ("resting_hr", "bpm", "{:.1f}"),
    "hrv": ("hrv_avg_ms", "ms", "{:.0f}"),
    "sleep": (None, "hours", "{:.2f}"),
}


def values(metric, start):
    """{app: {date: value}} for one metric."""
    field = METRICS[metric][0]
    if not field:
        return nights_by_app(start)
    out = defaultdict(dict)
    for r in records("daily", start=start.isoformat()):
        if r.get(field) is not None:
            out[r["provider"]][date.fromisoformat(r["date"])] = float(r[field])
    if metric == "resting_hr":  # Apple Health sends it as samples, not in a daily summary
        by_day = defaultdict(list)
        for s in records("samples", type="resting_heart_rate", start=start.isoformat()):
            if s["provider"] not in out:
                by_day[(s["provider"], date.fromisoformat(s["start_date"][:10]))].append(s["value"])
        for (app, d), vs in by_day.items():
            out[app][d] = sum(vs) / len(vs)
    return out


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--metric", choices=sorted(METRICS), default="steps")
    p.add_argument("--days", type=int, default=14, help="how many days (default 14)")
    a = p.parse_args()
    start = date.today() - timedelta(days=a.days)
    data = values(a.metric, start)
    apps = sorted(data)
    if not apps:
        sys.exit(f"no {a.metric} since {start}")
    _, unit, fmt = METRICS[a.metric]
    days = sorted({d for v in data.values() for d in v if d >= start})

    print(f"{a.metric} ({unit}), {len(apps)} source{'s' * (len(apps) > 1)}\n")
    print(f"{'Day':<12}" + "".join(f"{x:>16}" for x in apps))
    for d in days:
        cells = [fmt.format(data[x][d]) if d in data[x] else "-" for x in apps]
        print(f"{d!s:<12}" + "".join(f"{c:>16}" for c in cells))

    if len(apps) < 2:
        print("\nOnly one source measures this: connect a second one to compare.")
        return
    print()
    for x, y in combinations(apps, 2):
        both = [d for d in days if d in data[x] and d in data[y]]
        if not both:
            print(f"{x} and {y}: no day measured by both")
            continue
        gaps = [abs(data[x][d] - data[y][d]) for d in both]
        mean = sum((data[x][d] + data[y][d]) / 2 for d in both) / len(both)
        bias = sum(data[x][d] - data[y][d] for d in both) / len(both)
        share = f" ({sum(gaps) / len(gaps) / mean:.0%})" if mean else ""
        print(f"{x} vs {y}: {len(both)} days, typical gap {fmt.format(sum(gaps) / len(gaps))} {unit}"
              f"{share}, {x} reads {fmt.format(abs(bias))} {unit}"
              f" {'higher' if bias >= 0 else 'lower'} on average")


if __name__ == "__main__":
    main()
