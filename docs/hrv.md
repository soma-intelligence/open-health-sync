# RR intervals and HRV

A heartbeat series is a short recording of every beat, such as the ones an Apple Watch takes with
its heart rate variability readings and sends through Apple Health. Open Health Sync stores each beat-to-beat
(RR) interval of a series (see [connectors/apple-health.md](connectors/apple-health.md)), serves
them as they are, and derives the usual time domain heart rate variability measures from them,
so a client gets the same numbers from the same beats on every read.

The HRV a provider reports itself (the `heart_rate_variability_sdnn` and
`heart_rate_variability_rmssd` samples, a sleep session's `hrv_avg_ms`) is a separate figure,
computed by the provider from beats Open Health Sync never sees. It stays in
[`samples`](api.md#reading-data) and is not mixed with the values below.

## Routes

| Route | Answers |
|---|---|
| `GET /v1/users/{id}/rr-intervals` | the stored intervals, one per beat, by beat time |
| `GET /v1/users/{id}/hrv` | one record per heartbeat series, by the series' first beat |

Both take a tenant key, like every read, and the same `start`, `end`, `cursor` and `limit` (100
by default, at most 1000), plus `series_id` to keep one series. A user of another tenant is a
404. The MCP tools `get_rr_intervals` and `get_hrv` return the same records.

### RR intervals

```sh
curl -s "$OHS/v1/users/$USER_ID/rr-intervals?start=2026-03-01T07:00:00Z&end=2026-03-01T07:05:00Z" \
  -H "Authorization: Bearer $KEY"
```

```json
{
  "data": [
    {
      "timestamp": "2026-03-01T07:00:00.81Z",
      "rr_ms": 810,
      "series_id": "6d3c2a5e-8f2b-4b8e-9a51-0c1f2e3d4b5a",
      "provider": "apple_health",
      "ingested_at": "2026-03-01T07:12:03.5Z"
    }
  ],
  "next_cursor": null
}
```

`timestamp` is the beat that ends the interval and `rr_ms` the time since the beat before it.
`start` and `end` bound `timestamp`. A beat that follows a gap in the recording has no interval
and is not stored, so the first beat after a gap is missing from the list.

### HRV

```sh
curl -s "$OHS/v1/users/$USER_ID/hrv?start=2026-03-01&end=2026-03-08" \
  -H "Authorization: Bearer $KEY"
```

```json
{
  "data": [
    {
      "series_id": "6d3c2a5e-8f2b-4b8e-9a51-0c1f2e3d4b5a",
      "provider": "apple_health",
      "start_date": "2026-03-01T07:00:00.81Z",
      "end_date": "2026-03-01T07:00:58.4Z",
      "intervals": 72,
      "accepted_intervals": 70,
      "successive_pairs": 67,
      "mean_rr_ms": 812.4,
      "mean_heart_rate_bpm": 73.855,
      "sdnn_ms": 41.207,
      "rmssd_ms": 38.9,
      "pnn50_percent": 17.91
    }
  ],
  "next_cursor": null
}
```

A series is in `[start, end)` when its first beat is, and is always summarized whole, beats after
`end` included: a series reads the same whatever the window. Series are ordered by their first
beat, then provider and series id. A page stops early, before `limit`, once its series hold
200000 beats (a page always holds at least one series), and `next_cursor` continues from there.

## How the measures are derived

From the intervals of one series, in beat order:

1. **Plausible.** An interval outside 300 to 2000 ms (200 to 30 beats per minute) is set aside.
2. **Not ectopic.** When the beat before it is the directly preceding beat and its interval is
   plausible, an interval that differs from it by more than 20 % of it is set aside: a premature
   beat, or a beat the sensor missed. The interval after a premature beat is usually set aside
   too, since it differs from the short one before it.
3. **Successive pairs.** Two kept intervals form a pair when their beats are directly
   consecutive: the later beat's time is the earlier beat's time plus the later interval, within
   5 ms. A pair never spans a gap in the recording or a set-aside interval.

| Field | Meaning |
|---|---|
| `intervals` | the series' stored intervals (a beat stored twice counts once) |
| `accepted_intervals` | the intervals kept after steps 1 and 2 |
| `successive_pairs` | the pairs of step 3 |
| `mean_rr_ms` | the mean of the kept intervals |
| `mean_heart_rate_bpm` | 60000 divided by `mean_rr_ms` |
| `sdnn_ms` | the sample standard deviation of the kept intervals |
| `rmssd_ms` | the square root of the mean squared difference of the pairs |
| `pnn50_percent` | the share of pairs that differ by more than 50 ms, in percent |

`sdnn_ms`, `rmssd_ms` and `pnn50_percent` are null below 10 successive pairs, and the means are
null when no interval is kept: a few beats give a number, not a measure. Values are rounded to
three decimals. These are the standard short-term measures over the series as recorded; they are
not normalized for the length of the recording, so compare series of similar length.

## Limits

- Only Apple Health sends beat-to-beat data today. Providers that report only their own HRV
  figure keep it in `samples`.
- Frequency domain measures (LF, HF) and non-linear ones are not computed.
- Without `start`, `/hrv` groups every beat the user has: pass a window.
