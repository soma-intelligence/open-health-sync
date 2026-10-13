#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Last week of your health in a short Markdown note, against the week before.

    python3 weekly-digest.py                    # prints the note
    python3 weekly-digest.py --out week.md      # writes it to a file

Paste it in your notes, send it to yourself every Monday with cron, or post it
to a chat with a webhook. Needs Python 3.9+ and nothing else.
Recipe page: docs/recipes/weekly-digest.md
"""

import argparse
from collections import Counter, defaultdict
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


def mean(values):
    return sum(values) / len(values) if values else None


def line(label, now, before, fmt, unit, better):
    if now is None:
        return None
    text = f"- **{label}**: {fmt.format(now)}{unit}"
    if before:
        d = (now - before) / before
        arrow = "→" if abs(d) < 0.03 else ("↑" if d > 0 else "↓")
        good = abs(d) >= 0.03 and (d > 0) == better
        text += f" {arrow} {d:+.0%} vs the week before" + (" 👍" if good else "")
    return text


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--out", help="write the note to this file instead of printing it")
    a = p.parse_args()
    today = date.today()
    this = today - timedelta(days=today.weekday() + 7)  # Monday of last full week
    prev = this - timedelta(days=7)
    end = this + timedelta(days=7)

    def split(values):
        return ([v for d, v in values.items() if this <= d < end],
                [v for d, v in values.items() if prev <= d < this])

    steps, _ = daily("step_count", prev, end)
    rhr, _ = daily("resting_hr", prev, end)
    if not rhr:
        rhr, _ = sample_means("resting_heart_rate", prev, end)
    hrv, _ = daily("hrv_avg_ms", prev, end)
    sleep, _ = nights(prev - timedelta(days=1), end)
    workouts = list(records("workouts", start=prev.isoformat(), end=end.isoformat()))

    s_now, s_before = split(steps)
    sl_now, sl_before = split(sleep)
    r_now, r_before = split(rhr)
    h_now, h_before = split(hrv)
    w_now = [w for w in workouts if this.isoformat() <= w["start_date"][:10] < end.isoformat()]
    w_before = [w for w in workouts if w not in w_now]
    minutes = lambda ws: sum((w["duration_s"] or 0) for w in ws) / 60

    out = [f"## Health, week of {this:%d %B %Y}", ""]
    for item in (
        line("Steps", mean(s_now), mean(s_before), "{:,.0f}", " a day", True),
        line("Sleep", mean(sl_now), mean(sl_before), "{:.1f}", " h a night", True),
        line("Resting heart rate", mean(r_now), mean(r_before), "{:.1f}", " bpm", False),
        line("HRV", mean(h_now), mean(h_before), "{:.0f}", " ms", True),
        line("Training", minutes(w_now) or None, minutes(w_before), "{:.0f}", f" min in {len(w_now)} workouts", True),
    ):
        if item:
            out.append(item)
    kinds = Counter(w["activity_type"] for w in w_now)
    if kinds:
        out.append("  " + ", ".join(f"{n} × {k.replace('_', ' ')}" for k, n in kinds.most_common()))
    if len(out) == 2:
        out.append("No data for that week yet.")
    note = "\n".join(out) + "\n"
    if a.out:
        with open(a.out, "w", encoding="utf-8") as f:
            f.write(note)
        print(f"wrote {a.out}")
    else:
        print(note, end="")


if __name__ == "__main__":
    main()
