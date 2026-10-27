# Repair Fitbit rows of relayed Apple Health and Health Connect data

A user who links Apple Health (or Health Connect) in the Google Health app gets that data back
through the Fitbit connector, marked `platform` `HEALTH_KIT` (or `HEALTH_CONNECT`) in metadata.
Before the connector told these points apart (see the [changelog](../../CHANGELOG.md)), it stored
them as Fitbit's own:

- Apple's HRV, which is SDNN, under `hrv_rmssd`, next to Fitbit's real RMSSD;
- a deep sleep RMSSD of 0 in the metadata of daily HRV figures;
- every relayed sample with source `fitbit`, so Apple's or Health Connect's minutes added up
  with the tracker's in one stream.

New data is stored right. [fitbit-relayed-points.sql](fitbit-relayed-points.sql) repairs the
rows stored before:

1. An Apple HRV row the fixed connector has already stored again as `hrv_sdnn` (same user, same
   time) is deleted. Every other one becomes `hrv_sdnn`, with `hrv_method: sdnn`; a sample
   that carried a standard deviation takes it as its value, as the connector now reads it.
2. A deep sleep RMSSD of 0 or less is removed from metadata.
3. Relayed samples get source `apple_health` / `Apple Health` or `health_connect` /
   `Health Connect`.

It touches provider `fitbit` rows of `ohs_data.quantity_samples` only, runs in one
transaction, and a second run changes nothing.

## Why it is not a migration

`quantity_samples` is compressed after 30 days. The rows to fix are found by a metadata field,
which compressed chunks cannot filter without decompressing, so the script decompresses the
batches of every chunk it scans. On a large instance that takes time and disk space, which a
migration run on every start must not impose. Run it once, in a quiet hour, with room on the
database disk (as much again as the compressed part of `quantity_samples`, at worst).

## Run it

Back up first ([backup and restore](../backup-and-restore.md)). Then, with the stack running:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T postgres \
  psql -U ohs -d ohs -v ON_ERROR_STOP=1 < docs/how-to/fitbit-relayed-points.sql
```

It prints `BEGIN`, one `DELETE n`, four `UPDATE n` lines and `COMMIT`. TimescaleDB's
compression policy compresses the chunks again on its next run.

After it, a manual sync of a Fitbit connection that reaches further back than the regular pulls
can store an Apple HRV night again under its new identity; run the script again after such a
sync to remove the twin. Daily totals derived from the samples are recomputed the next time a
sample of that day arrives.
