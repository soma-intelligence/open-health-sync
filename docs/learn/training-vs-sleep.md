# Project 5: Does training cost you sleep?

**Goal:** a script of your own that puts two lists side by side, your workouts and your nights,
and answers a question your apps answer separately: do you sleep less after a hard day?

**Time:** 30 minutes. **Level:** some Python. **Needs:** [project 2](read-your-sleep.md), with
workouts from a watch, Strava, Polar, Suunto, intervals.icu or the Health app, and sleep from any
source. They do not need to come from the same device.

## 1. Start from the connection block

```sh
awk '/^def nights_by_app/{exit} {print}' recipes/sleep-report.py > training-vs-sleep.py
```

## 2. Join workouts and nights

Paste this at the end of `training-vs-sleep.py`:

```python
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo


def local(stamp, record):
    """A UTC time from the API, on the clock of the place it was recorded."""
    t = datetime.fromisoformat(stamp[:19]).replace(tzinfo=timezone.utc)
    zone = record["metadata"].get("timezone")
    return t.astimezone(ZoneInfo(zone)) if zone else t.astimezone()


since = (datetime.now() - timedelta(days=56)).date()

# Minutes of training per day.
training = {}
for w in records("workouts", start=since.isoformat()):
    day = local(w["start_date"], w).date()
    training[day] = training.get(day, 0) + (w["duration_s"] or 0) / 60

# Hours of sleep per night, named by the morning it ends on.
sleep = {}
for s in records("sleep", start=since.isoformat()):
    if s["bedtime_end"]:
        morning = (local(s["bedtime_end"], s) + timedelta(hours=10)).date()
        sleep[morning] = sleep.get(morning, 0) + (s["total_sleep_seconds"] or 0) / 3600

# The night after each day: trained 45 minutes or more, or rested.
groups = {"after 45+ min of training": [], "after a rest day": []}
for morning, hours in sleep.items():
    minutes = training.get(morning - timedelta(days=1), 0)
    if minutes >= 45:
        groups["after 45+ min of training"].append(hours)
    elif minutes == 0:
        groups["after a rest day"].append(hours)

print(f"Sleep since {since:%d %b}")
for name, nights in groups.items():
    if nights:
        print(f"  {name:27} {sum(nights) / len(nights):.2f} h   ({len(nights)} nights)")
```

```sh
python3 training-vs-sleep.py
```

```text
Sleep since 18 Aug
  after 45+ min of training   6.76 h   (18 nights)
  after a rest day            7.16 h   (30 nights)
```

**How it works.**

1. `records("workouts")` and `records("sleep")` read the two lists. A workout from Strava and a
   night from an Oura ring have the same shape here, so the script never asks where they came
   from.
2. Both are put on your local clock. The API sends UTC; `metadata.timezone` says where a record
   was made when the source knows it, otherwise the script uses your computer's zone.
3. Each night is matched with the day before it: the night that ends on Tuesday morning follows
   Monday's training.
4. Days with a short workout (under 45 minutes) are left out of both groups, so the comparison is
   between clearly hard days and clearly easy ones.

**Read it with care.** A difference over a handful of nights can be chance, and a hard day can
fall on the day you also went out. The count next to each average is there to remind you: the
more nights, the more it means.

## Make it yours

- **Late sessions.** Split the training days by the hour the workout started: before or after
  18:00. Evening sessions are often the ones that shorten a night.
- **HRV, not hours.** Use `records("daily", start=...)` and its `hrv_avg_ms` (rings and bands) in
  place of sleep hours, matched the same way.
- **By sport.** Each workout has an `activity_type` (`running`, `cycling`, `swimming`...): keep
  one and see if the answer changes.

## Share it

Your script and what you found, even "no difference at all", in
[Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).
A finding that holds over months is worth a post of its own.

**Next:** [Project 6, Your own dashboard](your-dashboard.md).
