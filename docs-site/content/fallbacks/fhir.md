---
title: "FHIR R4 export"
description: "Read health data as FHIR R4 resources. Planned, not in this release."
status: "planned"
icon: "HeartPulse"
source: "docs-site/content/fallbacks/fhir.md"
---

FHIR R4 output is **planned**: it is not in the current release, and no FHIR route answers
today. The REST API and the MCP server are the outputs that ship.

What is planned first: a user's quantity samples (heart rate, steps, weight, SpO2 and the rest)
served as FHIR R4 Observations, with their LOINC codes and UCUM units, in a `searchset` Bundle
paged with the same cursor as the REST API, and checked against the official HL7 validator.

The canonical model already carries what that needs: every quantity name has a UCUM unit and a
LOINC code where one exists. See the [data model](/docs/api-reference/data-model).

Follow the roadmap in the [changelog](https://github.com/soma-intelligence/open-health-sync/blob/main/CHANGELOG.md)
and the [releases](https://github.com/soma-intelligence/open-health-sync/releases). Until then,
map `GET /v1/users/{id}/samples` to Observations on your side: `type` gives the code, `value` and
`unit` the `valueQuantity`, and `start_date` and `end_date` the effective period.
