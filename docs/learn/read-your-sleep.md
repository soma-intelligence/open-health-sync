# Project 2: Read your sleep

**Goal:** a report of your last two weeks of sleep in the terminal, then a script of your own,
about twenty lines, that adds deep and REM sleep to it.

**Time:** 15 minutes. **Level:** first lines of Python. **Needs:** [project 1](first-sync.md),
with sleep from a watch, a ring or the Health app.

## 1. Run the ready-made report

```sh
python3 recipes/sleep-report.py
```

```text
Sleep, last 14 nights (apple_health)

Sun 04 Oct   8h00  ████████████████████████████████
Mon 05 Oct   6h38  ███████████████████████████
Tue 06 Oct   7h55  ████████████████████████████████
Wed 07 Oct   6h15  █████████████████████████
...
Average 7h07, shortest 6h15 (Wed 07 Oct), longest 8h00 (Sun 04 Oct)
Nights under 7 hours: 6 of 14
```

It works the same whether an Oura ring, a WHOOP band or your iPhone measured the nights, because
every source lands in the same `sleep` list with the same fields.

## 2. Start a script of your own

Every recipe begins with the same block: it reads `~/.ohs.env`, calls the API and follows the
pages of a list. Copy that block into a new file:

```sh
awk '/^def nights_by_app/{exit} {print}' recipes/sleep-report.py > my-sleep.py
```

The block gives you one function that matters: `records("sleep", start=...)` returns every sleep
session from that day on, whatever its source.

## 3. Add deep and REM sleep

Paste this at the end of `my-sleep.py`:

```python
from datetime import datetime, timedelta, timezone


def night_of(session):
    """The date of the morning a session ends on, in your computer's time zone."""
    end = datetime.fromisoformat(session["bedtime_end"][:19]).replace(tzinfo=timezone.utc).astimezone()
    return (end + timedelta(hours=10)).date()  # ends before 14:00: that morning


since = (datetime.now() - timedelta(days=14)).date().isoformat()
nights = {}
for s in records("sleep", start=since):
    if not s["bedtime_end"]:
        continue
    n = nights.setdefault(night_of(s), {"total": 0, "deep": 0, "rem": 0})
    n["total"] += s["total_sleep_seconds"] or 0
    n["deep"] += s["deep_sleep_seconds"] or 0
    n["rem"] += s["rem_sleep_seconds"] or 0

for day, n in sorted(nights.items()):
    hours = n["total"] / 3600
    print(f"{day:%a %d %b}  {hours:4.1f} h   deep {n['deep'] // 60:3d} min   REM {n['rem'] // 60:3d} min")
```

```sh
python3 my-sleep.py
```

```text
Sun 04 Oct   8.0 h   deep 100 min   REM 125 min
Mon 05 Oct   6.6 h   deep  80 min   REM 100 min
Tue 06 Oct   7.9 h   deep 100 min   REM 125 min
Wed 07 Oct   6.2 h   deep  80 min   REM  95 min
```

**What just happened.** A night can come in several pieces: a wake up in the middle, or one
piece per sleep stage from some apps. The script adds up every piece that ends on the same
morning. The API sends times in UTC, and `astimezone()` puts them back on your clock.

## Make it yours

- **One device only.** With a ring and a watch, both nights are added together. Add
  `if s["provider"] != "oura": continue` at the top of the loop (the names are in the first
  column of `python3 recipes/connect.py`).
- **Bedtime.** Keep the earliest `bedtime_start` of each night and print it. Late nights often
  explain short ones.
- **A goal.** Print a star next to the nights over your target, and count them at the end.

## Share it

Post your script, or a screenshot of the output, in
[Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).

**Next:** [Project 3, Your HRV trend](hrv-trend.md).
