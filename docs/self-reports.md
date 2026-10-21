# Self reports

A self report is a person's own rating: how they feel this morning, their mood, their stress,
their energy, how well they slept, how sore their muscles are, how hard a session was, how much
something hurts. Sensors say what the body did; a self report says how the person experienced
it. A coach's morning check-in, a study's daily wellbeing item and the labels a model learns
from all take this shape.

Open Health Sync stores each report as a quantity sample from the source `self_report`, next to
the synced data, so it is read, merged, exported, consented to and erased like any other sample.
The server only stores the ratings: it computes no score from them.

## Kinds

Each kind is a whole number on a fixed scale. The two anchors are the meaning of the lowest and
the highest value; the values in between are steps from one to the other.

| Kind | Stored as | Scale | Lowest | Highest | Consent category |
|---|---|---|---|---|---|
| `feeling` | `reported_feeling` | 1 to 5 | very bad | very good | `mental` |
| `mood` | `reported_mood` | 1 to 5 | very low | very good | `mental` |
| `stress` | `reported_stress` | 1 to 5 | none | very high | `mental` |
| `energy` | `reported_energy` | 1 to 5 | exhausted | full of energy | `mental` |
| `sleep_quality` | `reported_sleep_quality` | 1 to 5 | very poor | very good | `sleep` |
| `muscle_soreness` | `reported_muscle_soreness` | 1 to 5 | none | very sore | `activity` |
| `exertion` | `reported_exertion` | 0 to 10 | rest | maximal | `activity` |
| `pain` | `reported_pain` | 0 to 10 | no pain | worst imaginable | `other` |

The unit is `score`. The scales are plain numbers with these anchors, not the wording of a
licensed questionnaire. An app that asks a published instrument can still store its number
under the matching kind when the scale is the same.

## A report

| Member | Type | Notes |
|---|---|---|
| `id` | uuid | Your id of the report. Sending it again replaces the stored value. Absent, the server gives one |
| `kind` | string | Required, one of the kinds above |
| `value` | integer | Required, a whole number on the kind's scale |
| `start_at` | time | Required, RFC 3339, between 1900 and 2200. The moment the rating applies to |
| `end_at` | time or null | After `start_at`, for a rating of a period (a training session's effort). Absent or null for an instant |
| `timezone` | string or null | The IANA time zone the person was in (`Europe/Paris`), for their local day |

Times come back in UTC to the microsecond. Values are health data: they are never logged and
never quoted in an error message.

## Recording

`POST /v1/users/{id}/self-reports` takes 1 to 100 reports and answers 202 with the reports as
stored, each with its id:

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/self-reports -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"reports":[
    {"kind":"feeling","value":4,"start_at":"2026-03-01T08:30:00+01:00","timezone":"Europe/Paris"},
    {"kind":"exertion","value":7,"start_at":"2026-03-01T17:00:00Z","end_at":"2026-03-01T18:00:00Z"}]}'
```

```json
{
  "reports": [
    {"id": "0192f1a2-...", "kind": "feeling", "type": "reported_feeling", "value": 4,
     "start_at": "2026-03-01T07:30:00Z", "end_at": null, "timezone": "Europe/Paris"},
    {"id": "0192f1a2-...", "kind": "exertion", "type": "reported_exertion", "value": 7,
     "start_at": "2026-03-01T17:00:00Z", "end_at": "2026-03-01T18:00:00Z", "timezone": null}
  ],
  "duplicate": false
}
```

Who can call it:

- A tenant API key records reports for any user of the tenant: your server relays what the
  person answered in your app.
- A user token with the `upload` scope records reports for its own user only: the person's app
  posts directly, with the token it already uses for uploads. Another user is a 404.

The request is checked as a whole: one invalid report is a 400 naming it (`reports[2].value must
be a whole number from 1 to 5 for kind stress`) and nothing is stored. The reports then go
through the same path as a device upload: they are stored as one raw part of the `self_report`
provider, and the processing writes them within moments. A user being deleted, or another
tenant's user, is a 404; while the user's self reports are being erased the answer is 409 and
nothing is kept.

### Retries

The same reports sent again are the same part: the answer is 202 with `duplicate: true` and
nothing is written twice. A report without an `id` gets a new one on every call, so give each
report your own id, or send an `Idempotency-Key`: a retry with the same key gets the first
response back, with the ids it gave.

### Corrections

A report sent again with the same `id` replaces the stored one, the latest value wins. When its
`start_at` moved, the sample at the old time is replaced too, so one report is always one
sample. To remove every report of a user, erase the provider:
`DELETE /v1/users/{id}/data/self_report`. Removing a single report is not supported yet.

## Reading them back

Self reports need nothing new to read:

- `GET /v1/users/{id}/samples?type=reported_feeling`: the samples, with `provider` and `source`
  `self_report`, `was_user_entered` true and the report id as `external_id`.
- `GET /v1/users/{id}/samples/stats?type=reported_mood&interval=day&tz=Europe/Paris`: the daily
  mean, minimum and maximum of a rating.
- FHIR: each report is an Observation with category `survey` and the person as `performer`;
  `reported_pain` also carries LOINC `72514-3` (pain severity, 0 to 10 verbal numeric rating).
  `GET /v1/users/{id}/fhir/Observation?category=survey` finds them all. See [fhir.md](fhir.md).
- Exports and the user archive ([exports.md](exports.md)), and the MCP tools
  `list_quantity_types`, `get_samples` and `get_sample_stats` ([mcp.md](mcp.md)). The MCP server
  stays read only: an agent that asks the person how they feel posts the answer through this
  route with its own credentials.

Open mHealth has no schema for a generic rating scale, so self reports are not served as Open
mHealth data points.

## Consent

The consent category of each kind is in the table above: a user who granted `sleep` but not
`mental` for the `self_report` source keeps their sleep quality ratings and has their mood
ratings withheld. See [consent.md](consent.md).
