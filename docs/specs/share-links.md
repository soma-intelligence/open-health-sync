# Spec: share links

Status: shipped in v0.2.0 (built 2026-10-14). Guide:
[share-links.md](../share-links.md). Plan: [plans/share-links.md](../plans/share-links.md).

## Problem

A person wants to show some of their own data (last month's sleep and heart rate, a year of
blood pressure) to someone outside the app: a physician, a coach, a family member. Today the
only ways out are a tenant API key, a user token or a file export. The first two are
credentials, and handing them over gives access to everything, with no expiry the person
controls. The export works, but needs a file to travel and nothing stops it being copied
forever.

A share link is one URL (or QR code) that the person hands over. It opens a read-only
snapshot of the categories and time window they chose, encrypted end to end, expiring when they
said, optionally behind a passcode, revocable at any time, with every access recorded. It is
sharing of the person's own data, not clinical software: Open Health Sync shows what the devices
recorded and nothing else.

## Standard

SMART Health Links, from the HL7 FHIR implementation guide "SMART Health Cards and Links"
STU1 (`hl7.fhir.uv.smart-health-cards-and-links` 1.0.0). Pages read on 2026-10-14:

- <https://hl7.org/fhir/uv/smart-health-cards-and-links/STU1/links-specification.html>
  (the payload, flags, manifest request and response, files, encryption)
- <https://hl7.org/fhir/uv/smart-health-cards-and-links/STU1/links-examples.html>
  (an example payload and its encrypted file)
- <https://hl7.org/fhir/uv/smart-health-cards-and-links/STU1/links-user-stories.html>

What the IG asks, and what is used here:

| IG | Here |
|---|---|
| Payload: `url` (the manifest URL), `key` ("43 characters, consisting of 32 random bytes base64urlencoded"), `exp` (a hint, epoch seconds), `flag` (single characters "in alphabetical order"), `label` ("no longer than 80 characters"), `v` (may be omitted when 1); minified JSON, base64url, after `shlink:/` | All of them; `v` omitted; `flag` is `P` with a passcode, absent without |
| Flags `L` (long-term, the receiver may poll), `P` (passcode), `U` (one file by GET, no manifest) | `P` only. No `L`: a share is a snapshot, the manifest `status` is `finalized`. No `U`: the passcode needs the manifest |
| Viewer URLs end in `#`, so the payload stays in the fragment, which browsers never send | `viewer_url` is `{public URL}/shl/view#shlink:/...` |
| Manifest request: `POST`, `application/json`, `recipient` (required), `passcode` (with `P`), `embeddedLengthMax` | Same |
| Inactive link: 404 | Unknown, revoked, disabled or expired: 404, the same body |
| Wrong or missing passcode: 401 with `remainingAttempts`; the server "SHALL enforce a total lifetime count of incorrect Passcodes" | 10 per share, then the share is disabled (404 from then on) |
| Manifest response: `status` (`finalized`, `can-change`, `no-longer-valid`), `files` with `contentType`, `embedded` (a compact JWE) or `location`, `lastUpdated` | `status` `finalized`, one file `application/fhir+json` |
| `location`: lifetime "SHALL NOT exceed one hour", "intended for single use" | 15 minutes, single use |
| The server "SHALL NOT return embedded payload longer than the client-designated maximum" | Embedded unless longer than `embeddedLengthMax`, then `location` |
| Files: JWE compact, `"alg": "dir"`, `"enc": "A256GCM"`, `cty` the content type, `zip` may be `DEF` | Same, with `zip` `DEF` (raw DEFLATE) |
| Too many requests: the server MAY answer 429 with `Retry-After` | Same (rate limit below) |

The IG states no entropy for the manifest URL; the one here carries 256 random bits.

## Scope

Tenant key routes, under the user like every other user resource:

- `POST /v1/users/{id}/shares`: create. Body: `categories` (required), `start` and `end`
  (required, the time window), `expires_at` (required), `passcode` (optional), `label`
  (optional). Answers 201 with the share, `uri` (`shlink:/...`) and `viewer_url`, once, with
  `Cache-Control: no-store`. No `Idempotency-Key`: a stored response would keep the key in the
  database. The records are read and the share stored in one transaction that holds the user
  row `FOR SHARE` and the user's consent lock (`consent.Lock`, an advisory lock that every
  consent change takes): an erasure or a consent change that starts meanwhile waits, then finds
  the share and revokes it; one that finished before is already in what the share reads.
- `GET /v1/users/{id}/shares`: the user's shares, paged, oldest first; never a key or a link.
  Each share has `passcode` (whether it has one) and `passcode_failures`.
- `GET /v1/users/{id}/shares/{share_id}`: one share and `recent_accesses`, its last 50
  manifest requests (time, recipient, outcome), newest first.
- `DELETE /v1/users/{id}/shares/{share_id}`: revoke. 204, idempotent.

Public routes, no credential, rate limited:

- `POST /shl/{manifest_id}`: the manifest. `OPTIONS` answers the CORS preflight, so a viewer on
  another origin can post JSON.
- `GET /shl/file/{ticket}`: the file of a `location`, once, as `application/jose`. HEAD is
  refused (405), so a link checker or a preview never uses the location up.
- `GET /shl/view` (and its script and stylesheet): the built-in viewer.

Every public answer, the 429 of the rate limiter included, carries
`Access-Control-Allow-Origin: *` (no cookie or credential is involved),
`Access-Control-Expose-Headers: Retry-After, X-Request-Id`, `Cache-Control: no-store`,
`X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.

Out of scope: the `L` and `U` flags, SMART Health Cards, a QR code (the app renders `uri`; no
dependency), ECG recordings (not in the FHIR view of this branch; a follow-up once they are),
category samples, nutrition, routes and the other records the FHIR view does not serve, a user
token route (see open questions).

## What a share holds

One FHIR R4 Bundle of type `collection`, made at creation with the existing mapping
(docs/fhir.md), merged view, records whose time is in `[start, end)`:

| Category | Records |
|---|---|
| `activity` | activity quantity samples, workouts, daily summaries |
| `heart` | heart quantity samples (heart rate, resting heart rate, HRV...) |
| `blood_pressure` | blood pressure panels and unpaired halves |
| `glucose`, `respiratory`, `body`, `other` | the quantity samples of the category |
| `sleep` | sleep sessions, and the quantity samples of the category |

A quantity type belongs to the category `consent.Of` gives it, so a share and a consent agree on
what a category is. The other categories (`location`, `ecg`, `nutrition`, `reproductive`,
`mental`) have no record in the FHIR view yet and are a 400, so a share never looks like it
holds something it does not. Records of a category whose consent revocation is still being
purged are left out, and so are those of a provider with a queued erasure.

A sleep session, a workout or a daily summary carries figures of other categories: resting
heart rate, HRV, average and maximum heart rate (`heart`), respiratory rate, oxygen saturation,
VO2 max (`respiratory`), a daily summary's sleep score (`sleep`). Each figure is kept on the
terms of its own category (in the share, allowed by the consent) and removed from the record
otherwise; a figure kept adds its `provider/category` pair to `sources`, so a consent change on
that category revokes the share.

The Bundle holds the Patient (its id only: the tenant's `external_ref` stays inside the tenant),
the Observations in the order of the FHIR search, and the Devices they reference, each `fullUrl`
the resource's URL on the instance, as in the user archive. At most 50000 Observations: a
larger share is a 400 that says to narrow the window or the categories. The Bundle is written
straight into a DEFLATE stream, so memory holds the compressed form only.

## Keys and storage

- `key`: 32 random bytes (`crypto/rand`), in the payload only. Never stored, logged or sent
  again. Without it the stored file cannot be read, by the instance included.
- Manifest id: 32 random bytes, base64url. Only its SHA-256 is stored (like tokens), so the
  links cannot be rebuilt from the database.
- File: the compact JWE, in `ohs_sync.shares.jwe`. Removed when the share is revoked or
  disabled, and by an hourly sweep once expired.
- Passcode: PBKDF2-SHA256 (`crypto/pbkdf2`), 100000 iterations, a 16 byte random salt, 32 byte
  output, compared in constant time. 6 to 64 characters. The link and the passcode are meant to
  travel apart: the passcode guards a link that leaked.
- Encryption with the standard library only: `crypto/aes`, `crypto/cipher` (GCM, 12 byte
  random IV, 16 byte tag), `compress/flate`, `encoding/base64`. No new dependency.

## Requirements

- **Auth.** Management routes: tenant key only (open question below). Public routes: the
  manifest id is the credential.
- **Tenant isolation.** Every query on the new tables filters `tenant_id` and `user_id`, except
  the lookup of a manifest or a file by the SHA-256 of its secret, which cannot know them
  (the same as the token lookup); every query after it uses the tenant and user of the row.
  The user must be active. The hourly expiry sweep lists the users with expired files, then
  works per user.
- **Expiry.** `expires_at` is required, later than now and at most 365 days ahead. The manifest
  and the file answer 404 from that time, whatever the sweep has done.
- **Passcode attempts.** The passcode is checked first, holding no lock (its hash never
  changes; PBKDF2 takes tens of milliseconds), then counted under the share's row lock. A wrong
  or missing passcode counts, answers 401 with `remainingAttempts`; the tenth disables the share
  and removes its file. The public routes wait at most 2 seconds for a lock (`lock_timeout`),
  then answer 503 with `Retry-After`, so a burst on one link cannot hold the connection pool.
- **Rate limit.** `POST /shl/{manifest_id}` and `GET /shl/file/{ticket}` take
  `OHS_RATE_LIMIT_OAUTH` per client address, in a bucket of their own (`route_group="share"` in
  `ohs_rate_limited_total`), like the other browser-driven public routes (OAuth callback,
  setup).
- **Audit.** `POST`, `GET` and `DELETE` on the management routes are `share.create`,
  `share.read` and `share.revoke`. Each manifest request on a known share, and each file
  download, is `share.access` with the new actor type `share_link` (actor id: the share id),
  status and `detail` `{"share_id", "outcome"}`: `served`, `wrong_passcode`, `inactive`,
  `file`. The recipient's text is not in the audit log, which outlives the user: it is in
  `ohs_sync.share_accesses`, which goes with the user. An unknown manifest id names no tenant
  and is not recorded. An `inactive` request has an access row once a day per share at most,
  since anyone holding a dead link can keep calling it; the audit log has every one.
- **Webhooks** (thin, identifiers only, never a value, a key, a link, a label or a
  recipient): `share.created` (`share_id`, `categories`, `expires_at`, `passcode`),
  `share.revoked` (`share_id`, `reason`: `owner`, `consent`, `provider_erased`,
  `passcode_attempts`), `share.accessed` (`share_id`, on each served manifest).
- **Deletion.** A user deletion erases both tables (the worker deletes every table with a
  `user_id`). A consent change revokes every active share that holds a record of that source in
  a category it does not grant. A provider erasure revokes every active share that holds a
  record of that provider. Revoking removes the file and keeps the row (status, times, reason)
  for the person's history; a share already past its expiry stays `expired` and loses its file.
  Each share stores `sources`, the `provider/category` pairs it holds, for these two. Records
  deleted later at the provider or by retention stay in the shares made before.
- **Logs.** Nothing logs a key, a link, a manifest id, a ticket, a passcode, a recipient or a
  value; the request log has the route pattern, not the path.
- **Viewer.** Plain HTML, one script module and one stylesheet, embedded in the binary; no
  dependency. It reads the payload from the fragment, asks for the recipient's name (and the
  passcode with `P`), posts the manifest request to the payload's `url`, decrypts with WebCrypto
  (`AES-GCM`), inflates with `DecompressionStream("deflate-raw")`, and shows one table per kind
  of Observation with its unit and provenance (provider, source, device). It then removes the
  link from the address bar and the history (`history.replaceState`), and works under a path
  prefix (relative asset paths; the share routes are siblings of its own path). Strict CSP
  (`script-src 'self'`, `connect-src 'self'`, no inline code), `Referrer-Policy: no-referrer`,
  light and dark, labelled form, captioned tables, a live status region.
- **Migration 0037**: the two tables, and `share_link` added to the audit actor types.

## Who may create a share: tenant key, open question

Built: tenant key only, like every route under `/v1/users/{id}`. The person asks in the
tenant's app, the tenant's backend creates the share and passes `uri` to the app.

Alternative: a user token scope `share` (minted like `read`), so an app could create shares
from the device without a backend round trip. Against it, for now: a share is a way out of the
instance for the person's data, readable by anyone holding the link; a token on a phone that
leaks with `share` would let its holder publish the data, which `read` (an authenticated pull
that the audit log ties to the token) does not. The tenant key keeps the decision in the
tenant's backend, where the tenant already enforces its own consent screens. A `share` scope
would need a lower TTL than `read` and the tenant's opt-in. Flagged for a decision.

## Open questions

- The `share` user token scope (above).
- Snapshot only: an `L` share (the manifest rebuilt on request, the key kept by the app) would
  stay current but needs the instance to re-encrypt with a key it does not hold, so the app
  would have to send it. Not built.
- ECG recordings in shares, once the ECG read is merged.
- The 50000 Observation cap and the 365 day expiry limit are guesses; both are constants.
- Passcode attempts: 10 per share, lifetime, as the IG asks; no unlock route.
