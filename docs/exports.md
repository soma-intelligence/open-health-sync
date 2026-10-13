# CSV and Parquet exports

An export is one file holding every record of one list of a user in a time window: CSV for a
spreadsheet, Parquet for a data pipeline (pandas, Polars, DuckDB, Spark, a warehouse). The file
streams in a single response, read from the database page by page, so its size is not bounded
by a page.

To hand a person everything held about them at once, use the
[complete user archive](#complete-user-archive-data-portability) instead: one zip file with
every list, the FHIR and Open mHealth forms and the raw payloads.

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
| `view` | `merged` (the default) or `all`, as in the [API](api.md#several-sources-of-one-metric-the-merged-view); a full backup passes `all`. Nutrition logs have no merge. |

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
| JSON (`metadata`, `nutrients`, the daily `sources`) | its compact JSON text | `BYTE_ARRAY` (`JSON`) |
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

## Complete user archive (data portability)

`GET /v1/users/{id}/archive` returns everything the instance holds about one user as one zip
file. It is the counterpart of `DELETE /v1/users/{id}`: deletion answers a request for erasure,
the archive a request for access or for data portability (GDPR articles 15 and 20). The file
streams in a single response, read page by page, so its size is not bounded by memory.

| Route | Answers |
|---|---|
| `GET /v1/users/{id}/archive` | one zip file, `Content-Disposition: attachment; filename="ohs-user-<user id>-<YYYYMMDDTHHMMSSZ>.zip"` (the generation time, UTC) |

It takes a tenant key (any user of the tenant) or a user token with scope `read` (its own user
only, so an app can let a person download their own archive). Another user, a user of another
tenant, or a user being deleted is a 404; a user token without `read` is a 403. The route takes
no parameter: the archive always holds everything.

```sh
curl -sOJ "$OHS/v1/users/$USER_ID/archive" -H "Authorization: Bearer $KEY"
unzip -l ohs-user-*.zip
```

### Layout

```
manifest.json                   what the archive holds: user, times, version, counts, folders
user.json                       the user and its provider connections
canonical/samples.ndjson        quantity samples of every type
canonical/category-samples.ndjson
canonical/sleep.ndjson          sleep sessions
canonical/sleep-phases.ndjson   the phases of every session (each names its session_id)
canonical/workouts.ndjson
canonical/workout-routes.ndjson the route points of every workout (each names its workout_id)
canonical/nutrition.ndjson
canonical/daily.ndjson          daily summaries
fhir/bundle.json                one FHIR R4 Bundle, type collection
omh/data-points.ndjson          Open mHealth data points
raw/<provider>/<day>/<file>     every raw payload, byte for byte
```

- **`manifest.json`**: `archive_version` (1 for this layout), `user_id`, `external_ref`,
  `created_at`, `generated_at`, `version` (the instance version), `files` (each file but the
  raw ones with its record count: lines of an NDJSON file, entries of the Bundle), `raw`
  (number of raw objects and their total bytes) and `folders` (a short description of each
  part).
- **`user.json`**: the user as `GET /v1/users/{id}` returns it, plus `connections`: for each
  provider its `provider`, `status`, `provider_user_id`, `scopes`, `last_sync_at`,
  `created_at` and `updated_at`. Tokens and other sealed values never leave the database.
- **`canonical/`**: one JSON object per line, the same JSON as the items of the REST lists
  (`GET /v1/users/{id}/samples`, `/sleep`, `/sleep/{session_id}/phases`...), in the same order.
  An empty list is an empty file.
- **`fhir/bundle.json`**: a Bundle of type `collection` with every Observation the FHIR
  endpoints serve (quantity samples, blood pressure panels, sleep sessions, workouts, daily
  summaries), as described in [fhir.md](fhir.md). Each `fullUrl` is the Observation's URL on
  the instance. A user without records gets a Bundle without `entry`.
- **`omh/data-points.ndjson`**: the records that have an Open mHealth schema, as
  `GET /v1/users/{id}/omh/data-points` serves them; see [open-mhealth.md](open-mhealth.md).
- **`raw/`**: every object of the raw store under `raw/<tenant>/<user>/`, with its path below
  that prefix: the exact payloads providers sent and devices uploaded (gzip NDJSON parts stay
  gzip). Raw retention may already have removed old ones.

### Answering a portability request

1. Check the person is who they say, in your own product. Open Health Sync does not know them.
2. Download the archive with your tenant key, or let your app call the route with the
   person's own `read` token.
3. Hand over the zip as is. `canonical/` is structured and machine readable; `fhir/` and
   `omh/` can be imported into other health systems; `raw/` is what each source sent.
4. To erase the person afterwards, call `DELETE /v1/users/{id}`.

The archive holds health data. Serve it only over TLS, do not keep copies longer than needed,
and do not log its content. The server never logs record values; a failure is logged with the
route and the error only.

### Failures

As for single exports, the status line and headers are sent first. A failure once the archive
has started cuts the connection: the zip then has no central directory and does not open, so a
truncated archive is never taken for a complete one. Records are read list by list without one
database snapshot: data synced while the archive streams may or may not be in it, and the
manifest counts what was written.

## Not in this version

- Sleep phases and route points (each belongs to one session or workout; read them with
  `GET /v1/users/{id}/sleep/{session_id}/phases` and `GET /v1/users/{id}/workouts/{workout_id}/route`).
- Exports of several users at once, scheduled exports and exports to object storage.
- ECG recordings and heartbeat (RR) series in `canonical/` and `fhir/` of the user archive:
  they have no read route yet, so the archive holds them only as the raw payloads they came in.
