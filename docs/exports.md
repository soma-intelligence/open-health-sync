# CSV and Parquet exports

An export is one file holding every record of one list of a user in a time window: CSV for a
spreadsheet, Parquet for a data pipeline (pandas, Polars, DuckDB, Spark, a warehouse). The file
streams in a single response, read from the database page by page, so its size is not bounded
by a page.

## Route

| Route | Answers |
|---|---|
| `GET /v1/users/{id}/exports/{dataset}` | one file, `Content-Disposition: attachment; filename="<dataset>-<user id>.<format>"` |

It takes a tenant key, like every read. A user of another tenant, or a dataset that is not
below, is a 404; errors are `application/problem+json` like the rest of the API.

| `dataset` | Records, ordered by | Same as |
|---|---|---|
| `samples` | quantity samples of every type (or of `type`), by start | `GET /v1/users/{id}/samples` |
| `category-samples` | category samples, by start | `GET /v1/users/{id}/category-samples` |
| `sleep` | sleep sessions, by bedtime start | `GET /v1/users/{id}/sleep` |
| `workouts` | workouts, by start | `GET /v1/users/{id}/workouts` |
| `nutrition` | nutrition logs, by start | `GET /v1/users/{id}/nutrition` |
| `daily` | daily summaries, by date | `GET /v1/users/{id}/daily` |

| Parameter | Meaning |
|---|---|
| `format` | `csv` (the default) or `parquet` |
| `type` | the canonical type to keep, for `samples`, `category-samples` and `workouts`; a quantity type outside the catalog is a 400 |
| `start`, `end` | RFC 3339 or `YYYY-MM-DD`; `start` <= the record's time < `end`. Both optional: without them the file holds everything. |

An export is not paged: `cursor` and `limit` are a 400.

```sh
curl -sOJ "$OHS/v1/users/$USER_ID/exports/samples?type=heart_rate&start=2026-03-01&format=parquet" \
  -H "Authorization: Bearer $KEY"
```

```python
import pandas as pd
df = pd.read_parquet("samples-3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11.parquet")
```

## Columns

The columns are the members of the list's JSON records, with the same names, in the same order:
`samples` has `id, type, value, unit, start_date, end_date, source_name, source_bundle_id,
device_model, product_type, external_id, is_primary_source, was_user_entered, metadata,
provider, source, device, ingested_at`. The fields of each list are in the
[API reference](api.md) and the [OpenAPI file](../api/openapi.yaml).

| Value | CSV | Parquet |
|---|---|---|
| number | shortest form that reads back the same (`61.5`, `1200`) | `INT64` or `DOUBLE` |
| text | as is; see below for formulas | `BYTE_ARRAY` (`STRING`) |
| true / false | `true`, `false` | `BOOLEAN` |
| time | RFC 3339 in UTC | `INT64` `TIMESTAMP(MICROS, UTC)` |
| JSON (`metadata`, `nutrients`) | its compact JSON text | `BYTE_ARRAY` (`JSON`) |
| no value (or JSON `null`) | empty field | null (the column is optional) |

CSV follows RFC 4180, UTF-8, with a header line; an empty export is the header line alone. A
text field that a spreadsheet would read as a formula (it starts with `=`, `+`, `-`, `@`, a tab
or a carriage return) is written with a leading apostrophe, so opening the file never runs
anything. Parquet files are compressed with Snappy, in row groups of 65536 rows, and keep text
as written.

A daily summary's `date` is the provider's local day, `YYYY-MM-DD`, as text in both formats.

## Failures

The status line and headers are sent before the first row. If reading the database fails after
that, the server cuts the connection instead of finishing the file: the client sees a broken
transfer (and a Parquet file without its footer does not open), never a file that looks
complete but is short.

## Not in this version

- Sleep phases and route points (each belongs to one session or workout; read them with
  `GET /v1/users/{id}/sleep/{session_id}/phases` and `GET /v1/users/{id}/workouts/{workout_id}/route`).
- Exports of several users at once, scheduled exports and exports to object storage.
