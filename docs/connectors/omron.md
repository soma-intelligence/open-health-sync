# OMRON connector

Slug `omron`. Users connect their OMRON connect account over OAuth; Open Health Sync pulls
their blood pressure and weight readings from the official OMRON Wellness API (the
[Partner API](https://omronhealthcare-ohi.atlassian.net/wiki/spaces/API/pages/2480308225/Omron+Partner+API+for+US))
on a schedule. You register your own app with OMRON: nothing is shared between instances.

Status: available. It is tested against a fake of the API built from the published
documentation. The points marked *to confirm* below are the ones to check first, in OMRON's
staging environment.

## 1. Get your keys

1. Apply at [omronhealthcare.com/api](https://omronhealthcare.com/api/). OMRON asks for a
   display name, the redirect URI, a notification URI, the scopes and your company details.
   Access is granted by OMRON, not self-serve.
2. Give `<OHS_PUBLIC_URL>/oauth/omron/callback` as the **redirect URI**.
3. OMRON sends a **client ID and secret for staging** first. Production credentials come
   separately, after OMRON approves going live.

In staging, `tool.omronwellness.com` creates test users and generates or uploads readings.
Staging and production users are separate.

## 2. Configure the instance

```sh
OHS_PUBLIC_URL=https://ohs.example.org
OHS_OMRON_CLIENT_ID=...
OHS_OMRON_CLIENT_SECRET=...
# Optional: production (default) or staging.
OHS_OMRON_ENVIRONMENT=staging
```

An unknown environment stops startup.

## 3. Scopes and tokens

The consent page asks for `bloodpressure weight openid offline_access`. `openid` gives an
`id_token` whose `sub` claim is the OMRON user id; `offline_access` gives the refresh token.
Access tokens last an hour. Each refresh returns a new refresh token, which replaces the stored
one. *To confirm:* OMRON's example refresh request also sends `redirect_uri`; the connector
does not, because the redirect is not known at refresh time.

## 4. Connect a user

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connections/omron \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected"}'
```

## 5. What is pulled

One endpoint, `POST /api/measurement` (form encoded), once per type:

| `type` | Raw data type | Canonical records |
|---|---|---|
| `bloodpressure` | `bloodpressure` | `blood_pressure_systolic` and `blood_pressure_diastolic` (mmHg), `heart_rate` for the pulse, `irregular_heart_rhythm_event` when OMRON flags a possible AFib |
| `weight` | `weight` | `body_mass`, `body_mass_index`, `body_fat_percentage`, `visceral_fat_rating` |

Pages are read oldest first (`sortOrder=asc`, `since` the day before the window), following
`paginationKey` until a page reaches the end of the window. A non-zero `status` in the answer
is an error.

**Notifications.** OMRON sends a notification to the notification URI of the app when a user
uploads readings, and asks partners to pull after it, since the measurement API is rate limited.
The notification format is not in the public documentation, so this connector does not receive
notifications yet: data arrives with the scheduled pulls, every 3 hours. Readings reach OMRON
when the phone app syncs, often days after they were taken, so each pull re-reads the last 72
hours and the first pull of each UTC day the last 14 days
([sync policy per provider](../background-sync.md#sync-policy-per-provider)). Once the format is known, a
`HandleInbound` that queues a pull for the user is a small addition.

Not pulled yet: activity (steps), temperature and SpO2 (Europe only).

## 6. Mapping

- **Time**: `dateTime` is UTC. `dateTimeUtcOffset` (`+02:00`, or seconds such as `-21600`)
  gives `metadata.utc_offset_minutes` and, for whole hours, `metadata.timezone`.
- **Values**: OMRON may send numbers as strings (`"71.6"`); both are read. `-1` means "not
  measured" and gives no sample.
- **Blood pressure panel**: the systolic and diastolic samples share time, source and device
  (`deviceType`), so the read API and the [FHIR output](../fhir.md) pair them into one blood
  pressure panel (LOINC `85354-9`).
- **Possible AFib**: OMRON then sets systolic, diastolic and pulse to 0. These are not readings:
  no pressure is stored, and an `irregular_heart_rhythm_event` category sample records the flag.
  `metadata.possible_afib`, `irregular_heartbeat`, `movement_detected` and `cuff_wrap_ok` keep
  the flags OMRON sends.
- **Weight unit**: `weightPref` (`kg` or `lbs`); pounds are converted to kg by the pipeline.
- **Identity**: `external_id` is `<OMRON id>_<figure>` (`_systolic`, `_diastolic`, `_pulse`,
  `_weight`, ...).
