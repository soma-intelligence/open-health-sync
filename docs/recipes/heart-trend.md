# Heart trend

Resting heart rate and heart rate variability (HRV) are two of the most useful numbers a
wearable gives you, and two of the noisiest from one day to the next. Week by week, the trend
shows.

```sh
go run ./recipes/heart-trend            # the last 8 weeks
go run ./recipes/heart-trend -weeks 26
```

```text
7 days to       Resting HR         HRV
Mon 24 Aug        53.8 bpm     53.8 ms
Mon 31 Aug        53.5 bpm     54.2 ms
...
Mon 05 Oct        54.9 bpm     57.8 ms
Mon 12 Oct        53.8 bpm     49.7 ms

Sources: resting HR from oura, HRV from oura
Last 7 days against the 4 weeks before: resting HR -0.5 bpm, HRV -11%
```

A rising resting heart rate with a falling HRV often goes with short sleep, stress, a hard
training block or a cold coming. It is a trend, not a diagnosis.

## How it works

It reads the `resting_hr` and `hrv_avg_ms` of `GET /v1/users/{id}/daily`, the summaries rings and
bands send each day. Apple Health has no such summary: for it the recipe averages the
`resting_heart_rate` and `hrv_sdnn` samples of each day instead. Apple's HRV is SDNN, where most
rings and bands report RMSSD; the two are not on the same scale, so the recipe never mixes them
in one series and labels SDNN when it uses it.

## Make it yours

- Flag a week where resting heart rate is 3 bpm or more above your average.
- Put your training load next to it from `GET /v1/users/{id}/workouts`.
