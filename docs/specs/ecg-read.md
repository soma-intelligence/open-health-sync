# Spec: ECG read

Status: shipped in v0.2.0 (built 2026-10-14). Guides: [api.md](../api.md#ecg-recordings),
[fhir.md](../fhir.md#ecg-recordings), [mcp.md](../mcp.md#tools), [exports.md](../exports.md).

## Problem

Apple Health and Withings ECG recordings are stored (`ohs_data.ecg_recordings`, one row per
recording, and `ohs_data.ecg_samples`, one row per voltage sample) but no route serves them. The
data model says "no read route yet", the user archive holds them only as the raw payloads they
came in, and the FHIR view leaves them out. An app cannot show a person their own recording, and
a person cannot hand it to a clinician's system in a standard form.

## Scope

1. REST, tenant key, like every read:
   - `GET /v1/users/{id}/ecg`: the recordings, metadata only, ordered by `start_date` then id,
     `start` and `end` on `start_date`, paged with `cursor` and `limit` like every list.
   - `GET /v1/users/{id}/ecg/{ecg_id}`: one recording with its voltage samples as stored
     (offsets in seconds and voltages in microvolts, two arrays of the same length).
2. FHIR R4: a recording is an Observation `ecg-{id}` with the waveform in
   `component.valueSampledData`. It is listed by the Observation search (and found by its own
   `code` values), read by id, paged by `$everything`, has its Provenance, and its device (when
   the row names one) is in the Device search.
3. MCP: `get_ecg_recordings` lists recordings, metadata only; with `recording_id` it returns a
   summary of one recording (stored sample count, first and last offset, lowest and highest
   voltage), never the voltage array: an agent does not need 15,000 numbers.
4. Exports: dataset `ecg` (CSV and Parquet, metadata only). User archive: `canonical/ecg.ndjson`
   (each recording with its samples, as the read by id) and the ECG Observations in
   `fhir/bundle.json`.

Out of scope: Open mHealth (no ECG schema in the catalog), heartbeat (RR) series in the archive
(their own change), interpretation of any kind, a writer change to fill `device_id`.

## What is served is the device's recording

Open Health Sync passes the recording through. It computes no rhythm, no interval and no finding.
The classification is the one the recording device or its app made, in the maker's own
vocabulary, and the docs say so wherever it appears. `classification_value` keeps the stored
integer, so nothing is lost when a value is unknown.

| Provider | Stored integer | `classification` |
|---|---|---|
| `apple_health` | HealthKit `HKElectrocardiogram.Classification` raw value: 0, 1, 2, 3, 4, 5, 6, 100 | `not_set`, `sinus_rhythm`, `atrial_fibrillation`, `inconclusive_low_heart_rate`, `inconclusive_high_heart_rate`, `inconclusive_poor_reading`, `inconclusive_other`, `unrecognized` |
| `withings` | the recording's AFib result: 0, 1, 2 | `negative`, `positive`, `inconclusive` (the existing `ecg-afib-classification` codes) |
| any other value or provider | as stored | null |

`symptoms_status` is HealthKit's: 0 `not_set`, 1 `none`, 2 `present` (Withings rows store 0).

## FHIR mapping

| Observation | From the recording |
|---|---|
| `id` | `ecg-` and the row id |
| `code` | LOINC 11524-6, then the record type `ecg_recording` |
| `category` | none (see open questions) |
| `effectivePeriod` | `start_date` to `end_date` (`effectiveDateTime` when there is no end) |
| `issued`, `meta`, `subject`, `device` | as for the other records |
| `component` voltage | MDC 131328 and the component code `voltage`; `valueSampledData` with `origin` 0 `uV`, `period` 1000 / sampling frequency in ms, `dimensions` 1, `data` the voltages in order, rounded to 0.001 microvolt, `E` for a value that is not a finite number |
| `component` average heart rate | LOINC 8867-4 and `average_heart_rate`, `/min` |
| `component` classification | `device_classification`, `valueCodeableConcept` in the maker's code system, text "classification by the recording device"; left out when `not_set` or unknown |
| `component` symptoms | `symptoms_status`, `none` or `present`; left out when `not_set` |

No `value[x]` on the Observation itself: the components carry the values.

### Code verification (read 2026-10-14)

| Code | Source checked | Decision |
|---|---|---|
| LOINC 11524-6 "EKG study" | loinc.org/11524-6: active, class CARD.PROC, method EKG, scale Doc | Used as `Observation.code`, the recording as a whole |
| MDC 131328 `MDC_ECG_ELEC_POTL` | HL7 FHIR R4 example `observation-example-sample-data` (OID form `urn:oid:2.16.840.1.113883.6.24`); HL7 Point-of-Care Device IG example `ECGSampleArrayObservation` (URI form `urn:iso:std:iso:11073:10101`) | Used for the waveform component, URI form |
| MDC 131329 `MDC_ECG_ELEC_POTL_I` (lead I) | same two examples | Not used: the stored rows do not name the lead. HealthKit calls Apple Watch's lead "similar to lead I"; the Withings fields stored here name none. Naming a lead would be our claim, not the device's |
| LOINC 8867-4 heart rate | already used by the catalog; R4 vital signs | Average heart rate component |
| LOINC waveform code | none found: LOINC codes per-lead measurements (wave amplitudes) and the study, not the raw signal | MDC on the component instead |

The HL7 Personal Health Record IG (`hl7.fhir.uv.phr`, 1.0.0-ballot2) has a
`pghd-electrocardiogram` profile with classification codes taken from HealthKit and category
`exam`. It is a ballot, not a published release, so its code systems are not used here; our
classification codes use the same HealthKit names.

`make fhir-validate` runs the HL7 validator on the golden files, including the new
`observation-ecg-recording.json`. CI runs it on the pull request.

## Requirements

- **Auth.** REST: tenant key (the read routes take no user token). MCP: tenant key or user token
  with `read`, like every tool. The archive keeps its tenant key or own `read` token.
- **Audit.** The routes sit under `/v1/users/{id}/`: `data.read` for the reads, `data.export`
  for the export and the archive, with no change to the classifier.
- **Consent.** ECG is the `ecg` category: records outside a grant are dropped before they are
  stored and a revoked category's recordings and samples are purged (existing). A read serves
  what is stored, as every read does.
- **Deletion.** A user's deletion and a provider erasure already delete both tables; tests
  cover them.
- **Tenant isolation.** Every query filters `tenant_id` and `user_id`; another tenant's user or
  recording is a 404.
- **Bounds.** A list page holds metadata only (at most 1000). One recording serves at most
  100000 samples (`samples.truncated`; an Apple Watch recording has about 15360, a Withings one
  about 9000). A FHIR page holds at most 100 ECG Observations and ends early with a `next` link
  when more follow, so a page of ECGs never loads millions of samples.
- **Indexes.** `ecg_recordings_user_date (tenant_id, user_id, start_date DESC)` serves the list,
  `ecg_recordings_id` the read by id, `ecg_samples_ecg (ecg_id, ts)` the samples, with `ts`
  bounded to the recording's span so TimescaleDB reads only its chunks. No migration.
- **No new dependency.**

## Open questions

- `category`: left out. HL7 `procedure` names electrodiagnostics; the PHR ballot uses `exam`.
- LOINC 11524-6 has a document scale. The alternative is no LOINC code (MDC and the record type
  only); the code search then loses its LOINC handle.
- The lead: map HealthKit's "similar to lead I" to MDC 131329 for `apple_health` rows?
- The writer never fills `ecg_recordings.device_id` (Withings keeps the device id and model in
  metadata), so ECG Observations name no Device today.
