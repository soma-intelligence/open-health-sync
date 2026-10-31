# Open mHealth output

Open Health Sync serves a user's records as [Open mHealth](https://www.openmhealth.org/) data
points, so a tool built on the Open mHealth schemas (a research pipeline, a mobile health study,
an analysis notebook) can read them without learning the REST JSON shapes. Each data point is a
`header` and a `body` that follows one schema of the Open mHealth catalog. Every mapping is
checked against the official schemas by the unit tests, on every push (see
[Validation](#validation)).

Daily summaries, category samples, nutrition, sleep phases, routes and the quantity types
without an Open mHealth schema are [not in this version](#not-in-this-version).

## Route

| Route | Answers |
|---|---|
| `GET /v1/users/{id}/omh/data-points` | one page of data points, `{"data": [...], "next_cursor": ...}` |

It takes a tenant key, like every read, and answers `application/json`. A user of another
tenant is a 404; errors are `application/problem+json` like the rest of the API.

The list is the FHIR search's list (see [fhir.md](fhir.md#one-list-for-every-kind-of-record)):
the same records, in the same order (time, then kind, then id), with the same kind of cursor. A
record that has no Open mHealth schema is left out of its page, so a page can hold fewer than
`limit` data points and still have a `next_cursor`: follow `next_cursor` until it is null.

| Parameter | Meaning |
|---|---|
| `schema` | only the data points of one schema: bare (`heart-rate`) or with its namespace and, optionally, its version (`omh:heart-rate`, `omh:heart-rate:2.0`). One value; a schema the mapping does not emit is a 400. |
| `start`, `end` | RFC 3339 or `YYYY-MM-DD`; `start` <= the record's time < `end` |
| `limit`, `cursor` | page size (default 100, at most 1000) and the `next_cursor` of the previous page |
| `view` | `merged` (the default) or `all`, as in the [API](api.md#several-sources-of-one-metric-the-merged-view) |

```sh
curl -s "$OHS/v1/users/$USER_ID/omh/data-points?schema=omh:heart-rate&start=2026-03-01&limit=1" \
  -H "Authorization: Bearer $KEY"
```

```json
{
  "data": [
    {
      "header": {
        "id": "qs-101",
        "creation_date_time": "2026-03-01T09:15:30.25Z",
        "schema_id": { "namespace": "omh", "name": "heart-rate", "version": "2.0" },
        "acquisition_provenance": {
          "source_name": "apple_health",
          "source_data_point_id": "hk-uuid-1",
          "modality": "sensed"
        },
        "user_id": "3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11"
      },
      "body": {
        "effective_time_frame": { "date_time": "2026-03-01T08:00:00Z" },
        "heart_rate": { "value": 62, "unit": "beats/min" }
      }
    }
  ],
  "next_cursor": "MjAyNi0wMy0wMVQwODowMDowMFp8MXwxMDE"
}
```

## Mapping

### Header

| Header member | Value |
|---|---|
| `id` | the record's id, the same as its FHIR Observation id: `qs-` (a quantity sample), `bp-` (a blood pressure pair, the systolic sample's id), `sleep-`, `workout-`, then the stored row id |
| `creation_date_time` | when Open Health Sync stored the record (`ingested_at`; for a blood pressure pair, the later of the two halves) |
| `schema_id` | `omh`, the schema's name and version |
| `acquisition_provenance.source_name` | the provider the record came from (`apple_health`, `oura`, `withings`...) |
| `acquisition_provenance.source_data_point_id` | the provider's own id of the record (`external_id`), when it gave one |
| `acquisition_provenance.modality` | `self-reported` for a sample the user typed in, `sensed` for one the source says was measured, left out when the source does not say |
| `user_id` | the end user's id |

### Bodies

Each body holds the value, its unit as Open mHealth spells it, and `effective_time_frame`: a
`time_interval` (`start_date_time`, `end_date_time`) when the record has an end after its start,
else a `date_time`. A schema that only accepts an interval (step count, calories burned, sleep
episode) gets an interval from the start to the start when the record has no end. Times are
RFC 3339 in UTC.

| Records | Schema | Body |
|---|---|---|
| `heart_rate` | `omh:heart-rate:2.0` | `heart_rate` in `beats/min` |
| `resting_heart_rate` | `omh:heart-rate:2.0` | `heart_rate` in `beats/min`, `temporal_relationship_to_physical_activity: "at rest"` |
| `step_count` | `omh:step-count:3.0` | `step_count` in `steps` |
| `active_energy_burned` | `omh:calories-burned:2.0` | `kcal_burned` in `kcal` |
| `body_mass` | `omh:body-weight:2.0` | `body_weight` in `kg` |
| `height` | `omh:body-height:1.0` | `body_height` in `cm` |
| `body_mass_index` | `omh:body-mass-index:2.0` | `body_mass_index` in `kg/m^2` |
| `body_fat_percentage` | `omh:body-fat-percentage:1.0` | `body_fat_percentage` in `%` |
| `body_temperature` | `omh:body-temperature:3.0` | `body_temperature` in `C` |
| `oxygen_saturation` | `omh:oxygen-saturation:2.0` | `oxygen_saturation` in `%` |
| `respiratory_rate` | `omh:respiratory-rate:2.0` | `respiratory_rate` in `breaths/min` |
| `blood_glucose` | `omh:blood-glucose:3.0` | `blood_glucose` in `mg/dL` |
| a blood pressure pair | `omh:blood-pressure:3.1` | `systolic_blood_pressure` and `diastolic_blood_pressure` in `mmHg` |
| a sleep session | `omh:sleep-episode:1.1` | the bedtime as `effective_time_frame`, `total_sleep_time` in `sec` when known |
| a workout | `omh:physical-activity:1.2` | `activity_name` (the activity type, `trail running`), `distance` in `m` and `kcal_burned` in `kcal` when known |

A blood pressure pair is a systolic and a diastolic sample with the same user, provider, start,
end, source and device, as in the [FHIR output](fhir.md#blood-pressure); a half without its
partner has no data point.

A sample has a data point only in its type's storage unit (the unit the
[data model](data-model.md) lists, which the pipeline converts to), and only with a finite
value. A workout distance or energy that is not a finite number is left out of the body.

### Schema versions

Each schema is the newest version whose definitions are all in the
[Open mHealth schema repository](https://github.com/openmhealth/schemas). Since 2022 the
repository has been moving to the IEEE 1752.1 standard: the newest majors of several schemas
(for example `heart-rate` 3.0, `blood-pressure` 4.0) differ from the versions above only in that
their building blocks (unit value, time frame) are IEEE 1752.1 schemas hosted outside the
repository, and some schemas (`step-count` 3.0, `sleep-episode` 1.1, `physical-activity` 1.2,
`header` 1.2) are marked as superseded by their IEEE 1752.1 counterparts. The versions above are
the ones that can be checked end to end against the repository's own files. IEEE 1752.1 output
is not in this version.

## Validation

`internal/omh/testdata` holds a copy of the official schemas the mapping emits and every schema
they refer to, and the repository's own test data for them, taken unchanged from
[openmhealth/schemas](https://github.com/openmhealth/schemas) (Apache-2.0) at a pinned commit
named in `internal/omh/testdata/README.md`. The unit tests, part of `make test` and CI:

- run the repository's `shouldPass` and `shouldFail` examples through the schemas, so the
  validator is known to accept and refuse what the repository expects;
- check every golden data point (`internal/omh/testdata/golden`, one per mapping) against
  `omh:data-point:1.0` with its header, and its body against the schema its header names, with
  formats (`date-time`) asserted;
- check that every quantity type the mapping names is in the seeded catalog with the storage
  unit the mapping expects.

```sh
go test ./internal/omh              # validate
go test ./internal/omh -update      # rewrite the golden files after a mapping change
```

The golden data points come from the mapping alone. `make conformance` checks what the server
really serves: a clean synthetic dataset of every connector goes through a whole server on a fresh
database, and every data point `/omh/data-points` returns is checked the same way, with the run
failing if any schema the mapping emits is missing from it. CI runs it on every pull request (the
`conformance` job of `.github/workflows/ci.yml`), together with the HL7 validator on the FHIR the
same server served ([fhir.md](fhir.md#validation)).

## Not in this version

- Daily summaries: Open mHealth has no daily summary schema. Their steps and energy are in the
  REST list, the FHIR output and the exports.
- Quantity types without an Open mHealth schema here (heart rate variability, VO2 max, the
  scores...), category samples, nutrition, sleep phases and routes.
- IEEE 1752.1 data points (see [Schema versions](#schema-versions)).
- One data point by id: the list has no single-item route.
