# FHIR R4 output

Open Health Sync serves a user's quantity samples (heart rate, steps, weight, SpO2...) as
[FHIR R4](https://hl7.org/fhir/R4/) (4.0.1) Observations, so a FHIR client, an EHR integration
or a research pipeline can read them without learning the REST JSON shapes. The output is
checked against the specification with the official HL7 validator (see [Validation](#validation)).

This first version covers quantity samples only. Sleep, workouts and the rest are
[planned](#not-in-this-version).

## Routes

| Route | Answers |
|---|---|
| `GET /v1/users/{id}/fhir/Observation` | a `searchset` Bundle of Observations, one page |
| `GET /v1/users/{id}/fhir/Observation/{observation_id}` | one Observation, the one an entry's `fullUrl` points to |

Both take a tenant key, like every read, and answer `Content-Type: application/fhir+json`. A
user of another tenant, or an Observation that is not the user's, is a 404. Errors are
`application/problem+json` like the rest of the API (not an OperationOutcome).

The search takes the usual read parameters, all optional:

| Parameter | Meaning |
|---|---|
| `type` | one canonical quantity name, for example `heart_rate` (a name outside the catalog is a 400) |
| `code` | a FHIR token `system\|code`: `http://loinc.org\|8867-4`, or the quantity type system below with a quantity name. One value; another system is a 400. |
| `start`, `end` | RFC 3339 or `YYYY-MM-DD`; `start` <= sample start < `end` |
| `limit`, `cursor` | page size (default 100, at most 1000) and the cursor of the `next` link |

Observations come in sample start order. The Bundle has a `self` link and, unless this is the
last page, a `next` link that carries the cursor: follow `next` until there is none. `total` is
left out (the API never counts). Links and `fullUrl` start with `OHS_PUBLIC_URL`, so set it to
the address clients use.

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
        "device": { "display": "apple_watch" }
      },
      "search": { "mode": "match" }
    }
  ]
}
```

The complete outputs for every case below are the golden files in
[internal/fhir/testdata](../internal/fhir/testdata).

## Mapping

One stored quantity sample is one Observation.

| Observation | From the sample |
|---|---|
| `id` | `qs-` and the stored row id: stable, a re-read gives the same id |
| `status` | always `final` |
| `category` | see [Categories](#categories) |
| `code.coding` | the LOINC code the quantity type catalog gives the type (see [data-model.md](data-model.md)), when it has one, then the canonical quantity name in the quantity type system |
| `code.text` | the quantity name with spaces (`heart rate`) |
| `subject` | `Patient/{user id}` (no Patient resource is served yet) |
| `effectiveDateTime` | `start_date`, when the sample is an instant (no `end_date`, or the same) |
| `effectivePeriod` | `start_date` to `end_date`, when the sample covers an interval |
| `issued` | `ingested_at` |
| `valueQuantity` | `value`; `unit` is the storage unit as the REST API spells it, `system` UCUM and `code` its UCUM code (see [Units](#units)) |
| `dataAbsentReason` | instead of `valueQuantity` when the stored value is NaN or infinite |
| `device.display` | the provenance `device`, else `device_model`; left out when neither is known |
| `meta.source` | the provider the sample came through: `.../fhir/provider/{provider}` |
| `meta.tag` | the vendor that produced the sample (provenance `source`), in the source code system |
| `identifier` | the provider's own id of the sample (`external_id`), when it has one, with system `.../fhir/provider/{provider}/external-id` |
| `meta.profile` | the R4 vital signs profiles the Observation meets (see [Vital signs](#vital-signs)) |

A quantity type without a LOINC code (VO2 max, body fat mass, the provider indices, a name a
connector emits that the catalog does not declare yet...) is still served, coded with the
quantity type system alone: leaving it out would make the FHIR view silently smaller than the
REST one, and a client can always filter on the codes it knows. `metadata`, `source_name` and the
other provider fields of the REST sample are not mapped.

### Categories

`category` uses the HL7
[observation category](https://terminology.hl7.org/CodeSystem-observation-category.html) codes.
It is left out when none of them describes the measurement, rather than set to a wrong one.

| Category | Quantity types |
|---|---|
| `vital-signs` | `heart_rate`, `resting_heart_rate`, `respiratory_rate`, `oxygen_saturation`, `body_temperature`, `blood_pressure_systolic`, `blood_pressure_diastolic`, `body_mass`, `height`, `body_mass_index` |
| `laboratory` | `blood_glucose`, `blood_alcohol_content` |
| `activity` | steps, flights, distances, active energy, Apple exercise, move and stand time, push count, physical effort, walking and running speed, running and cycling power, running form metrics, strain and workout strain, and the other activity counts of the catalog (and their intraday variants) |
| none | everything else: body composition, heart rate variability, VO2 max, the other temperatures, readiness scores, audio exposure, respiratory function, insulin, falls, UV, electrodermal activity, vascular age, pulse wave velocity, and names outside the catalog |

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
| `blood_pressure_systolic`, `blood_pressure_diastolic` | quantity type system only | none, see below |

Every profiled Observation also names the base `vitalsigns` profile. A profile is claimed only
when the catalog gives the type the code above and the value is in a unit the profile accepts.

**Blood pressure.** Systolic and diastolic pressure are stored as two samples. R4 allows their
LOINC codes (8480-6, 8462-4) only as the two components of one blood pressure panel (85354-9),
so a separate systolic or diastolic Observation carrying them is invalid. Until the two samples
are paired into that panel (planned), each is served as its own Observation, in the
`vital-signs` category, coded with the quantity type system only. A `code` search for
8480-6 or 8462-4 therefore returns an empty Bundle.

### Units

`valueQuantity.code` is UCUM. It is the catalog's UCUM unit when the type has one, else the
spelling of the storage unit below; a unit outside this table (a type the catalog does not
declare) is kept in `unit` with no `system` or `code`.

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
the storage unit: nothing is converted on the way out.

### Identifiers Open Health Sync defines

Every URI below starts with `https://github.com/soma-intelligence/open-health-sync/fhir`. They
are identifiers, documented here; nothing is served at them.

| URI | Used for |
|---|---|
| `.../CodeSystem/quantity-type` | `code.coding`: the canonical quantity names of [data-model.md](data-model.md) |
| `.../CodeSystem/source` | `meta.tag`: the vendor that produced the sample |
| `.../provider/{provider}` | `meta.source`: the provider the sample came through |
| `.../provider/{provider}/external-id` | `identifier.system`: the provider's own sample ids |

No custom extension is used, so a validator needs nothing beyond the R4 specification.

## Validation

Each mapping case has a golden file in [internal/fhir/testdata](../internal/fhir/testdata): a
profiled vital sign of each kind, resting heart rate, systolic pressure, an interval, a
laboratory value, a type without LOINC code, a type and unit outside the catalog, a NaN value,
and searchset Bundles with and without entries. `go test ./internal/fhir` compares the mapping
with them (`-update` rewrites them), and checks they hold no empty array, object or string, which
FHIR JSON forbids. The contract tests check the same files against `api/openapi.yaml`.

`make fhir-validate` (`scripts/fhir-validate.sh`) runs the official HL7 validator on the golden
files with `-version 4.0.1`. It needs Java 17 or later and the network, and is not part of CI.
Every file passes with 0 errors. The warnings left are expected:

- no narrative (`dom-6`) and no `performer`: both best practice, neither required;
- the quantity type and source code systems are not published, so their codes cannot be checked;
- a UCUM annotation such as `{steps}` is flagged as best practice to review.

Run offline (`FHIR_VALIDATOR_TX=n/a`), the validator also says it cannot check the UCUM and LOINC
codes; with the default terminology server it checks them.

## Not in this version

Planned, in no fixed order:

- Sleep sessions, workouts, daily summaries, category samples and nutrition as FHIR resources.
- Pairing systolic and diastolic samples into the blood pressure panel.
- `Patient` and `Device` resources (today `subject` points to `Patient/{user id}` and `device`
  is a display name only).
- Bulk export (`$export`), a `CapabilityStatement`, OperationOutcome errors, the FHIR `date` and
  `_count` parameters, and access with a user token.
- Open mHealth schemas.
