# Share your health data with your clinician through a link

To share your health data with your clinician, make a share link in Open Health Sync: one call
to `POST /v1/users/{id}/shares` picks the categories and the time window, and returns a link your
clinician opens in a browser. The link is encrypted end to end, expires when you say, can sit
behind a passcode, can be revoked at any time, and every opening is recorded.

Links follow SMART Health Links, so any viewer that implements that standard can open one, and
your instance serves a small viewer of its own. The file is a FHIR R4 Bundle of your records as
your devices recorded them: Open Health Sync interprets nothing.

## 1. Start an instance with your data

Follow the [quickstart](../../README.md#quickstart), connect your devices (each
[connector guide](../connectors/README.md) starts with the keys it needs), and load your user's
credentials into the shell as the other how-to pages do (`OHS_URL`, `OHS_KEY`, `OHS_USER`). Set
`OHS_PUBLIC_URL` to the instance's public https address: the link is built from it, and without
it the link uses the `Host` of the request, which behind a proxy is usually not what your
clinician can reach.

## 2. Make the link

```sh
curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/shares" -H "Authorization: Bearer $OHS_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"categories":["heart","sleep"],"start":"2026-09-01","end":"2026-10-01",
       "expires_at":"2026-10-21T00:00:00Z","passcode":"maple-orbit-42","label":"For my GP"}'
```

| Member | Required | Notes |
|---|---|---|
| `categories` | yes | One or more of the categories below |
| `start`, `end` | yes | The window, `start <= record time < end`; RFC 3339 or `YYYY-MM-DD` |
| `expires_at` | yes | In the future, at most 365 days ahead |
| `passcode` | no | 6 to 64 characters; the server keeps a PBKDF2-SHA256 hash only |
| `label` | no | At most 80 characters, shown to the reader and readable by whoever holds the link: no health detail |

The answer holds `uri` (`shlink:/...`) and `viewer_url` (the built-in viewer), once. Both carry
the decryption key and the server keeps neither: a lost link can't be shown again, so revoke it
and make another. A retry after a lost answer makes a second link.

| Category | What goes in |
|---|---|
| `activity` | Steps, distance, energy and other activity samples, workouts, daily summaries |
| `heart` | Heart rate, resting heart rate, heart rate variability and the other heart samples |
| `blood_pressure` | Blood pressure readings, a systolic and diastolic pair as one panel |
| `glucose`, `respiratory`, `body`, `other` | The quantity samples of the category |
| `sleep` | Sleep sessions and the quantity samples of the category |

`location`, `ecg`, `nutrition`, `reproductive` and `mental` are refused for now (400). When two
devices recorded the same thing, the link holds your preferred source's records
([merged view](merge-multiple-wearables.md)). A figure of another category inside a record (the
resting heart rate of a sleep session, say) is only included if that category is shared too.
Records your [consent](../consent.md) refuses are left out.

## 3. Hand it to your clinician

Send `viewer_url` in a message or email, or draw a QR code from it in your app (the instance
draws none). Give the passcode another way, out loud or over another channel. Your clinician
opens the page, types their name and the passcode, and sees one table per metric: time, value,
unit, provider, source and device. The browser decrypts the file; the key never reaches the
instance.

## 4. See who opened it

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/shares" -H "Authorization: Bearer $OHS_KEY"
curl -s "$OHS_URL/v1/users/$OHS_USER/shares/$SHARE_ID" -H "Authorization: Bearer $OHS_KEY"
```

The list gives each link's `status`, `accesses` and `last_accessed_at`. One link adds
`recent_accesses`: the 50 latest requests with the name the reader typed and an `outcome`
(`served`, `wrong_passcode`, `inactive`). The name is whatever the reader typed: nothing verifies it.

## 5. Revoke it

```sh
curl -s -X DELETE "$OHS_URL/v1/users/$OHS_USER/shares/$SHARE_ID" -H "Authorization: Bearer $OHS_KEY"
```

It answers 204 and the link stops at once. A link also stops by itself at `expires_at`, after 10
wrong passcodes, when a consent change refuses a category it holds, or when a provider's data in
it is erased. Revocation stops what comes next: a reader who already saved the file keeps it.

## What a link does not do

- The snapshot is fixed when it is made. To share a later period, make another link.
- It holds at most 50000 records. A bigger window is a 400 that says why.
- Whoever holds the link reads the snapshot until it stops. Keep the expiry short.

## Related

- [Share links reference](../share-links.md)
- [Combine data from several wearables](merge-multiple-wearables.md)
- [Delete a user's health data](delete-user-data.md)
- [Get Oura data as FHIR](oura-fhir.md)
- [README](../../README.md)
