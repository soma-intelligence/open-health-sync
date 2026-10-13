#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Backs up an Open Health Sync compose stack into one archive:
#
#   ohs-backup-<UTC timestamp>.tar
#     manifest.txt    format, time, ohs version, schema version, Postgres and
#                     TimescaleDB versions, raw store kind, SHA-256 of the files
#     database.dump   pg_dump of the database, custom format
#     raw.tar.gz      the raw store directory (file raw store only)
#
# pg_dump runs inside the database container (docker compose exec), so the
# host needs no Postgres tools. The raw directory is read through a throwaway
# container that mounts the server's volumes. With an S3 raw store the raw
# files are not copied: the bucket's own versioning or replication covers them.
#
# The server keeps running. The database is dumped first, then the raw files,
# so every raw object the dump knows about is in the archive.
#
# The archive does NOT hold OHS_ENCRYPTION_KEY: keep .env (or the key) in a
# separate safe place, or the stored OAuth tokens in the dump cannot be read.
#
# Usage: scripts/backup.sh            (prints the archive path on stdout)
# Env:   OHS_BACKUP_DIR   where archives go (default backups/ at the
#                         repository root)
#        OHS_BACKUP_KEEP  keep only the newest N archives there (default 0:
#                         keep all)
#        and the compose settings of scripts/backup_lib.sh (OHS_COMPOSE_FILES,
#        OHS_COMPOSE_ENV_FILE, OHS_COMPOSE_PROJECT, OHS_PG_SERVICE,
#        OHS_APP_SERVICE, OHS_PG_USER, OHS_PG_DATABASE).
# See docs/backup-and-restore.md.
set -euo pipefail

# shellcheck source=scripts/backup_lib.sh
source "$(dirname "$0")/backup_lib.sh"

case "${1:-}" in
  "") ;;
  -h|--help) sed -n '4,/^set -euo/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'; exit 0 ;;
  *) die "unexpected argument $1 (see --help)" ;;
esac

OHS_BACKUP_DIR="${OHS_BACKUP_DIR:-$OHS_REPO_ROOT/backups}"
OHS_BACKUP_KEEP="${OHS_BACKUP_KEEP:-0}"
[[ "$OHS_BACKUP_KEEP" =~ ^[0-9]+$ ]] || die "OHS_BACKUP_KEEP must be a number"

need_tools
umask 077
mkdir -p "$OHS_BACKUP_DIR"

ts=$(date -u +%Y%m%dT%H%M%SZ)
name="ohs-backup-$ts"
work=$(mktemp -d "$OHS_BACKUP_DIR/.$name.XXXXXX")
in_container="/tmp/$name.dump"
cleanup() {
  rm -rf "$work"
  dc exec -T "$OHS_PG_SERVICE" rm -f "$in_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

running "$OHS_PG_SERVICE" || die "the $OHS_PG_SERVICE service is not running (start the stack, or set OHS_COMPOSE_* to match how you run it)"
app=$(container_id "$OHS_APP_SERVICE")
[ -n "$app" ] || die "the $OHS_APP_SERVICE service has no container (start the stack first)"

log "reading versions"
ohs_version=unknown
if running "$OHS_APP_SERVICE"; then
  ohs_version=$(dc exec -T "$OHS_APP_SERVICE" /ohs version 2>/dev/null | head -n 1) || ohs_version=unknown
fi
mig=$(psql_q "$OHS_PG_DATABASE" 'SELECT version, dirty FROM schema_migrations') ||
  die "cannot read schema_migrations from database $OHS_PG_DATABASE"
migration_version=${mig%%|*}
[[ "$migration_version" =~ ^[0-9]+$ ]] || die "no schema version in schema_migrations (has ohs ever started?)"
[ "${mig#*|}" = "f" ] || die "the schema is dirty (a migration failed half way): fix it before taking a backup"
pg_version=$(psql_q "$OHS_PG_DATABASE" 'SHOW server_version_num')
tsdb_version=$(psql_q "$OHS_PG_DATABASE" "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'")
[ -n "$tsdb_version" ] || die "the timescaledb extension is not installed in $OHS_PG_DATABASE"

log "dumping database $OHS_PG_DATABASE (schema version $migration_version, TimescaleDB $tsdb_version)"
# Into a file in the container, then copied out: a seekable dump keeps the
# data offsets pg_restore uses. pg_dump warns about circular foreign keys in
# TimescaleDB's own catalog; the dump is complete despite them.
if ! dc exec -T "$OHS_PG_SERVICE" pg_dump -U "$OHS_PG_USER" -d "$OHS_PG_DATABASE" -Fc -f "$in_container" 2>"$work/pg_dump.log"; then
  cat "$work/pg_dump.log" >&2
  die "pg_dump failed"
fi
dc cp "$OHS_PG_SERVICE:$in_container" "$work/database.dump" >/dev/null 2>&1 || die "cannot copy the dump out of the $OHS_PG_SERVICE container"
dc exec -T "$OHS_PG_SERVICE" rm -f "$in_container" >/dev/null 2>&1 || true
[ -s "$work/database.dump" ] || die "the dump is empty"
rm -f "$work/pg_dump.log"

raw_url=$(raw_store_url "$app")
raw_path=$(raw_dir "$raw_url")
files="manifest.txt database.dump"
if [ -n "$raw_path" ]; then
  mounted_at "$app" "$raw_path" ||
    die "$raw_path is not on a volume of the $OHS_APP_SERVICE container: its files would not survive the container"
  image=$(helper_image) || die "cannot find the $OHS_PG_SERVICE image"
  log "archiving the raw store ($raw_path)"
  # Read only, no network; temporary files of uploads in flight are left out.
  docker run --rm --network none --user 0:0 --volumes-from "$app:ro" --entrypoint tar "$image" \
    -C "$raw_path" --exclude '.ohs-tmp-*' -czf - . >"$work/raw.tar.gz" ||
    die "cannot archive the raw store"
  raw_store="file"
  raw_included=yes
  files="$files raw.tar.gz"
else
  raw_store=${raw_url%%:*}
  raw_included=no
  note "the raw store is $raw_store, not a local directory: its files are not in this backup."
  note "rely on the bucket's own versioning or replication for them (see docs/backup-and-restore.md)."
fi

{
  echo "format=$OHS_BACKUP_FORMAT"
  echo "created_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "ohs_version=$ohs_version"
  echo "migration_version=$migration_version"
  echo "postgres_version_num=$pg_version"
  echo "timescaledb_version=$tsdb_version"
  echo "database=$OHS_PG_DATABASE"
  echo "raw_store=$raw_store"
  echo "raw_included=$raw_included"
  [ -z "$raw_path" ] || echo "raw_path=$raw_path"
  echo "sha256_database.dump=$(sha256_of "$work/database.dump")"
  [ "$raw_included" = no ] || echo "sha256_raw.tar.gz=$(sha256_of "$work/raw.tar.gz")"
} >"$work/manifest.txt"

# shellcheck disable=SC2086 # $files is a list of plain names
tar -C "$work" -cf "$work/$name.tar" $files
mv "$work/$name.tar" "$OHS_BACKUP_DIR/$name.tar"
out="$OHS_BACKUP_DIR/$name.tar"
log "wrote $out ($(du -h "$out" | awk '{print $1}'))"

if [ "$OHS_BACKUP_KEEP" -gt 0 ]; then
  # Names sort by time; drop all but the newest OHS_BACKUP_KEEP.
  find "$OHS_BACKUP_DIR" -maxdepth 1 -name 'ohs-backup-*.tar' | sort -r |
    tail -n +"$((OHS_BACKUP_KEEP + 1))" | while IFS= read -r old; do
      rm -f "$old" && log "removed old backup $old"
    done
fi

log "the encryption key is not in the archive: keep OHS_ENCRYPTION_KEY (your .env) safe separately"
echo "$out"
