# Backup and restore

Two scripts back up and restore an install that runs on the bundled Docker Compose stack:
`scripts/backup.sh` writes one archive, `scripts/restore.sh` puts it back. They run on the host
and need Docker with Compose v2.24 or later, `tar`, `gzip` and `sha256sum` (or `shasum`). They
need no Postgres tools on the host: `pg_dump` and `pg_restore` run inside the database
container. CI runs the whole cycle on every pull request (`make backup-test`, below).

## What is backed up

`scripts/backup.sh` writes `backups/ohs-backup-<UTC time>.tar` (mode 600) holding:

| File | Content |
|---|---|
| `database.dump` | `pg_dump` of the database in custom format: tenants, keys, users, connections and their sealed OAuth tokens, jobs, and all normalized data |
| `raw.tar.gz` | every raw payload of the file raw store (the `raw` volume). Left out with an S3 raw store, see below |
| `manifest.txt` | the time, the `ohs version` of the running server, the schema version (from `schema_migrations`), the Postgres and TimescaleDB versions, the raw store kind and the SHA-256 of each file |

The database is dumped first, then the raw files are copied, so every raw object the dump knows
about is in the archive. The server keeps running during a backup; `pg_dump` reads one
consistent snapshot. Raw files can rebuild normalized data, but not users, keys or connections,
so the database is the part that matters most.

**Not in the archive: `OHS_ENCRYPTION_KEY`.** The OAuth tokens in the dump are sealed with it.
Without the same key a restored instance cannot read them, and every user must connect their
providers again (uploads and stored data are not affected). Keep `.env`, or at least the key, in
a separate safe place, such as a password manager, and not next to the archives: anyone with
both the archive and the key can use the stored tokens.

The archive holds health data. Store it encrypted and with access limited the way you limit
access to the server itself.

## Take a backup

From the repository root, with the stack running:

```sh
scripts/backup.sh
```

It prints the archive path on the last line. Settings are environment variables, with the
defaults of `deploy/docker-compose.yml`:

| Variable | Default | Meaning |
|---|---|---|
| `OHS_BACKUP_DIR` | `backups/` in the repository | Where archives go |
| `OHS_BACKUP_KEEP` | `0` (keep all) | Keep only the newest N archives in `OHS_BACKUP_DIR` |
| `OHS_COMPOSE_FILES` | `deploy/docker-compose.yml` | Compose files, colon separated, relative to the repository root. List the overlays you run with, for example `deploy/docker-compose.yml:deploy/docker-compose.pi.yml` |
| `OHS_COMPOSE_ENV_FILE` | `.env` when it exists | Passed to compose as `--env-file`; set it empty for none |
| `OHS_COMPOSE_PROJECT` | `COMPOSE_PROJECT_NAME`, else `ohs` | Compose project name, for a second install on the same host |
| `OHS_PG_SERVICE`, `OHS_APP_SERVICE` | `postgres`, `ohs` | Service names |
| `OHS_PG_USER`, `OHS_PG_DATABASE` | `ohs`, `ohs` | Database user and name |

The scripts never print secrets: they read one variable (`OHS_RAW_STORE`) from the server
container's environment and nothing else.

## How often

Daily is a sensible default; run it more often if losing a day of data would hurt. Copy each
archive off the host, or a disk failure takes the backups with the server. A cron entry that keeps two weeks of daily backups and logs the result:

```sh
# m h dom mon dow  command
15 3 * * * cd /srv/open-health-sync && OHS_BACKUP_KEEP=14 scripts/backup.sh >>/var/log/ohs-backup.log 2>&1
```

Cron runs with a short `PATH`: make sure `docker` is on it, or set `PATH=` at the top of the
crontab. A deleted user's data stays in backups taken before the deletion, so set
`OHS_BACKUP_KEEP` (and the retention of your off-host copies) to match your own erasure
commitments.

## S3 raw store

With `OHS_RAW_STORE=s3://...` (the S3 overlay, or a bucket at a provider) the raw files are not
in the archive, and the scripts say so. Protect the bucket with the store's own tools instead:
turn on object versioning, and replication to a second bucket or region if you need it. Raw
objects are written once and never rewritten, so versioning costs little. The database dump is
still taken as usual, and it is still the part you cannot rebuild.

## Restore

Restore **replaces** the database and the raw files of the stack:

```sh
scripts/restore.sh backups/ohs-backup-20261011T031500Z.tar
```

It asks you to type `restore` first; `--yes` (or `OHS_RESTORE_YES=1`) skips the question for
scripts. It takes the same settings as `scripts/backup.sh` and then:

1. checks the archive: the manifest, the SHA-256 of each file, and that `pg_restore` can read the
   dump's table of contents;
2. starts the database service, and refuses to go on when the archive's schema version is newer
   than the ohs image (`ohs migrate latest`), when the archive's TimescaleDB version is not
   available in the database image, or when the Postgres major version is older than the one the
   backup came from. Nothing is changed when it refuses;
3. stops `ohs`, drops and recreates the database, creates the `timescaledb` extension at the
   archive's version, runs `SELECT timescaledb_pre_restore()`, `pg_restore`, then
   `SELECT timescaledb_post_restore()`;
4. empties the raw directory and unpacks the archive's raw files into it, keeping their owner;
5. starts `ohs` and waits until it is healthy. An archive with an older schema version is
   migrated forward by `ohs up` on start.

To restore on a new host: check out the same release (or a newer one), put the `.env` of the
backup in place (the same `OHS_ENCRYPTION_KEY`), then run `scripts/restore.sh` with the archive.
The stack does not need to be running first.

`ohs migrate latest` came after 0.0.2. With a 0.0.2 image the script cannot check the version
and refuses; set `OHS_RESTORE_SKIP_VERSION_CHECK=1` only when the archive was taken by that same
release.

## Verify a backup

A backup you have never restored is a hope. Two checks, from cheap to complete:

```sh
# checksums, manifest and the dump's table of contents; changes nothing
scripts/restore.sh --verify backups/ohs-backup-20261011T031500Z.tar

# a full restore into a second, throwaway install on port 8090
OHS_COMPOSE_PROJECT=ohs-restore-test OHS_HTTP_PORT=8090 \
  scripts/restore.sh --yes backups/ohs-backup-20261011T031500Z.tar
curl -s http://localhost:8090/readyz
docker compose -p ohs-restore-test -f deploy/docker-compose.yml exec ohs /ohs admin tenant list
docker compose -p ohs-restore-test -f deploy/docker-compose.yml down -v
```

The second install uses its own project, so its volumes are separate from the live ones. Run the
full check after each upgrade and every few months.

## By hand

For an install that does not use the compose stack, the same steps by hand. Back up:

```sh
pg_dump -Fc -d "$DATABASE" -f ohs.dump
tar -C /var/lib/ohs/raw -czf raw.tar.gz .
```

Restore into the release that took the backup, or a newer one, with `ohs` stopped:

```sh
psql -d postgres -c 'DROP DATABASE ohs WITH (FORCE)' -c 'CREATE DATABASE ohs TEMPLATE template0'
psql -d ohs -c "CREATE EXTENSION timescaledb VERSION '<the version the backup was taken with>'" \
  -c 'SELECT timescaledb_pre_restore()'
pg_restore -d ohs --no-owner --exit-on-error ohs.dump
psql -d ohs -c 'SELECT timescaledb_post_restore()'
# empty the raw directory first, so files written after the backup do not linger
find /var/lib/ohs/raw -mindepth 1 -delete
tar -C /var/lib/ohs/raw -xzf raw.tar.gz
```

`pg_dump` warns about circular foreign keys in TimescaleDB's own catalog; the dump is complete
despite the warning.

## How it is tested

`make backup-test` (`test/e2e/backup_restore.sh`, the `backup` job in CI) builds the image from
the checkout, starts the stack, uploads a fixture and waits until it is processed, records the
row count of every table, API reads and the raw files with their checksums, takes a backup,
destroys the stack with its volumes, starts it empty, checks that an archive with a newer
schema version is refused, restores, and checks that everything recorded came back.
