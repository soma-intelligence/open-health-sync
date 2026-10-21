# Export everything

A copy of all your health data, every app in one set of files: CSV to open in a spreadsheet,
Parquet for DuckDB, Polars, pandas or Spark. Same columns whatever device the data came from.

```sh
go run ./recipes/export-all                                # CSV, into health-export-<date>/
go run ./recipes/export-all -format parquet -since 2026-01-01
```

```text
health-export-2026-10-12/daily.csv                    22,907 bytes
health-export-2026-10-12/sleep.csv                    42,984 bytes
health-export-2026-10-12/workouts.csv                 10,496 bytes
health-export-2026-10-12/samples.csv                 647,810 bytes
health-export-2026-10-12/category-samples.csv            118 bytes
health-export-2026-10-12/nutrition.csv                   161 bytes
```

```sh
duckdb -c "PIVOT 'health-export-2026-10-12/daily.parquet' ON provider USING first(step_count) GROUP BY date ORDER BY date"
```

## How it works

One call per list to `GET /v1/users/{id}/exports/{dataset}`, which streams the whole list as one
file. The columns, types and time format are described in [CSV and Parquet exports](../exports.md).

## Why it matters

Your data outlives the apps. An export you hold is a backup if an account closes, a way to move
to another app, and the input of any analysis you want to run yourself.
