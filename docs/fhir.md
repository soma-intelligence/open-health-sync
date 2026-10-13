# FHIR R4 output

Open Health Sync serves a user's records as [FHIR R4](https://hl7.org/fhir/R4/) (4.0.1)
Observations, so a FHIR client, an EHR integration or a research pipeline can read them without
learning the REST JSON shapes: quantity samples (heart rate, steps, weight, SpO2...), blood
pressure readings, sleep sessions, workouts and daily summaries. The user is served as a
[Patient](#patient), the devices the records came from as [Devices](#devices), and who produced
each Observation as its [Provenance](#provenance); [`$everything`](#everything) pages through all
of them in one Bundle. The output is checked against the specification with the official HL7
validator, in CI (see [Validation](#validation)).

Category samples, nutrition, sleep phases, routes and the other resources are
[not in this version](#not-in-this-version).

## Routes

| Route | Answers |
|---|---|
| `GET /v1/users/{id}/fhir/Observation` | a `searchset` Bundle of Observations, one page |
| `GET /v1/users/{id}/fhir/Observation/{observation_id}` | one Observation, the one an entry's `fullUrl` points to |
| `GET /v1/users/{id}/fhir/Patient/{id}` | the user as a [Patient](#patient) |
| `GET /v1/users/{id}/fhir/Patient/{id}/$everything` | a `searchset` Bundle of the Patient, the Observations, their Devices and Provenance, one page ([`$everything`](#everything)) |
| `GET /v1/users/{id}/fhir/Device` | a `searchset` Bundle of the [Devices](#devices) of the user's records, in one page |
| `GET /v1/users/{id}/fhir/Device/{device_id}` | one Device, the one an Observation's `device` points to |
| `GET /v1/users/{id}/fhir/Provenance?target=Observation/{observation_id}` | a `searchset` Bundle of the [Provenance](#provenance) of one Observation |
| `GET /v1/users/{id}/fhir/Provenance/{provenance_id}` | one Provenance |
| `GET /v1/users/{id}/fhir/metadata` | the [CapabilityStatement](#capabilitystatement) |

Every route takes a tenant key, like every read, and answers `Content-Type:
application/fhir+json`. The Observation reads read the merged view by default (`view=merged`, see
[api.md](api.md#several-sources-of-one-metric-the-merged-view)): a record that a better ranked
source covers is left out, and a daily summary carries only the figures no better ranked source
of that day holds (a summary left with none is left out). `view=all` serves every stored record
as is. A user of another tenant, a Patient id other than the user's, or a resource that is not
the user's, is a 404. Errors are `application/problem+json` like the rest of the API (not an
OperationOutcome).

### One list for every kind of record

The search returns every kind of record in one list, so a client gets everything with one paged
search. Observations come in time order, then by kind (quantity sample, sleep session, workout,
daily summary), then by id:

| Record | Its time in the list |
|---|---|
| quantity sample, blood pressure panel | the sample's start |
| sleep session | bedtime start |
| workout | start |
| daily summary | the day at midnight UTC (the Observation itself covers the local day) |

The cursor in the `next` link is the last Observation's time, kind and id, so a page never
skips or repeats one even when records of different kinds share a time. A REST list cursor is
not a valid FHIR cursor (400). The merged list is read in two steps: one query takes the ids of
the page from the four tables (each branch ordered and limited on its own index, then merged),
a second reads those rows.

The search takes these parameters, all optional. They combine: an Observation is listed when it
matches every one given.

| Parameter | Meaning |
|---|---|
| `type` | one canonical quantity name, for example `heart_rate`: quantity samples of that type only, and the blood pressure panels that hold a sample of it (a name outside the catalog is a 400) |
| `code` | a FHIR token `system\|code` matched against `Observation.code`, see below. One value; another system is a 400. |
| `category` | an HL7 observation category code, bare (`vital-signs`) or `http://terminology.hl7.org/CodeSystem/observation-category\|vital-signs`. One value. Another code of that system finds nothing; anything else is a 400. |
| `start`, `end` | RFC 3339 or `YYYY-MM-DD`; `start` <= the record's time above < `end` |
| `limit`, `cursor` | page size (default 100, at most 1000) and the cursor of the `next` link |

| `code` | Finds |
|---|---|
| `http://loinc.org\|8867-4` (any LOINC code) | the Observations whose code carries it: the quantity types the catalog codes with it, and the types the profile adds it to (8867-4 also finds resting heart rate, 2708-6 SpO2) |
| `http://loinc.org\|85354-9`, `\|8480-6`, `\|8462-4` | the blood pressure panels (R4 allows the codes of the two halves only as the panel's components) |
| `http://loinc.org\|93832-4` | the sleep sessions |
| `http://loinc.org\|55411-3` | the workouts |
| `.../CodeSystem/quantity-type\|heart_rate` | the same as `type=heart_rate` |
| `.../CodeSystem/record-type\|blood_pressure` | the blood pressure panels; `sleep_session`, `workout` and `daily_summary` likewise |

| `category` | Finds |
|---|---|
| `vital-signs` | the vital sign quantity samples and the blood pressure panels |
| `laboratory` | blood glucose and blood alcohol samples |
| `activity` | the activity quantity samples, the workouts and the daily summaries |

The Bundle has a `self` link and, unless this is the last page, a `next` link that carries the
cursor: follow `next` until there is none. `total` is left out (the API never counts). Links
and `fullUrl` start with `OHS_PUBLIC_URL`, so set it to the address clients use.

```sh
curl -s "$OHS/v1/users/$USER_ID/fhir/Observation?code=http://loinc.org%7C8867-4&start=2026-03-01&limit=2" \
  -H "Authorization: Bearer $KEY"
```

```json
{
  "resourceType": "Bundle",
  "type": "searchset",
  "link": [
    { "relation": "self", "url": "https://ohs.example.org/v1/users/3f0c.../fhir/Observation?code=...&limit=2" },
    { "relation": "next", "url": "https://ohs.example.org/v1/users/3f0c.../fhir/Observation?code=...&cursor=MjAy...&limit=2" }
  ],
  "entry": [
    {
      "fullUrl": "https://ohs.example.org/v1/users/3f0c.../fhir/Observation/qs-1001",
      "resource": {
        "resourceType": "Observation",
        "id": "qs-1001",
        "meta": {
          "source": "https://github.com/soma-intelligence/open-health-sync/fhir/provider/apple_health",
          "profile": [
            "http://hl7.org/fhir/StructureDefinition/vitalsigns",
            "http://hl7.org/fhir/StructureDefinition/heartrate"
          ],
          "tag": [{ "system": "https://github.com/soma-intelligence/open-health-sync/fhir/CodeSystem/source", "code": "apple" }]
        },
        "status": "final",
        "category": [{
          "coding": [{ "system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "vital-signs", "display": "Vital Signs" }],
          "text": "Vital Signs"
        }],
        "code": {
          "coding": [
            { "system": "http://loinc.org", "code": "8867-4" },
            { "system": "https://github.com/soma-intelligence/open-health-sync/fhir/CodeSystem/quantity-type", "code": "heart_rate" }
          ],
          "text": "heart rate"
        },
        "subject": { "reference": "Patient/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11" },
        "effectiveDateTime": "2026-03-01T08:00:00Z",
        "issued": "2026-03-01T09:15:30.25Z",
        "valueQuantity": { "value": 61, "unit": "bpm", "system": "http://unitsofmeasure.org", "code": "/min" },
        "device": { "reference": "Device/dev-4cd59d3e7b09cd28", "display": "apple_watch" }
      },
      "search": { "mode": "match" }
    }
  ]
}
```

The complete outputs for every case below are the golden files in
[internal/fhir/testdata](../internal/fhir/testdata).

## Mapping

Every Observation shares these elements; the sections below give what each kind of record adds.

| Observation | From the record |
|---|---|
| `id` | a prefix and the stored row id: `qs-`, `bp-` (the systolic sample's id), `sleep-`, `workout-`, `daily-`. Stable: a re-read gives the same id |
| `status` | always `final` |
| `subject` | `Patient/{user id}`, the [Patient](#patient) |
| `issued` | `ingested_at` |
| `meta.source` | the provider the record came through: `.../fhir/provider/{provider}` |
| `meta.tag` | the vendor that produced the record (provenance `source`), in the source code system |
| `identifier` | the provider's own id of the record (`external_id`), when it has one, with system `.../fhir/provider/{provider}/external-id` |
| `device` | `reference` is `Device/` and the id of the record's [Device](#devices); `display` is the provenance `device`, else (for a sample) `device_model`, else `product_type`. Left out when none is known, which is always the case for a daily summary |

`metadata` and the other provider fields of the REST records are not mapped.

### Quantity samples

One stored quantity sample is one Observation, except the blood pressure halves that are paired
into a [panel](#blood-pressure).

| Observation | From the sample |
|---|---|
| `category` | see [Categories](#categories) |
| `code.coding` | the LOINC code the quantity type catalog gives the type (see [data-model.md](data-model.md)), when it has one, then the canonical quantity name in the quantity type system |
| `code.text` | the quantity name with spaces (`heart rate`) |
| `effectiveDateTime` | `start_date`, when the sample is an instant (no `end_date`, or the same) |
| `effectivePeriod` | `start_date` to `end_date`, when the sample covers an interval |
| `valueQuantity` | `value`; `unit` is the storage unit as the REST API spells it, `system` UCUM and `code` its UCUM code (see [Units](#units)) |
| `dataAbsentReason` | instead of `valueQuantity` when the stored value is NaN or infinite |
| `meta.profile` | the R4 vital signs profiles the Observation meets (see [Vital signs](#vital-signs)) |

A quantity type without a LOINC code (VO2 max, body fat mass, the provider indices, a name a
connector emits that the catalog does not declare yet...) is still served, coded with the
quantity type system alone: leaving it out would make the FHIR view silently smaller than the
REST one, and a client can always filter on the codes it knows.

### Categories

`category` uses the HL7
[observation category](https://terminology.hl7.org/CodeSystem-observation-category.html) codes.
It is left out when none of them describes the measurement, rather than set to a wrong one.

| Category | Records |
|---|---|
| `vital-signs` | `heart_rate`, `resting_heart_rate`, `respiratory_rate`, `oxygen_saturation`, `body_temperature`, `blood_pressure_systolic`, `blood_pressure_diastolic`, `body_mass`, `height`, `body_mass_index`, and the blood pressure panels |
| `laboratory` | `blood_glucose`, `blood_alcohol_content` |
| `activity` | steps, flights, distances, active energy, Apple exercise, move and stand time, push count, physical effort, walking and running speed, running and cycling power, running form metrics, strain and workout strain, and the other activity counts of the catalog (and their intraday variants); workouts; daily summaries |
| none | everything else: body composition, heart rate variability, VO2 max, the other temperatures, readiness scores, audio exposure, respiratory function, insulin, falls, UV, electrodermal activity, vascular age, pulse wave velocity, names outside the catalog, and sleep sessions (no HL7 category describes sleep) |

### Glucose from a CGM

The catalog codes `blood_glucose` with LOINC `2339-0`, glucose in blood. A continuous glucose
monitor measures the fluid under the skin instead, so a glucose sample whose
`metadata.specimen` is `interstitial_fluid` (every [Dexcom](connectors/dexcom.md) reading, and
[Health Connect](connectors/health-connect.md) readings with that specimen source) is coded
with LOINC `99504-3`, glucose in interstitial fluid, the code the HL7 CGM implementation guide
uses. A `code` search for `2339-0` finds both kinds, through the catalog; a search for `99504-3` finds nothing yet.

### Vital signs

R4 requires an Observation that carries one of its vital signs LOINC codes to meet the matching
profile. The mapping does, and names the profiles in `meta.profile`.

| Quantity type | Codes | Profile |
|---|---|---|
| `heart_rate` | LOINC 8867-4 | heartrate, `/min` |
| `resting_heart_rate` | LOINC 8867-4 (required by the profile), then 40443-4 | heartrate, `/min` |
| `respiratory_rate` | LOINC 9279-1 | resprate, `/min` |
| `oxygen_saturation` | LOINC 2708-6 (required by the profile), then 59408-5 (pulse oximetry, the catalog's) | oxygensat, `%` |
| `body_temperature` | LOINC 8310-5 | bodytemp, `Cel` |
| `body_mass` | LOINC 29463-7 | bodyweight, `kg` |
| `height` | LOINC 8302-2 | bodyheight, `cm` |
| `body_mass_index` | LOINC 39156-5 | bmi, `kg/m2` |
| blood pressure panel | LOINC 85354-9, components 8480-6 and 8462-4 | bp, `mm[Hg]` |
| `blood_pressure_systolic`, `blood_pressure_diastolic` alone | quantity type system only | none, see below |

Every profiled Observation also names the base `vitalsigns` profile. A profile is claimed only
when the catalog gives the type the code above and the value is in a unit the profile accepts.

### Blood pressure

Systolic and diastolic pressure are stored as two samples, and R4 allows their LOINC codes
(8480-6, 8462-4) only as the two components of one blood pressure panel (85354-9). So a
systolic and a diastolic sample of the same reading become one panel Observation:

- **Pairing.** The two samples pair when they share user, provider, start, end, source and
  device (an unknown end, source or device matches only an unknown one). When several samples
  of one type share that key (a duplicate reading), the one with the lowest id pairs and the
  others stay single, so a sample is in at most one pair and the pairing does not depend on the
  page or the filters.
- **The panel.** `id` is `bp-` and the systolic sample's id. `code` is LOINC 85354-9 and the
  record type `blood_pressure`; there is no `valueQuantity`; `component` holds the systolic
  value (LOINC 8480-6, then the quantity type) and the diastolic value (8462-4), each in
  `mm[Hg]`, or a `dataAbsentReason` when it is NaN or infinite. `effective`, `device` and
  `meta` come from the systolic sample, `identifier` lists both samples' external ids, and
  `issued` is the later of the two. It meets the `vitalsigns` and `bp` profiles.
- **Paging.** The pairing is done in SQL: the diastolic half of a pair is never listed on its
  own, and the panel is listed at the systolic sample's place, so a pair is never split across
  pages or listed twice. `qs-` and either half's id is a 404; so is `bp-` and the id of a
  sample that is not the systolic half of a pair.
- **Unpaired halves** stay as before: their own Observation in the `vital-signs` category,
  coded with the quantity type system only (no LOINC code, no profile).
- A half in a unit other than `mm[Hg]` keeps its value, but the panel then carries neither
  85354-9 nor a profile (the profile requires `mm[Hg]`). The storage unit is `mmHg`, so this
  does not happen with data the connectors write today.

### Sleep sessions

One sleep session is one Observation of its total sleep duration.

| Observation | From the session |
|---|---|
| `id` | `sleep-` and the row id |
| `code` | LOINC 93832-4 (sleep duration), then the record type `sleep_session` |
| `category` | none |
| `effectivePeriod` | `bedtime_start` to `bedtime_end` (`effectiveDateTime` when there is no end) |
| `valueQuantity` | `total_sleep_seconds` in `s`; `dataAbsentReason` `unknown` when it is not known |
| `component` | each of the fields below that has a value |

| Component | Code | Unit |
|---|---|---|
| `deep_sleep_seconds`, `rem_sleep_seconds`, `light_sleep_seconds`, `awake_seconds` | component code system only | `s` |
| `sleep_score` | component code system only | `{score}` |
| `hrv_avg_ms` | LOINC 80404-7 | `ms` |
| `resting_hr` | LOINC 40443-4 | `/min` |
| `respiratory_rate` | LOINC 9279-1 | `/min` |
| `spo2_avg` | LOINC 59408-5 | `%` |

Every component code also carries the field name in the component code system. The stage
durations carry no LOINC code: LOINC's sleep stage codes were not checked against a
terminology server, and a wrong code is worse than none. Sleep phases are not mapped.

### Workouts

One workout is one Observation of its duration.

| Observation | From the workout |
|---|---|
| `id` | `workout-` and the row id |
| `code` | LOINC 55411-3 (exercise duration), then the record type `workout`; `text` is the activity (`trail running workout`) |
| `category` | `activity` |
| `effectivePeriod` | `start_date` to `end_date` |
| `valueQuantity` | `duration_s` in `s`; `dataAbsentReason` `unknown` when it is not known |
| `component` | the activity type, then each measurement that has a value |

| Component | Code | Value |
|---|---|---|
| `activity_type` | LOINC 73985-4 (exercise activity) | `valueCodeableConcept` in the workout activity code system (`running`, `cycling`...) |
| `active_energy_kcal` | LOINC 41981-2 (calories burned) | `kcal` |
| `distance_m` | component code system only | `m` |
| `avg_heart_rate`, `max_heart_rate` | component code system only | `/min` |
| `avg_speed_mps` | component code system only | `m/s` |
| `elevation_gain_m` | component code system only | `m` |
| `vo2_max` | component code system only | `mL/kg/min` |

### Daily summaries

One daily summary is one Observation with each metric the provider gave as a component (one
Observation per day rather than per metric keeps one id per row and one place in the list).

| Observation | From the summary |
|---|---|
| `id` | `daily-` and the row id |
| `code` | the record type `daily_summary` (LOINC has no code for a daily summary panel) |
| `category` | `activity` |
| `effectivePeriod` | local midnight to the next local midnight of `date`, in the zone of the `timezone` metadata (an IANA name), else UTC: 23 or 25 hours on a daylight saving change |
| `component` | each metric that has a value; `dataAbsentReason` `unknown` when none has |

| Component | Code | Unit |
|---|---|---|
| `step_count` | LOINC 41950-7 (steps in 24 hours) | `{steps}` |
| `active_kcal` | LOINC 41981-2 | `kcal` |
| `resting_hr` | LOINC 40443-4 | `/min` |
| `hrv_avg_ms` | LOINC 80404-7 | `ms` |
| `total_kcal`, `distance_m` | component code system only | `kcal`, `m` |
| `floors_climbed` | component code system only | `{floors}` |
| `readiness_score`, `activity_score`, `sleep_score`, `recovery_score`, `strain_score` | component code system only | `{score}` |

A `code` search matches `Observation.code` only, so `code=http://loinc.org|41950-7` does not
find daily summaries; use `code=.../CodeSystem/record-type|daily_summary`.

### Units

`valueQuantity.code` is UCUM. For a quantity sample it is the catalog's UCUM unit when the type
has one, else the spelling of the storage unit below; a unit outside this table (a type the
catalog does not declare) is kept in `unit` with no `system` or `code`. The components of sleep
sessions, workouts and daily summaries use the units of their tables above.

| Storage unit | UCUM | Storage unit | UCUM |
|---|---|---|---|
| `bpm` | `/min` | `kcal` | `kcal` |
| `percent` | `%` | `kcal/kg/h` | `kcal/kg/h` |
| `celsius` | `Cel` | `min` | `min` |
| `mmHg` | `mm[Hg]` | `W` | `W` |
| `kg`, `kg/m2`, `cm`, `m`, `m/s`, `ms`, `L`, `L/min`, `mg/dL`, `mL/kg/min` | the same | `uS` | `uS` |
| `years` | `a` | `dBASPL` | `dB[SPL]` |
| `IU` | `[IU]` | `count`, `index`, `score` | `{count}`, `{index}`, `{score}` |

A count, index or score is dimensionless: its UCUM code is an annotation, which means 1, and
`unit` repeats the annotation's word (`{steps}` reads `steps`). Values are the stored ones, in
the storage unit: nothing is converted on the way out (durations stay in seconds).

## Patient

`GET /v1/users/{id}/fhir/Patient/{id}` serves the user, and the `subject` of every Observation
points to it. Open Health Sync stores no name, birth date or other demographics, so the Patient
holds only what it does store:

| Patient | From the user |
|---|---|
| `id` | the user id; the path's two ids must be the same, another is a 404 |
| `identifier` | the tenant's `external_ref`, when it set one, with system `.../fhir/tenant/{tenant id}/external-ref` (an `external_ref` is unique within one tenant only) |
| `active` | always `true`: a user being deleted is a 404 |

## Devices

A Device is one device the user's records came from, as their provenance names it. Its
identity is five stored fields: the provider, the source (`source_slug`), and the device fields
of the record: `device_id`, and for a quantity sample `device_model` and `product_type` (sleep
sessions and workouts keep `device_id` only; daily summaries keep none). A record with no
device id, model or product type names no Device. Two records with the same five fields name
the same Device, whatever their kind.

| Device | From the device fields |
|---|---|
| `id` | `dev-` and the first 16 hex digits of the SHA-256 of the user id and the five fields. Stable: a re-read gives the same id, and another user's device of the same fields has another id |
| `meta.source` | the provider, as on the Observations: `.../fhir/provider/{provider}` |
| `meta.tag` | the source, in the source code system, when known |
| `identifier` | `device_id`, the provider's own device name or id, with system `.../fhir/provider/{provider}/device-id`, when known |
| `deviceName` | `device_model`, with type `model-name`, when known |
| `modelNumber` | `product_type`, when known. For Apple Health it is the hardware that ran the app that wrote the sample (`Watch7,2`), most often the device itself |
| `patient` | `Patient/{user id}` |

The id is computed from the record's own fields, so an Observation references its Device with
no extra query. `GET /fhir/Device` reads the five fields of the user's quantity samples, sleep
sessions and workouts whose time is in `[start, end)` (both optional, as for Observations), each
device once, in one page with no `next` link: a user has a handful of devices. It reads the
`(tenant_id, user_id, time)` index of each table, so a window reads only the chunks it covers;
with no window it reads the device fields of every record of the user once, which on years of
minute-level samples takes seconds. Pass `start` and `end` on a large history, or use
[`$everything`](#everything), which gives the Devices of each page from the rows it already
reads. `cursor` and `limit` are a 400. `GET /fhir/Device/{device_id}` makes the same read with
no window, then answers the Device of that id (404 when none of the user's records names it).

## Provenance

Each Observation has one Provenance: who and what produced it.

| Provenance | From the record |
|---|---|
| `id` | `prov-` and the Observation id (`prov-qs-1001`). Stable |
| `target` | `Observation/{observation id}` |
| `recorded` | `ingested_at`, the Observation's `issued` (the later of a panel's two samples) |
| `agent` | the record's [Device](#devices), when it names one, with type `author`; then the provider integration the record came through, with type `assembler`, as a logical reference: `identifier` with system `.../fhir/provider` and the provider (`apple_health`) |
| `entity` | the provider's own record, when it has an id: role `derivation`, `what` a logical reference to the Observation's `identifier` (both samples' of a panel) |

The agent types are codes of the HL7
[provenance participant type](https://terminology.hl7.org/CodeSystem-provenance-participant-type.html)
system. The vendor that produced the record is not an agent of its own: it is the `meta.tag` of
the Observation and of the Device. The raw upload or provider response a record was read from is
not linked: the stored records do not keep its key.

`GET /fhir/Provenance` takes one `target`, `Observation/` and an Observation id, and answers a
Bundle with its Provenance, or no entry when the user has no such Observation. Without `target`,
or with another resource type, it is a 400. `GET /fhir/Provenance/{provenance_id}` reads one;
the Provenance of an Observation that does not exist is a 404.

## $everything

`GET /v1/users/{id}/fhir/Patient/{id}/$everything` is a `searchset` Bundle of the Patient and
all their resources, paged like the Observation search: follow `next` until there is none.

| Entry | `search.mode` | On which page |
|---|---|---|
| the Patient | `match` | the first (no `cursor`) |
| the Observations | `match` | the page of the [Observation search](#one-list-for-every-kind-of-record) with the same `start`, `end`, `limit`, `cursor` and `view` (merged by default) |
| the Devices those Observations reference | `include` | each page that references it, once per page |
| the Provenance of each Observation | `include` | the Observation's page |

The entries come in that order. `limit` counts the Observations, and the cursor in the `next`
link is the Observation search's, so no Observation is skipped or repeated and every reference
of a page resolves to an entry of the same page, except `Patient/{user id}` on the later
pages, which the first page holds. `start` and `end` bound the Observations only. The Devices and the Provenance are made
from the rows of the page: `$everything` costs the Observation search's two queries and a read
of the user. `type`, `code` and `category` are not taken (`type` is a 400); the FHIR `_since`,
`_type` and `_count` parameters are not supported.

## CapabilityStatement

`GET /v1/users/{id}/fhir/metadata` describes these routes as a `CapabilityStatement` of kind
`instance`, for the user's FHIR base (`implementation.url`): `read` and `search-type` on
Observation, Device and Provenance with their parameters, `read` and the `everything` operation
on Patient. It needs a tenant key like every route.

## Identifiers Open Health Sync defines

Every URI below starts with `https://github.com/soma-intelligence/open-health-sync/fhir`. They
are identifiers, documented here; nothing is served at them.

| URI | Used for |
|---|---|
| `.../CodeSystem/quantity-type` | `code.coding`: the canonical quantity names of [data-model.md](data-model.md) |
| `.../CodeSystem/record-type` | `code.coding`: `blood_pressure`, `sleep_session`, `workout`, `daily_summary` |
| `.../CodeSystem/observation-component` | `component.code.coding`: the REST field a component comes from (`deep_sleep_seconds`, `distance_m`...) |
| `.../CodeSystem/workout-activity` | a workout's activity type: the canonical activity names (`running`, `cycling`...) |
| `.../CodeSystem/source` | `meta.tag`: the vendor that produced the record |
| `.../provider/{provider}` | `meta.source`: the provider the record came through |
| `.../provider/{provider}/external-id` | `identifier.system`: the provider's own record ids |
| `.../provider/{provider}/device-id` | `Device.identifier.system`: the provider's own device names or ids |
| `.../provider` | `Provenance.agent.who.identifier.system`: the provider integrations (`apple_health`, `withings`...) |
| `.../tenant/{tenant id}/external-ref` | `Patient.identifier.system`: the tenant's own references to its users |

No custom extension is used, so a validator needs nothing beyond the R4 specification.

## Validation

Each mapping case has a golden file in [internal/fhir/testdata](../internal/fhir/testdata): a
profiled vital sign of each kind, resting heart rate, an unpaired systolic sample, a blood
pressure panel, an interval, a laboratory value, a type without LOINC code, a type and unit
outside the catalog, a NaN value, a sleep session, a workout, a daily summary on a daylight
saving day, searchset Bundles (vital signs, every kind of record, empty), a Patient with and
without identifier, a Device with every field and one with a model only, the Provenance of a
sample, of a blood pressure panel and of a daily summary (no device), the Device and Provenance
searchsets, a first and a last `$everything` page, and the CapabilityStatement. `go test
./internal/fhir` compares the mapping with them (`-update` rewrites them), and checks they hold
no empty array, object or string, which FHIR JSON forbids. The contract tests check the same
files against `api/openapi.yaml`, each against the route that answers it.

`make fhir-validate` (`scripts/fhir-validate.sh`) runs the official HL7 validator on the golden
files with `-version 4.0.1`. It needs Java 17 or later and the network. CI runs it
(`.github/workflows/fhir-validate.yml`, Java 21) on every pull request and push to `main` that
changes `internal/fhir`, the script or the workflow, and fails on any error. The warnings left
are expected:

- no narrative (`dom-6`) and no `performer`: both best practice, neither required;
- a reference to a resource the file does not hold (the `Device/` of a single Observation, the
  `Patient/` of a Bundle page that is not the first) cannot be resolved;
- the Open Health Sync code systems are not published, so their codes cannot be checked;
- a UCUM annotation such as `{steps}` is flagged as best practice to review.

Run offline (`FHIR_VALIDATOR_TX=n/a`), the validator also says it cannot check the UCUM and LOINC
codes; with the default terminology server it checks them.

## Not in this version

Planned, in no fixed order:

- Category samples, nutrition, sleep phases and workout routes as FHIR resources, and the
  devices of those records.
- Patient demographics (none are stored), a Patient search, and an `Organization` for each
  provider (the Provenance names it by identifier only).
- A Provenance entity for the raw upload or provider response a record was read from.
- Bulk export (`$export`), OperationOutcome errors, the FHIR `date`, `component-code`,
  `_count`, `_since`, `_type`, `_include` and `_revinclude` parameters, and access with a user
  token.
- Open mHealth schemas.
