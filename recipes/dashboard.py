#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""A one page dashboard of your steps, sleep, resting heart rate and HRV.

    python3 dashboard.py                 # last 90 days, writes health-dashboard.html
    python3 dashboard.py --days 365 --out year.html

The page is a single HTML file with no script and nothing loaded from the
internet: open it in a browser, keep it, print it. It holds your health data,
so share it with care. Needs Python 3.9+ and nothing else.
Recipe page: docs/recipes/dashboard.md
"""

import argparse
from collections import Counter, defaultdict
from datetime import date, datetime, timedelta, timezone
import html
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


def chart(title, unit, values, start, days, bars, fmt):
    """An inline SVG: one bar or dot per day and the 7 day average as a line."""
    w, h, pad = 720, 180, 28
    if not values:
        return f'<section><h2>{html.escape(title)}</h2><p class="none">No data in this window.</p></section>'
    lo = 0 if bars else min(values.values()) * 0.95
    hi = max(values.values()) * 1.05 or 1
    x = lambda i: pad + (w - 2 * pad) * (i + 0.5) / days
    y = lambda v: h - pad - (h - 2 * pad) * (v - lo) / (hi - lo or 1)
    marks, avg = [], []
    for i in range(days):
        d = start + timedelta(days=i)
        if d in values:
            v = values[d]
            tip = f"<title>{d:%a %d %b}: {fmt.format(v)} {unit}</title>"
            if bars:
                bw = max((w - 2 * pad) / days - 1, 1)
                marks.append(f'<rect x="{x(i) - bw / 2:.1f}" y="{y(v):.1f}" width="{bw:.1f}" '
                             f'height="{h - pad - y(v):.1f}" class="mark">{tip}</rect>')
            else:
                marks.append(f'<circle cx="{x(i):.1f}" cy="{y(v):.1f}" r="2.5" class="mark">{tip}</circle>')
        week = [values[d - timedelta(days=k)] for k in range(7) if d - timedelta(days=k) in values]
        if len(week) >= 4:
            avg.append(f"{x(i):.1f},{y(sum(week) / len(week)):.1f}")
    recent = [values[d] for d in values if d > start + timedelta(days=days - 8)]
    now = fmt.format(sum(recent) / len(recent)) if recent else "-"
    labels = (f'<text x="{pad}" y="{h - 8}" class="axis">{start:%d %b}</text>'
              f'<text x="{w - pad}" y="{h - 8}" class="axis" text-anchor="end">'
              f'{start + timedelta(days=days - 1):%d %b}</text>'
              f'<text x="{pad}" y="16" class="axis">{fmt.format(hi)}</text>')
    if not bars:
        labels += f'<text x="{pad}" y="{h - pad - 4}" class="axis">{fmt.format(lo)}</text>'
    line = f'<polyline points="{" ".join(avg)}" class="avg"/>' if avg else ""
    return (f'<section><h2>{html.escape(title)} <span>{now} {unit}, last 7 days</span></h2>'
            f'<svg viewBox="0 0 {w} {h}" role="img" aria-label="{html.escape(title)}">'
            f'{labels}{"".join(marks)}{line}</svg></section>')


PAGE = """<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>My health</title>
<style>
:root {{ --bg: #fbfaf8; --fg: #1d1d1f; --muted: #6e6e73; --mark: #8fb3e8; --avg: #1f5fbf; --card: #fff; }}
@media (prefers-color-scheme: dark) {{
  :root {{ --bg: #111214; --fg: #f2f2f2; --muted: #9a9aa0; --mark: #35527d; --avg: #8ab4ff; --card: #1a1b1e; }}
}}
body {{ margin: 0; background: var(--bg); color: var(--fg); font: 15px/1.5 system-ui, sans-serif; }}
main {{ max-width: 760px; margin: 0 auto; padding: 24px 16px 48px; }}
h1 {{ font-size: 24px; margin: 0 0 4px; }}
p.sub, h2 span, .none {{ color: var(--muted); }}
section {{ background: var(--card); border-radius: 12px; padding: 12px 16px; margin: 16px 0; }}
h2 {{ font-size: 16px; margin: 0 0 4px; display: flex; justify-content: space-between; gap: 8px; flex-wrap: wrap; }}
h2 span {{ font-weight: 400; font-size: 14px; }}
svg {{ width: 100%; height: auto; display: block; }}
.mark {{ fill: var(--mark); }} .avg {{ fill: none; stroke: var(--avg); stroke-width: 2.5; }}
.axis {{ fill: var(--muted); font-size: 12px; }}
</style></head>
<body><main>
<h1>My health, last {days} days</h1>
<p class="sub">{sources}. Line: 7 day average. Made {made} with Open Health Sync.</p>
{charts}
</main></body></html>
"""


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--days", type=int, default=90, help="how many days (default 90)")
    p.add_argument("--out", default="health-dashboard.html")
    a = p.parse_args()
    start = date.today() - timedelta(days=a.days - 1)

    steps, steps_app = daily("step_count", start)
    sleep, sleep_app = nights(start - timedelta(days=1))
    rhr, rhr_app = daily("resting_hr", start)
    if not rhr:
        rhr, rhr_app = sample_means("resting_heart_rate", start)
    hrv, hrv_app = daily("hrv_avg_ms", start)
    if not hrv:
        hrv, hrv_app = sample_means("hrv_sdnn", start)
    apps = sorted({x for x, v in ((steps_app, steps), (sleep_app, sleep), (rhr_app, rhr), (hrv_app, hrv)) if x and v})

    charts = "\n".join([
        chart("Steps", "steps", steps, start, a.days, True, "{:,.0f}"),
        chart("Sleep", "h", sleep, start, a.days, True, "{:.1f}"),
        chart("Resting heart rate", "bpm", rhr, start, a.days, False, "{:.0f}"),
        chart("Heart rate variability", "ms", hrv, start, a.days, False, "{:.0f}"),
    ])
    page = PAGE.format(days=a.days, charts=charts, made=f"{date.today():%d %B %Y}",
                       sources="From " + html.escape(", ".join(apps)) if apps else "No source yet")
    with open(a.out, "w", encoding="utf-8") as f:
        f.write(page)
    print(f"wrote {a.out}: open it in a browser")


if __name__ == "__main__":
    main()
