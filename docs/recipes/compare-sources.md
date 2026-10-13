# Compare sources

Wear a ring and a watch, carry a phone, and they never agree on your steps or your sleep. Since
Open Health Sync keeps every source with its provenance, you can see by how much.

```sh
python3 recipes/compare-sources.py                     # steps, last 14 days
python3 recipes/compare-sources.py --metric sleep      # also resting_hr, hrv
python3 recipes/compare-sources.py --metric resting_hr --days 30
```

```text
steps (steps), 2 sources

Day             apple_health            oura
2026-10-08              9507            8661
2026-10-09              9471            9505
2026-10-10              9937           10231
2026-10-11              9470            8752

apple_health vs oura: 14 days, typical gap 534 steps (6%), apple_health reads 436 steps higher on average
```

## How it works

Every record the API returns carries `provider`, the integration it came through. The recipe
reads one figure per provider and day from `GET /v1/users/{id}/daily` (steps, resting heart
rate, HRV) or from `GET /v1/users/{id}/sleep` (hours per night), then compares each pair of
sources on the days both measured: the typical gap, and which one reads higher.

## Why it matters

For you, it says which device to trust for which number. For a developer, it is the question
every multi-device product meets: show one value per day, and know how much the choice moves it.
Studies and coaches use the same comparison to calibrate one device against another.
