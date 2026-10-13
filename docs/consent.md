# Consent

Open Health Sync keeps each end user's consent per source and per data category: a user can
share their sleep and activity from one wearable but not their heart data, and change their mind
later. The engine enforces it. Records of a category the user has not granted are dropped
before they are stored, and the records already stored in a category the user revokes are
deleted. Every change is kept in a history with who made it and what backs it, and announced by
webhook.

Your app collects the consent (a screen, a form) and tells Open Health Sync what the user
chose. Open Health Sync does not show consent screens itself.

## Categories

| Category | What it covers |
|---|---|
| `activity` | Steps, distances, energy, floors, workouts, daily summaries, speed and power |
| `location` | Workout GPS routes |
| `heart` | Heart rate, resting and walking heart rate, HRV, RR intervals, rhythm events |
| `ecg` | Electrocardiograms and their voltage samples |
| `blood_pressure` | Systolic and diastolic pressure |
| `glucose` | Blood glucose (CGM included), insulin delivery |
| `respiratory` | Respiratory rate, SpO2, VO2 max, lung function, inhaler use |
| `body` | Weight, height, body composition, body and skin temperature |
| `sleep` | Sleep sessions, phases and scores |
| `nutrition` | Food, drinks, water, caffeine |
| `reproductive` | Menstrual cycle, ovulation, basal body temperature, sexual activity, pregnancy |
| `mental` | Mindfulness, state of mind, mood, stress |
| `other` | Anything no category above describes |

A record goes with its parent: route points are `location` but also go when `activity` is not
granted (no workout, no route), and sleep phases go with their session. `GET /v1/users/{id}/consents`
returns the list of categories, so your consent screen can follow it.

## How it is enforced

- **No consent recorded for a source**: everything from it is stored. This keeps an existing
  install working as before.
- **`OHS_CONSENT_REQUIRED=true`**: a source with no consent recorded stores nothing. Set it when
  your app always asks for consent before connecting a source.
- **Consent recorded**: only the granted categories are stored. A category left out of the
  first consent counts as not granted.
- **Revocation**: from the moment it commits, new records of the category are dropped. The
  records already stored from that source are deleted by the etl role in the background
  (usually within seconds). Granting the category again before the purge runs keeps the data.
- **Raw payloads** (the files the engine keeps as received) are not split by category: they
  follow `OHS_RETENTION_DAYS` and are deleted with the user or with the provider's data. A
  reprocess of a raw file applies the consent current at that time.

Withheld records are not failures: they are counted in the ETL log line
`records withheld, no consent for their category`, never written to `failed_records`.

## Routes

All take a tenant API key.

```bash
# Grant heart and sleep from Oura; every other category of Oura is revoked.
curl -X PUT "$OHS_URL/v1/users/$USER_ID/consents/oura" \
  -H "Authorization: Bearer $OHS_KEY" -H "Content-Type: application/json" \
  -d '{"granted":["heart","sleep"],"evidence":"consent screen v3, 2026-10-13"}'

# The user's consent for every source, and the list of categories.
curl "$OHS_URL/v1/users/$USER_ID/consents" -H "Authorization: Bearer $OHS_KEY"

# Every change, newest first (paged with cursor and limit).
curl "$OHS_URL/v1/users/$USER_ID/consents/history" -H "Authorization: Bearer $OHS_KEY"

# Revoke everything from Oura.
curl -X DELETE "$OHS_URL/v1/users/$USER_ID/consents/oura?evidence=withdrawn%20in%20settings" \
  -H "Authorization: Bearer $OHS_KEY"
```

`PUT` takes the whole list: `granted` is required (`[]` for none) and `evidence` is optional,
up to 500 characters (a form version, a ticket number). The answer is the source's consent
after the change: each category with `granted`, `granted_at` and `revoked_at`.

A history event holds the source, the category, `granted` or `revoked`, the time, the id of
the API key that made the change and the evidence. Setting the same consent twice records
nothing. The audit log records each call as `consent.read` or `consent.write`
([audit-log.md](audit-log.md)).

## Webhooks

| Type | Sent when | `data` |
|---|---|---|
| `consent.granted` | One or more categories of a source were granted | `user_id`, `external_ref`, `provider`, `categories` |
| `consent.revoked` | One or more categories of a source were revoked | `user_id`, `external_ref`, `provider`, `categories` |

A first consent announces its grants only: the categories it leaves out were never granted.
See [webhooks.md](webhooks.md) for signatures and retries.

## Erasure

Consent, its history and queued purges belong to the user: they are deleted with the user
(`DELETE /v1/users/{id}`) and, for one source, with that source's data (a disconnect with
`delete_data=true`). The audit log keeps the record that the changes happened.
