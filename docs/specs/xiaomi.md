# Spec: Xiaomi

Status: shipped in v0.2.0 (built 2026-10-13). Guide: [connectors/xiaomi.md](../connectors/xiaomi.md).

## Problem

Xiaomi ships more wrist wearables than almost any other vendor (Xiaomi Smart Band, Redmi Watch,
Xiaomi Watch). Their data lives in the Mi Fitness app and the Xiaomi cloud (Zepp Life for Mi
Band 7 and earlier). A developer whose users wear Xiaomi devices today gets that data only if the
app happens to sync it to Health Connect or Apple Health, gets no history from before the sync
was turned on, and cannot tell Xiaomi data apart from other apps' in Health Connect.

## Integration paths, as checked on 2026-10-13

| Path | Verdict |
|---|---|
| Public cloud API with OAuth (like Withings, Oura) | Does not exist for Mi Fitness |
| Xiaomi Health Cloud SDK (dev.mi.com, `micloud/health`) | An Android SDK behind a Xiaomi developer app, undated, with no list of data types and no sign that it reads Mi Fitness data of global accounts. Not pursued; to revisit if Xiaomi documents it |
| Private Mi Fitness cloud API, used by community tools | Reverse engineered, needs the user's session token, breaks without notice. Rejected |
| Health Connect (Android) and Apple Health (iPhone) | Mi Fitness, Mi Health and Zepp Life write there. Already supported; missing: Xiaomi attribution |
| Mi Fitness data copy (Xiaomi account privacy center) | Official, self-serve, full history, CSV tables documented by Xiaomi's own guide (column names, not value shapes). Chosen for history |

## Scope

1. Health Connect: records whose `dataOrigin` is `com.xiaomi.wearable` (Mi Fitness),
   `com.mi.health` (Mi Health) or `com.xiaomi.hm.health` (Zepp Life) get source slug `xiaomi`.
2. A device connector `xiaomi` with the route `POST /v1/ingest/xiaomi`: one NDJSON line per row of
   the copy's `hlth_center_fitness_data` and `hlth_center_sport_record` tables. Same upload contract,
   limits and idempotency as the Apple Health route.
3. `ohs import mi-fitness <folder|zip>`: reads the copy, joins the device model from
   `hlth_center_data_source`, drops the account id and the user's device alias, uploads parts with
   a batch id derived from the files.
4. Mapping of heart rate, resting heart rate, SpO2, steps, distance, active energy, weight, body
   fat, VO2 max, sleep with stages and workouts, per the table in the guide.

Out of scope: stress, HRV (method not stated by Xiaomi), temperature, blood pressure, daily
aggregates, GPS tracks (the copy holds only expiring links), profile and goal tables, Zepp Life's
own export (a different vendor format).

## Standards

- Units follow the canonical storage units (UCUM in FHIR): `bpm`, `percent`, `count`, `m`, `kcal`,
  `kg`, `mL/kg/min`.
- Sleep stages map to the canonical phases (deep, light, REM, awake).
- Provenance: provider and source slug `xiaomi`, source name `Mi Fitness`, device model, and the
  copy's table, key, data source id and update time in metadata.

## Requirements

- Every connector item: auth flow (user-requested copy plus a user token, no operator keys), rate
  limit strategy (no provider calls; the shared in-flight upload limit and client retries),
  backfill strategy (the copy is the full history; ongoing data through Health Connect or Apple
  Health), mapping table and provenance source: all in the guide.
- Privacy: the account id and the device alias never leave the user's machine; the command
  prints counts and key names only.
- Idempotency: a re-import sends the same batch and parts (answered as duplicates); a newer copy
  updates rows in place (samples are hashed on data source, key and time).
- Tests use an invented copy, never real user data.

## Open questions

- Value shapes beyond the published examples (`heart_rate`, `valid_stand`, `sport_record`) come
  from community documentation of the same JSON objects. Fields that differ in a real copy are
  skipped, never misread: unknown keys stay raw.
- Whether the copy can arrive as a password protected zip; the importer reads folders and plain
  zips.
