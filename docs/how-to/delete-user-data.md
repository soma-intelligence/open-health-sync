# Delete a user's health data (erasure and data portability)

To erase a user in Open Health Sync, call `DELETE /v1/users/{id}`: it answers 202 with a
`job_id`, cuts the user's access at once, and a background job removes their raw files, every
record and the user. To hand a person their data first, `GET /v1/users/{id}/archive` streams
everything the instance holds about them as one zip file. Both calls help an operator answer
erasure and portability requests.

No software is compliant on its own, and Open Health Sync is not certified or audited. Whether a
deployment meets GDPR or HIPAA depends on how you run it ([security model](../security-model.md)).

## 1. Check who is asking

Confirm the person is who they say, in your own product. Open Health Sync doesn't know them: it
knows a user id and your `external_ref`.

## 2. Hand over their data (portability)

```sh
curl -sOJ "$OHS_URL/v1/users/$OHS_USER/archive" -H "Authorization: Bearer $OHS_KEY"
unzip -l ohs-user-*.zip
```

The route takes a tenant key, or a user token with scope `read` for its own user only, so your
app can let a person download their own archive. It takes no parameter: the archive always holds
everything.

| Path in the zip | What it holds |
|---|---|
| `manifest.json` | User, times, instance version, record counts per file, raw object count and bytes |
| `user.json` | The user and its provider connections (never a token) |
| `canonical/*.ndjson` | Every record of every list, the same JSON as the REST lists |
| `fhir/bundle.json` | One FHIR R4 Bundle, type `collection` |
| `omh/data-points.ndjson` | The records that have an Open mHealth schema |
| `raw/<provider>/<day>/<file>` | Every raw payload, byte for byte (raw retention may have removed old ones) |

A failure mid-stream cuts the connection, so a truncated zip doesn't open and is never taken for
a complete one. The archive holds health data: serve it over TLS only and don't keep copies
longer than needed. Details: [complete user archive](../exports.md#complete-user-archive-data-portability).

## 3. Erase the user

```sh
curl -s -X DELETE "$OHS_URL/v1/users/$OHS_USER" -H "Authorization: Bearer $OHS_KEY"
# 202 {"job_id": "..."}
```

The user's status becomes `deleting` at once: reads return 404 and the user's tokens stop
working. The job then revokes provider connections (best effort), deletes the raw files and every
row, then the user. Consent, its history and queued purges go with it, and so do the user's share
links and their access history.

Poll the job until `status` is `done` (`pending`, `running`, `done` or `failed`):

```sh
curl -s "$OHS_URL/v1/deletions/$JOB_ID" -H "Authorization: Bearer $OHS_KEY"
```

Or subscribe a [webhook](../webhooks.md) to `user.deleted`, sent when the erasure finished.

## 4. Or erase one provider's data only

Some providers' terms ask that a user's data from them be erased when the user disconnects them.

| Call | When |
|---|---|
| `DELETE /v1/users/{id}/connections/{provider}?delete_data=true` | Disconnect a cloud provider and erase its data |
| `DELETE /v1/users/{id}/data/{provider}` | Erase without touching a connection: device providers (`apple_health`, `health_connect`) or a provider already disconnected. A provider still connected is a 409 |

Both answer 202 with a `job_id` that `GET /v1/deletions/{job_id}` reads. The job removes that
provider's records, tombstones, raw files, failed records, jobs, sync log and the disconnected
connection; the user, their other providers and their order of sources stay. Until it's done the
provider can't be connected again and its uploads are refused (409). Share links holding that
provider's records are revoked, and the `data.erased` webhook announces the end.

## 5. Keep the proof

The audit log records the request (`user.delete`) and the completed erasure (`user.erased`, actor
`system`, with the job id). Those events hold the user's id only, no personal data, and stay
after the erasure:

```sh
curl -s "$OHS_URL/v1/audit?user_id=$OHS_USER&action=user." -H "Authorization: Bearer $OHS_KEY"
```

Exports and archive downloads show up as `data.export`. Events older than
`OHS_AUDIT_RETENTION_DAYS` (default 365) are deleted once a day; set it to what your legal basis
requires. See the [audit log](../audit-log.md).

## What erasure does not reach

- A copy someone already took: a downloaded archive or export, a share link file a reader saved.
- Backups taken before the deletion. Set `OHS_BACKUP_KEEP` and the retention of your off-host
  copies to match your own erasure commitments ([backup and restore](../backup-and-restore.md)).

## Related

- [API guide: users](../api.md#users)
- [Consent](../consent.md)
- [Share your health data with your clinician](share-with-clinician.md)
- [Export WHOOP data to CSV or Parquet](whoop-export.md)
- [Import Apple Health data into PostgreSQL](apple-health-to-postgres.md)
- [README](../../README.md)
