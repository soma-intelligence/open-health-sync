# SMART on FHIR connector (health records)

Slug `smart_fhir`. A patient connects the patient portal of their health system (Epic MyChart,
Oracle Health and any other EHR certified in the US) and Open Health Sync pulls their record from
the FHIR R4 API that the 21st Century Cures Act requires those systems to open to patients. The
flow is a [SMART App Launch](https://hl7.org/fhir/smart-app-launch/) standalone patient launch
with PKCE, and the resources follow [US Core](https://hl7.org/fhir/us/core/).

Status: preview. It is tested against a fake server and, end to end, against the public
[SMART Health IT sandbox](https://launch.smarthealthit.org) (synthetic patients). The Epic
sandbox is the next check.

## What it pulls

| Resource | How | Stored as |
|---|---|---|
| `Observation` | the window of each pull, by effective date | raw, then mapped (below) |
| `Patient` | whole, at each recent pull | raw only |
| `Condition`, `AllergyIntolerance`, `MedicationRequest`, `Immunization`, `Procedure`, `DiagnosticReport` | whole, at each recent pull | raw only |

Observations are searched for every category at once; a server that requires a category (Epic)
is asked for `vital-signs` and `laboratory` instead. The chart resources are read whole at each
pull whose window ends within 48 hours, and an unchanged chart is recognised and not stored
again.

`Map` turns each Observation into records:

- A vital sign with a LOINC code Open Health Sync knows (heart rate, blood pressure, weight,
  height, BMI, temperature, SpO2, respiratory rate, blood glucose, body fat) and a unit that
  converts becomes a quantity sample (`blood_pressure_systolic`, `body_mass`...). A blood pressure
  panel gives one sample per component.
- Everything else with a value (labs, coded results, scores) becomes a generic observation with
  `type_concept` `ehr`, coded `loinc:<code>` (or `<system>|<code>` without LOINC), its value,
  UCUM unit, and in metadata the category, interpretation (`H`, `L`...) and reference range.
- An Observation `entered-in-error` deletes the samples stored from it.

A search the grant does not cover (403) or the server does not offer (400, 404) is skipped with
a warning; the rest of the pull goes on.

## 1. Register an app

For Epic: create an account on [fhir.epic.com](https://fhir.epic.com), then **Build Apps**:

1. Audience **Patients**. SMART on FHIR version R4, SMART scope version 1.
2. Incoming APIs: `Patient.Read (R4)`, `Observation.Search (Vital Signs) (R4)`,
   `Observation.Search (Labs) (R4)`, `Condition.Search (Problems) (R4)`,
   `AllergyIntolerance.Search (R4)`, `MedicationRequest.Search (R4)`,
   `Immunization.Search (R4)`, `Procedure.Search (R4)`, `DiagnosticReport.Search (R4)`.
3. Redirect URI: `<OHS_PUBLIC_URL>/oauth/smart_fhir/callback` (`ohs doctor` prints it). Epic
   accepts `http://localhost` for the sandbox.
4. Copy the **non-production client ID**. A new client ID can take a while to reach the sandbox.

A public client (no secret) gets no refresh token from Epic: the connection then lasts as long as
the access token (about an hour) and the user connects again. A confidential client gets one.

## 2. Configure the instance

```sh
OHS_PUBLIC_URL=http://localhost:8080
OHS_SMART_FHIR_CLIENT_ID=...
OHS_SMART_FHIR_FHIR_BASE_URL=https://fhir.epic.com/interconnect-fhir-oauth/api/FHIR/R4
# Optional
OHS_SMART_FHIR_CLIENT_SECRET=...   # a confidential client
OHS_SMART_FHIR_PKCE_SECRET=...     # keys the PKCE verifier of a public client; set it when
                                   # several processes serve the API
OHS_SMART_FHIR_SCOPES="launch/patient openid fhirUser patient/*.read"
OHS_SMART_FHIR_AUTHORIZE_URL=...   # with TOKEN_URL, replaces the discovery
OHS_SMART_FHIR_TOKEN_URL=...
```

The OAuth endpoints are read from `<FHIR_BASE_URL>/.well-known/smart-configuration`. An instance
talks to one FHIR server; a tenant can set its own keys, and so its own server.

## 3. Test it

```sh
go test ./connectors/smartfhir/
# The whole launch against the public SMART sandbox (network, no account):
go test -tags live -run TestLive_SMARTLauncher -v ./connectors/smartfhir/
```

Epic sandbox test patients and their MyChart logins are listed on
[fhir.epic.com/Documentation?docId=testpatients](https://fhir.epic.com/Documentation?docId=testpatients).

## Limits

- One health system per instance (or per tenant). Picking a health system per user, from the
  endpoint lists Epic and the other vendors publish, is not built yet.
- Backfill reaches one year, the scheduler's bound; a manual sync 30 days.
- Only Observations are mapped. The other resources are kept raw for a later mapping.
- Records dated before 1990 are refused by the validator.
- A record is health data under HIPAA (for a covered entity), the FTC Health Breach
  Notification Rule and GDPR article 9; in France its host needs HDS certification.
