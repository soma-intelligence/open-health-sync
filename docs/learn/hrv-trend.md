# Project 3: Your HRV trend

**Goal:** your resting heart rate and heart rate variability (HRV) week by week, how this week
compares, and, with an Apple Watch, the HRV of each recording computed from your own heartbeats.

**Time:** 15 minutes. **Level:** terminal. **Needs:** [project 1](first-sync.md), with a ring, a
band or an Apple Watch.

## 1. The weekly trend

Day to day, resting heart rate and HRV jump around. Week by week, a trend shows.

```sh
go run ./recipes/heart-trend -weeks 8
```

```text
7 days to       Resting HR         HRV
Tue 22 Sep        54.0 bpm     53.0 ms
Tue 29 Sep        55.1 bpm     52.6 ms
Tue 06 Oct        53.6 bpm     57.7 ms
Tue 13 Oct        52.3 bpm     55.0 ms

Sources: resting HR from apple_health, HRV from apple_health (SDNN)
Last 7 days against the 3 weeks before: resting HR -2.0 bpm, HRV +1%
```

A resting heart rate going up while HRV goes down often comes with short nights, stress, a hard
training block or a cold on its way. It is a trend to notice, not a diagnosis.

**One thing to know.** Rings and bands usually report HRV as RMSSD, the Apple Watch as SDNN.
They are two different measures on two different scales, so the recipe never mixes them and says
which one it used. Compare yourself with yourself, on the same device.

## 2. HRV from your own heartbeats (Apple Watch)

When an Apple Watch takes an HRV reading, it also records the time between each of your
heartbeats, and the Health export carries them. Open Health Sync keeps every one of those
intervals and computes the standard measures from them, the same way every time:

```sh
set -a; . ~/.ohs.env; set +a
START=$(date -v-30d +%F 2>/dev/null || date -d '30 days ago' +%F)   # macOS, then Linux
curl -s "$OHS_URL/v1/users/$OHS_USER/hrv?start=$START" -H "Authorization: Bearer $OHS_KEY" \
  | jq -r '.data[] | "\(.start_date[:16])  RMSSD \(.rmssd_ms // "-") ms  SDNN \(.sdnn_ms // "-") ms  (\(.accepted_intervals) beats)"'
```

```text
2026-10-01T07:00  RMSSD 38.9 ms  SDNN 41.207 ms  (70 beats)
2026-10-02T07:01  RMSSD 44.2 ms  SDNN 47.031 ms  (68 beats)
```

Now you have RMSSD from your watch too, so it can sit next to the figure a ring gives. A dash
means the recording had too few clean beats to give a measure. How the beats are cleaned and
counted is in [RR intervals and HRV](../hrv.md).

## Make it yours

- **Flag a week.** Open `recipes/heart-trend/main.go` and print a `!` next to any week whose resting
  heart rate is 3 bpm or more above the average of the others.
- **A longer view.** `-weeks 26` shows half a year: you may see a training block, a holiday or
  a season.
- **Morning only.** Keep the `/hrv` recordings that start before 9:00, when you are most
  comparable from one day to the next.

## Share it

A screenshot of your trend, with or without the numbers, in
[Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).

**Next:** [Project 4, Ask an AI about your training](ask-an-ai.md).
