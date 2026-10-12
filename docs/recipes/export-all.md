# Export everything

A copy of all your health data, every app in one set of files: CSV to open in a spreadsheet,
Parquet for pandas, Polars, DuckDB or Spark. Same columns whatever device the data came from.

```sh
python3 recipes/export-all.py                                  # CSV, into health-export-<date>/
python3 recipes/export-all.py --format parquet --since 2026-01-01
```

```text
health-export-2026-10-12/daily.csv                    22,907 bytes
health-export-2026-10-12/sleep.csv                    42,984 bytes
health-export-2026-10-12/workouts.csv                 10,496 bytes
health-export-2026-10-12/samples.csv                 647,810 bytes
health-export-2026-10-12/category-samples.csv            118 bytes
health-export-2026-10-12/nutrition.csv                   161 bytes
```

```python
import pandas as pd
daily = pd.read_parquet("health-export-2026-10-12/daily.parquet")
daily.pivot_table(index="date", columns="provider", values="step_count")
```

## How it works

One call per list to `GET /v1/users/{id}/exports/{dataset}`, which streams the whole list as one
file. The columns, types and time format are described in [CSV and Parquet exports](../exports.md).

## Why it matters

Your data outlives the apps. An export you hold is a backup if an account closes, a way to move
to another app, and the input of any analysis you want to run yourself.
