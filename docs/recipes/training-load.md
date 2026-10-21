# Training load

How much did you train these last weeks, and is this week a jump? One command adds up your
workouts week by week, whatever watch, band or app recorded them.

```sh
go run ./recipes/training-load              # the last 8 weeks
go run ./recipes/training-load -weeks 26
```

```text
7 days to     Sessions      Time  Distance    Energy
Tue 25 Aug           4      4h42   94.0 km 2604 kcal
Tue 01 Sep           4      4h45   69.8 km 2450 kcal
...
Tue 06 Oct           4      4h56   99.4 km 2732 kcal
Tue 13 Oct           5      6h20   61.5 km 3180 kcal

Last 7 days against the 4 weeks before: time +48% (380 min against 257)
Workouts: 31, most often running
```

A week far above the ones before it is when overuse injuries and bad weeks tend to start. Run
[Heart trend](heart-trend.md) next to it: more training with a rising resting heart rate or a
falling HRV often means you are not recovering. It is a trend, not a diagnosis.

## How it works

It reads `GET /v1/users/{id}/workouts` in the merged view, so a run your watch recorded and that
also reached Strava counts once. For each 7 day window ending today it adds the sessions,
`duration_s`, `distance_m` and `active_energy_kcal`; a workout that does not report one of them
adds nothing to that column. The last line compares the training time of the last 7 days with
the mean of the 4 weeks before.

## Make it yours

- Break the time down by `activity_type` (running, cycling, strength).
- Weight each session by its `avg_heart_rate` for a heart rate based load.
- Add the week's average sleep from [Sleep report](sleep-report.md).
