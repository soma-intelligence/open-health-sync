# Lab results

A lab result is one test of one specimen: a glucose, an LDL cholesterol, an HbA1c, a urine
culture. It has a test code, a value, a reference range, a flag, a status and the times the
specimen was taken and the result was issued. Results come grouped in reports (a lipid panel,
a blood count, an order), and a lab may send a result again: preliminary first, then final,
then amended or corrected.

Open Health Sync takes lab results from your backend or the person's app, as FHIR R4 or as a
simple JSON form, and stores them next to the synced data. They are read, exported, served as
FHIR, consented to, retained and erased like any other record. The server interprets nothing:
it keeps the flag the lab gave and computes no score.

## A result

| Member | Notes |
|---|---|
| `id` | Your id of the result, required. A result sent again with the same `id` replaces the stored one |
| `loinc` | The LOINC code (`2093-3`), when the source has one |
| `code`, `system`, `display` | The source's own code, its code system and the test's name. `loinc` or `code` is required |
| `value`, `unit`, `comparator` | A numeric value, its UCUM unit (`mg/dL`, `mmol/L`, `10*3/uL`) and a bound (`<`, `<=`, `>=`, `>`) |
| `value_string` | A result given as text (`positive`, `see note`) |
| `value_code`, `value_system` | A coded result (a SNOMED CT organism) |
| `reference_low`, `reference_high`, `reference_text` | The reference range, in the result's unit |
| `interpretation` | `H`, `L`, `N`, `A` (high, low, normal, abnormal) or their critical forms `HH`, `LL`, `AA`. `critical` is taken as `AA` |
| `status` | `preliminary`, `final`, `amended`, `corrected` or `cancelled`. Defaults to the report's, then `final` |
| `specimen` | The specimen, as text (`Serum`) |
| `collected_at`, `collected_end` | When the specimen was taken (and the end of a collection period, a 24 hour urine). Required here or on the report |
| `issued_at` | When the lab issued the result |
| `performer` | The lab that ran the test. Defaults to the report's |
| `absent` | Why there is no value (a FHIR data absent reason code, `not-performed`) |

At most one of `value`, `value_string` and `value_code`. None is accepted only for a cancelled
result or one with `absent`. Times are RFC 3339 between 1900 and 2200.

A report holds `id`, `loinc` or `code` (the panel), `status`, `collected_at`, `issued_at`,
`performer`, `orderer` (who ordered the tests) and its `results`. Its status, collection time,
issued time and performer are the defaults of its results.

Values are health data: they are never logged and never quoted in an error message.

## Uploading

`POST /v1/users/{id}/labs` (scope `data:write`) takes either form and answers 202:

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/labs -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"reports":[{
    "id":"order-8812","loinc":"24331-1","display":"Lipid panel",
    "collected_at":"2026-03-04T08:30:00+01:00","performer":"City Lab",
    "results":[
      {"id":"order-8812-chol","loinc":"2093-3","value":5.2,"unit":"mmol/L","reference_high":5.0,"interpretation":"H"},
      {"id":"order-8812-hdl","loinc":"2085-9","value":1.4,"unit":"mmol/L","reference_low":1.0}]}]}'
```

```json
{
  "reports": [
    {"id": "order-8812", "results": [
      {"id": "order-8812-chol", "code": "lab:loinc:2093-3", "status": "final"},
      {"id": "order-8812-hdl", "code": "lab:loinc:2085-9", "status": "final"}]}
  ],
  "duplicate": false
}
```

Who can call it:

- A tenant API key uploads for any user of the tenant: your backend relays what a lab portal or
  an EHR sent it.
- A user token with the `upload` scope uploads for its own user only: the person's app posts
  directly. Another user is a 404.

The body is checked as a whole: one invalid result is a 400 naming it
(`reports[0].results[1].status must be one of preliminary, final, amended, corrected, cancelled`)
and nothing is stored. Limits: 100 reports, 500 results per report, 2000 results in all, ids of
at most 200 bytes, a body of at most 1 MiB (413 past it). The body is then stored as one raw
part of the `labs` provider, sealed when the vault is on, and processed within moments like a
device upload. While the user's lab results are being erased the answer is 409 and nothing is
kept.

### FHIR R4

A body with a `resourceType` is read as FHIR R4 (`application/json` or `application/fhir+json`):

- a `Bundle` (any type) of DiagnosticReports and Observations;
- a `DiagnosticReport`, with its Observations in `contained`;
- an `Observation`, sent alone.

A DiagnosticReport must have a laboratory category: a v2-0074 lab section (`LAB`, `CH`, `HM`,
`MB`, `SP` and the other laboratory sections) or the observation category `laboratory`. An
Observation must be of category `laboratory`. Anything else is a 400 that says so
(`category must be laboratory: only laboratory results are accepted here`): imaging, vital
signs and notes have their own routes or none.

How the elements map:

| FHIR | Stored as |
|---|---|
| `DiagnosticReport.id` (else its first identifier as `system|value`) | report `id` |
| `DiagnosticReport.code` | the panel's `loinc` and `code` |
| `DiagnosticReport.status` | `registered` and `partial` as `preliminary`, `appended` as `amended`, the others as is |
| `DiagnosticReport.effective[x]`, `.issued`, `.performer` | the report's defaults |
| `DiagnosticReport.basedOn` (a ServiceRequest) `.requester` | `orderer` |
| `DiagnosticReport.result` | its results, resolved by `fullUrl`, `Type/id`, the tail of an absolute URL or `#id` in `contained` |
| `Observation.id` (else its first identifier) | result `id` |
| `Observation.code` | `loinc` (the `http://loinc.org` coding), `code` and `system` (the first other coding), `display` |
| `valueQuantity` | `value`, `comparator` and `unit` (the UCUM `code`, else the unit text) |
| `valueString`, `valueBoolean` | `value_string` |
| `valueCodeableConcept` | `value_code` and `value_system` (its first coding), else its text as `value_string` |
| `valueInteger` | `value` |
| `referenceRange[0]` | `reference_low`, `reference_high`, `reference_text` |
| `interpretation[0]` | `interpretation` when a v3 ObservationInterpretation flag above, else its text |
| `specimen` | its display, else the type of the Specimen it references |
| `effective[x]` | `collected_at` (and `collected_end` for a period) |
| `issued`, `performer`, `dataAbsentReason` | `issued_at`, `performer`, `absent` |
| `hasMember` on an Observation without a value | the member Observations, each as a result |

Without any effective time on the result or its report, the issued time stands in for the
collection time and `issued_as_collected` is true.

Refused with a 400: an Observation with `component` (send each component as its own
Observation), a `value[x]` other than the five above, a status other than the five result
statuses (`entered-in-error` included), a report result that is not in the upload, and the
same Observation referenced twice. Only the first reference range and the first interpretation
are kept.

### Retries, amendments and corrections

The same body sent again is the same part: 202 with `duplicate: true`, nothing written twice.
An `Idempotency-Key` also works, as on every write.

A result is stored once per `id`. A result sent again with the same `id` (an amended copy, a
correction, a cancellation) replaces the stored one whole when its upload is newer: the latest
upload wins, by the time the server received it. An older upload processed again later (a
replayed job) never brings an earlier value back. When the collection time of the new copy
moved, the result moves with it. A cancelled copy clears the earlier value. The stored row id
changes when a result is replaced.

A result that a later version of its report no longer lists stays stored. To remove every lab
result of a user, erase the provider: `DELETE /v1/users/{id}/data/labs`.

## Unit normalization

Each result keeps its value and unit as sent. For a few analytes the server also stores the
value in one canonical unit, `normalized_value` and `normalized_unit`, so results from labs
that report in different units compare. It converts only where the conversion is exact (a
fixed molar mass or a defined equation) and never guesses: a unit it does not know exactly, a
LOINC code outside the table or a result without a LOINC code is left without a normalized
value. Units match exactly, case included (`mmol/L`, not `mmol/l`).

| Analyte | LOINC codes | Canonical unit | From | Conversion |
|---|---|---|---|---|
| Glucose | 2345-7, 2339-0, 1558-6, 14749-6, 15074-8 | mg/dL | mmol/L | × 18.0156 (180.156 g/mol) |
| Cholesterol (total, HDL, LDL) | 2093-3, 14647-2, 35200-5, 2085-9, 13457-7, 18262-6, 2089-1 | mg/dL | mmol/L | × 38.665 (386.65 g/mol) |
| HbA1c | 4548-4, 17856-6, 59261-8 | % | mmol/mol | ÷ 10.929 + 2.15 (IFCC to NGSP master equation) |
| Creatinine | 2160-0, 38483-4, 14682-9 | mg/dL | umol/L | ÷ 88.40 (113.12 g/mol) |
| Vitamin D (25-hydroxy) | 1989-3, 62292-8, 14635-7 | ng/mL | nmol/L | ÷ 2.496 (400.64 g/mol) |

A value already in the canonical unit is copied. Triglycerides are not normalized: their
molar mass depends on the fatty acids, so there is no exact factor. The reference range is
never converted.

## Reading them back

- `GET /v1/users/{id}/labs` (`data:read`): one result per `id`, by collection time, in keyset
  pages. Filters: `code` (a LOINC code, or the source's own code), `report` (a report id) and
  the collection time, `start` and `end`. Each result carries its
  report id, both codes, the value as sent, the normalized value, the range, the flag,
  `critical` (true for `HH`, `LL`, `AA`), the status, the times, the lab, the orderer and the
  provenance (`provider` and `source` are `labs`).
- `GET /v1/users/{id}/labs/reports/{report_id}`: one report with its results. Its panel code,
  status, issued time and orderer come from its most recently uploaded result.
- FHIR ([fhir.md](fhir.md)): each result is an Observation `lab-<id>` of category
  `laboratory`, with its LOINC and source codes, `valueQuantity` (with its comparator),
  `valueString` or `valueCodeableConcept`, `referenceRange`, `interpretation`, `specimen`,
  `issued` and the lab as `performer`; a result without a value has a `dataAbsentReason`.
  `Observation?category=laboratory` and `Observation?code=http://loinc.org|2093-3` find them.
  Each report is a DiagnosticReport `labreport-<hash>` (the first 40 hex digits of the SHA-256
  of its id, so no source id is in the URL) with category `LAB`, its source id as
  `identifier` and a reference to each result: `GET .../fhir/DiagnosticReport` lists them,
  `GET .../fhir/DiagnosticReport/{id}` reads one, and `$everything` includes the reports of
  each page's results. The normalized value has no FHIR element and is left out.
- Exports: the `labs` dataset (`GET /v1/users/{id}/exports/labs`, CSV or Parquet) and a
  `labs` file in the user archive ([exports.md](exports.md)).
- MCP: the tool `get_lab_results`, with the same filters ([mcp.md](mcp.md)).

Lab results are not served as Open mHealth data points and cannot be put in a share link yet.

## Consent, retention and erasure

Lab results have their own consent category, `labs`: a user who revokes it for the `labs`
source has their lab results withheld from every read and purged like any other category
([consent.md](consent.md)). Retention prunes them by collection time, erasing the user or the
`labs` provider removes them, and they are counted in coverage under `lab_result`.

## Storage

Each result is one row of `ohs_data.observations`, with the code `lab:loinc:<code>` (or
`lab:local:<code>`) and the provider and source `labs`. Migration `0055` adds three clear
columns: `external_id` (the result id), `report_id` and `captured_at` (when the upload holding
this copy was received, which orders two copies of one result). With the vault on, the value,
the normalized value and the details (range, flag, status, specimen, times, lab, orderer, the
report's fields) are sealed; the test code, the unit, the collection time and the ids stay
clear, like the code and the unit of any observation.
