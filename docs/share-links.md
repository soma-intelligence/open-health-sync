# Share links

> **Next release.** This is on `main` and ships in the next release; the 0.1.0 image does not have it.

A share link lets a person show some of their own data to someone they choose (a physician, a
coach, a relative) without handing over a credential or a file. It opens a read-only snapshot
of the categories and the time window the person picked, encrypted end to end, expiring when
they said, optionally behind a passcode, revocable at any time, with every access recorded.

Links follow SMART Health Links (HL7 FHIR IG "SMART Health Cards and Links", STU1), so any viewer
that implements it can open one, and the instance serves a small viewer of its own. The file is
a FHIR R4 Bundle of the same Observations the [FHIR routes](fhir.md) serve. It is the person
sharing their own data as their devices recorded it: Open Health Sync interprets nothing and is
not clinical software.

## How it works

1. The person asks your app for a link. Your backend calls `POST /v1/users/{id}/shares` with the
   categories, the window, the expiry and, if the person wants one, a passcode.
2. The server reads the records, writes them as one FHIR Bundle, compresses it and encrypts it
   with a fresh random key, and stores the ciphertext only. It answers the link (`uri`) and the
   built-in viewer's link (`viewer_url`), once. Both carry the key, and the server keeps neither.
3. The person passes the link on: a message, an email, a QR code your app draws from
   `viewer_url`. A passcode travels another way (said out loud, another channel).
4. The reader opens it. The viewer sends the reader's name (and the passcode) to the instance,
   gets the encrypted file back and decrypts it in the browser with the key from the link. The
   key never reaches the instance.

## Make a link

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/shares -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"categories":["heart","sleep"],"start":"2026-09-01","end":"2026-10-01",
       "expires_at":"2026-10-21T00:00:00Z","passcode":"maple-orbit-42","label":"For my GP"}'
```

```json
{
  "id": "0199e1b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b",
  "label": "For my GP",
  "categories": ["heart", "sleep"],
  "start": "2026-09-01T00:00:00Z",
  "end": "2026-10-01T00:00:00Z",
  "expires_at": "2026-10-21T00:00:00Z",
  "passcode": true,
  "passcode_failures": 0,
  "status": "active",
  "revoked_at": null,
  "revoked_reason": null,
  "observations": 1874,
  "file_bytes": 61240,
  "accesses": 0,
  "last_accessed_at": null,
  "created_at": "2026-10-14T09:30:02.118Z",
  "uri": "shlink:/eyJ1cmwiOiJodHRwczovL29ocy5leGFtcGxlLm9yZy9zaGwv...",
  "viewer_url": "https://ohs.example.org/shl/view#shlink:/eyJ1cmwiOiJodHRwczovL29ocy5leGFtcGxlLm9yZy9zaGwv..."
}
```

`uri` and `viewer_url` are in this answer only (sent with `Cache-Control: no-store`): hand them to
the person's app and do not store them, since whoever holds one reads the snapshot. A lost link
cannot be shown again; revoke it and make another.

| Member | Required | Notes |
|---|---|---|
| `categories` | yes | One or more of the categories below |
| `start`, `end` | yes | The window, `start <= record time < end`; RFC 3339 or `YYYY-MM-DD` (midnight UTC); `end` after `start` |
| `expires_at` | yes | In the future, at most 365 days ahead |
| `passcode` | no | 6 to 64 characters, no control character. The server keeps a PBKDF2-SHA256 hash only |
| `label` | no | At most 80 characters, one line, shown to the reader. It travels inside the link, readable by whoever holds it: no health detail |

A request that breaks a rule, or a share that would hold more than 50000 records, is a 400 that
says why; an unknown user is a 404. The snapshot is read and stored in one transaction that
holds the user and their consent: a consent change or an erasure that starts meanwhile waits for
it, then revokes the new link if it holds what they remove. The route takes no `Idempotency-Key`
(the stored response would keep the key), so a retry after a lost answer makes a second link;
revoke the one you did not get.

## What a link holds

One FHIR R4 Bundle of type `collection`, built from the merged view (when several sources
recorded the same thing, the user's preferred one; see [api.md](api.md)):

| Category | Records |
|---|---|
| `activity` | Activity quantity samples (steps, distance, energy...), workouts, daily summaries |
| `heart` | Heart rate, resting heart rate, heart rate variability and the other heart samples |
| `blood_pressure` | Blood pressure readings, a systolic and diastolic pair as one panel |
| `glucose`, `respiratory`, `body`, `other` | The quantity samples of the category |
| `sleep` | Sleep sessions, and the quantity samples of the category |

A metric belongs to the category the [consent model](consent.md) gives it, so a share and a
consent agree on what `heart` means. `location`, `ecg`, `nutrition`, `reproductive` and `mental`
have no records in the FHIR view yet and are refused (400). Records of a source whose consent
refuses the category, or whose revocation is still being purged, are left out, and so are the
records of a provider whose erasure is queued.

A sleep session, a workout or a daily summary also carries figures of other categories. Each
one is shared on the terms of its own category, and left out of the record otherwise:

| Figure | Category |
|---|---|
| Resting heart rate and heart rate variability (sleep session, daily summary), average and maximum heart rate (workout) | `heart` |
| Respiratory rate and oxygen saturation (sleep session), VO2 max (workout) | `respiratory` |
| Sleep score (daily summary) | `sleep` |

A `sleep` link holds a night's duration and stages; its resting heart rate only if the link also
holds `heart` and the consent allows it.

The Bundle holds a Patient with its id only (no name, no `external_ref`), the Observations with
their units, their provider (`meta.source`) and source app, and the Devices they came from. The
snapshot is fixed when it is made: records written later are not added, and records removed
later (deleted at the provider, or past `OHS_RETENTION_DAYS`) stay in it until it stops. To
share a later period, make another link; to take records back, revoke it.

## The link

`uri` is `shlink:/` followed by a base64url JSON payload:

| Member | Value |
|---|---|
| `url` | The manifest URL, `<OHS_PUBLIC_URL>/shl/<manifest id>`; the manifest id is 32 random bytes |
| `key` | The 256-bit key of the file, 43 base64url characters |
| `exp` | `expires_at` in Unix seconds |
| `flag` | `P` when a passcode is needed, absent otherwise |
| `label` | The label, absent when empty |

`viewer_url` is `<OHS_PUBLIC_URL>/shl/view#shlink:/...`. The part after `#` is the address
fragment, which a browser never sends to a server. To show a QR code, encode `viewer_url` with
any QR library in your app; the instance draws none. Set `OHS_PUBLIC_URL` to the instance's
public https address: without it the link is built from the `Host` of the create request,
which behind a proxy is usually not what a reader can reach.

## Opening a link

### The built-in viewer

`GET /shl/view` is a page with its own script and stylesheet, nothing loaded from elsewhere. It
asks the reader's name (and the passcode for a `P` link), requests the file, decrypts it in the
browser (WebCrypto) and shows one table per metric: time (in the reader's time zone), value and
unit, provider, source and device. It shows the records as recorded, with no interpretation.
It follows the system's light or dark setting, works with a keyboard and a screen reader, and
opens only the links of its own instance (its Content-Security-Policy allows no other server).
Once it has read the link it removes it from the address bar and the browser history, since
the link holds the key: reloading the page needs the link again.

### Any other viewer: the manifest request

A viewer that implements SMART Health Links posts to the link's `url`, from any origin
(`Access-Control-Allow-Origin: *`), with no credential:

```sh
curl -s -X POST https://ohs.example.org/shl/<manifest id> -H 'Content-Type: application/json' \
  -d '{"recipient":"Dr. Rivera, Riverside clinic","passcode":"maple-orbit-42","embeddedLengthMax":100000}'
```

```json
{
  "status": "finalized",
  "files": [
    {
      "contentType": "application/fhir+json",
      "embedded": "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIiwiY3R5IjoiYXBwbGljYXRpb24vZmhpcitqc29uIiwiemlwIjoiREVGIn0..<iv>.<ciphertext>.<tag>",
      "lastUpdated": "2026-10-14T09:30:02Z"
    }
  ]
}
```

| Member | Notes |
|---|---|
| `recipient` | Required: who opens the link, as the reader names themselves, at most 256 characters. The owner sees it |
| `passcode` | Required for a `P` link |
| `embeddedLengthMax` | Optional. A file longer than this comes as a `location` instead of `embedded` |

- `embedded` is the file. A `location` is a URL to `GET` once, within 15 minutes; it answers the
  same JWE as `application/jose`. HEAD is refused (405), so a link preview does not use it up.
- A wrong or missing passcode is a 401 with `remainingAttempts`. The 10th wrong passcode
  disables the link for good.
- A link that is unknown, revoked, disabled or past its expiry is a 404, the same answer for
  each.
- A link busy past 2 seconds (a burst of requests on it, or its user being erased) is a 503 with
  `Retry-After`.
- The file is a compact JWE: `alg` `dir`, `enc` `A256GCM`, `cty` `application/fhir+json`, `zip`
  `DEF` (raw DEFLATE, inflate after decrypting). The key is the link's `key`.

## Status, expiry and revocation

| `status` | Meaning |
|---|---|
| `active` | The link works |
| `expired` | Past `expires_at`; the link answers 404 from that time |
| `revoked` | Stopped before its expiry; `revoked_reason` says why |
| `disabled` | 10 wrong passcodes (`revoked_reason` `passcode_attempts`) |

`DELETE /v1/users/{id}/shares/{share_id}` revokes a link at once (`revoked_reason` `owner`); it
answers 204, and again 204 for a link already stopped. The instance also stops links by itself:

- **Consent.** A consent change that refuses a category of a source revokes every link holding
  that source's records of that category (`consent`), in the same transaction.
- **Provider erasure.** Erasing a user's data from one provider (`DELETE
  /v1/users/{id}/data/{provider}`, or a disconnect with `delete_data=true`) revokes every link
  holding that provider's records (`provider_erased`).
- **User deletion.** Deleting the user deletes their links and their access history.

A stopped link keeps its row (status, times, reason) for the person's history; its encrypted
file is removed when it stops, and the `etl` role removes the files of expired links every hour.

## Who opened it

`GET /v1/users/{id}/shares` lists the user's links, oldest first, with how many times each was
served (`accesses`) and when last. `GET /v1/users/{id}/shares/{share_id}` adds
`recent_accesses`, the 50 latest manifest requests, newest first:

```json
"recent_accesses": [
  { "at": "2026-10-15T08:02:41.507Z", "recipient": "Dr. Rivera, Riverside clinic", "outcome": "served" },
  { "at": "2026-10-15T08:02:19.233Z", "recipient": "Dr. Rivera, Riverside clinic", "outcome": "wrong_passcode" }
]
```

`outcome` is `served`, `wrong_passcode` or `inactive` (the link no longer worked). Requests on a
link that no longer works are listed once a day at most, since whoever holds it can keep
calling it; the audit log has every one. The name is what the reader typed, not a verified
identity: one line, no control or bidirectional formatting character.

The [audit log](audit-log.md) records the management calls (`share.create`, `share.read`,
`share.revoke`) and every manifest request on a known link and every file download as
`share.access`, actor `share_link` (its id is the share id), with the outcome in `detail`. The
reader's name stays out of the audit log, which outlives the user. These [webhooks](webhooks.md)
name the share, never the link, its key, its label or the reader:

| Type | Sent when | `data` |
|---|---|---|
| `share.created` | A link was made | `user_id`, `external_ref`, `share_id`, `categories`, `expires_at`, `passcode` |
| `share.revoked` | A link stopped before its expiry | `user_id`, `external_ref`, `share_id`, `reason` |
| `share.accessed` | A manifest request was served the file | `user_id`, `external_ref`, `share_id` |

## Security notes

- **What the instance holds.** The encrypted file, the SHA-256 of the manifest id and a
  PBKDF2-SHA256 hash of the passcode. Not the key: a copy of the database does not open the
  files, and the links cannot be rebuilt from it.
- **Whoever holds the link reads the snapshot** until it expires or is revoked. Keep the expiry
  short, send the passcode another way, revoke a link that went to the wrong place.
- **A passcode is a short secret.** What protects it is the limit: 10 wrong tries per link, ever,
  and a rate limit per client address on the public routes (`OHS_RATE_LIMIT_OAUTH`, a bucket of
  their own, `route_group="share"` in `ohs_rate_limited_total`). See
  [self-hosting.md](self-hosting.md#rate-limits).
- **Revocation stops what comes next.** A reader who already opened the file keeps what they
  saved.
- **The viewer is plain.** No third party script, no cookie, no inline code, no framing, no
  referrer sent. Nothing is cached (`Cache-Control: no-store` on the public routes).
- More in the [security model](security-model.md#share-links).

## Limits

| What | Limit |
|---|---|
| Records per link | 50000 |
| Expiry | At most 365 days ahead |
| Wrong passcodes | 10 per link, then disabled |
| Passcode | 6 to 64 characters |
| Label | 80 characters |
| Reader's name | 256 characters |
| A `location` | One `GET`, within 15 minutes |

## Who can make a link

The routes take a tenant API key, like every route under `/v1/users/{id}`: the person asks in
your app, your backend makes the link and passes `uri` to the app. A user token scope for it is
an open question; see the [spec](specs/share-links.md#who-may-create-a-share-tenant-key-open-question).
