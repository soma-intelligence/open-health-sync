#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Restores an archive written by scripts/backup.sh into an Open Health Sync
# compose stack. It REPLACES the stack's database and raw files.
#
#   1. checks the archive (manifest, SHA-256 of each file, the dump's table of
#      contents)
#   2. starts the database service, checks that the archive's schema version is
#      not newer than the ohs image (`ohs migrate latest`) and that the
#      archive's TimescaleDB version can be installed
#   3. stops ohs, drops and recreates the database, creates the extension at
#      the archive's version, runs timescaledb_pre_restore(), pg_restore and
#      timescaledb_post_restore()
#   4. empties the raw directory and unpacks the archive's raw files into it
#      (file raw store; with S3 the bucket is left alone)
#   5. starts ohs, which applies any newer migrations on start, and waits until
#      it is healthy
#
# Restore with the .env the backup was taken with: the stored OAuth tokens are
# sealed with its OHS_ENCRYPTION_KEY.
#
# Usage: scripts/restore.sh [--yes] ARCHIVE    restore (asks first without --yes)
#        scripts/restore.sh --verify ARCHIVE   check the archive only, change nothing
# Env:   OHS_RESTORE_YES=1 is the same as --yes
#        OHS_RESTORE_WAIT  seconds to wait for ohs to become healthy (default 300)
#        OHS_RESTORE_SKIP_VERSION_CHECK=1 skips the schema version check, for
#                          an ohs image without `ohs migrate latest` (0.0.2):
#                          only when the archive was taken by that same release
#        and the compose settings of scripts/backup_lib.sh (OHS_COMPOSE_FILES,
#        OHS_COMPOSE_ENV_FILE, OHS_COMPOSE_PROJECT, OHS_PG_SERVICE,
#        OHS_APP_SERVICE, OHS_PG_USER, OHS_PG_DATABASE).
# See docs/backup-and-restore.md.
set -euo pipefail

# shellcheck source=scripts/backup_lib.sh
source "$(dirname "$0")/backup_lib.sh"

usage() { sed -n '4,/^set -euo/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'; }

yes="${OHS_RESTORE_YES:-0}"
verify_only=0
archive=""
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes) yes=1 ;;
    --verify) verify_only=1 ;;
    -h|--help) usage; exit 0 ;;
    -*) die "unknown option $1 (see --help)" ;;
    *) [ -z "$archive" ] || die "one archive at a time"; archive=$1 ;;
  esac
  shift
done
[ -n "$archive" ] || die "usage: scripts/restore.sh [--yes|--verify] ARCHIVE"
[ -f "$archive" ] || die "$archive does not exist"
OHS_RESTORE_WAIT="${OHS_RESTORE_WAIT:-300}"

need_tools
umask 077
work=$(mktemp -d "${TMPDIR:-/tmp}/ohs-restore.XXXXXX")
in_container="/tmp/ohs-restore-$$.dump"
cleanup() {
  rm -rf "$work"
  if [ -n "${pg_up:-}" ]; then
    dc exec -T "$OHS_PG_SERVICE" rm -f "$in_container" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# --- 1. the archive ---------------------------------------------------------
log "checking $archive"
tar -C "$work" -xf "$archive" || die "cannot unpack $archive"
m="$work/manifest.txt"
[ -f "$m" ] || die "no manifest.txt in $archive: not an archive from scripts/backup.sh"
format=$(manifest_get "$m" format)
[ "$format" = "$OHS_BACKUP_FORMAT" ] || die "archive format '$format', this script reads format $OHS_BACKUP_FORMAT"
migration_version=$(manifest_get "$m" migration_version)
tsdb_version=$(manifest_get "$m" timescaledb_version)
pg_version=$(manifest_get "$m" postgres_version_num)
raw_included=$(manifest_get "$m" raw_included)
[[ "$migration_version" =~ ^[0-9]+$ ]] || die "the manifest has no schema version"
[[ "$tsdb_version" =~ ^[0-9][0-9A-Za-z.-]*$ ]] || die "the manifest has no TimescaleDB version"
[[ "$pg_version" =~ ^[0-9]+$ ]] || die "the manifest has no Postgres version"

files="database.dump"
[ "$raw_included" = yes ] && files="$files raw.tar.gz"
for f in $files; do
  [ -f "$work/$f" ] || die "$f is missing from the archive"
  want=$(manifest_get "$m" "sha256_$f")
  got=$(sha256_of "$work/$f")
  [ -n "$want" ] && [ "$want" = "$got" ] || die "$f: checksum mismatch (archive damaged)"
done
if [ "$raw_included" = yes ]; then
  gzip -t "$work/raw.tar.gz" || die "raw.tar.gz is damaged"
fi
log "archive ok: taken $(manifest_get "$m" created_at) by $(manifest_get "$m" ohs_version), schema version $migration_version, TimescaleDB $tsdb_version, raw files: $raw_included"

# --- 2. the target stack ------------------------------------------------------
log "starting the $OHS_PG_SERVICE service"
dc up -d --wait --wait-timeout 180 "$OHS_PG_SERVICE" >"$work/up.log" 2>&1 || {
  tail -n 20 "$work/up.log" >&2; die "cannot start $OHS_PG_SERVICE"; }
pg_up=1

# The dump's table of contents must be readable by this Postgres.
dc cp "$work/database.dump" "$OHS_PG_SERVICE:$in_container" >/dev/null 2>&1 ||
  die "cannot copy the dump into the $OHS_PG_SERVICE container"
dc exec -T "$OHS_PG_SERVICE" pg_restore --list "$in_container" >"$work/toc.txt" 2>&1 || {
  tail -n 5 "$work/toc.txt" >&2; die "pg_restore cannot read the dump"; }
log "dump ok: $(grep -c 'TABLE DATA' "$work/toc.txt" || true) tables with data"

if [ "$verify_only" = 1 ]; then
  log "verify only: nothing changed"
  exit 0
fi

target_pg=$(psql_q postgres 'SHOW server_version_num')
if [ "$((target_pg / 10000))" -lt "$((pg_version / 10000))" ]; then
  die "the archive comes from Postgres $((pg_version / 10000)), this stack runs Postgres $((target_pg / 10000)): restore into the same major version or newer"
fi
if ! psql_q postgres "SELECT 1 FROM pg_available_extension_versions WHERE name = 'timescaledb' AND version = '$tsdb_version'" | grep -q 1; then
  die "TimescaleDB $tsdb_version is not available in this $OHS_PG_SERVICE image: use the database image the backup was taken with"
fi

latest_out=$(dc run --rm --no-deps -T "$OHS_APP_SERVICE" migrate latest 2>"$work/latest.log") || latest_out=""
latest=$(sed -n 's/^latest schema version \([0-9][0-9]*\)$/\1/p' <<<"$latest_out")
if [ -n "$latest" ]; then
  if [ "$migration_version" -gt "$latest" ]; then
    die "the archive's schema version $migration_version is newer than this ohs image ($latest): run the release that took the backup, or newer"
  fi
  [ "$migration_version" -eq "$latest" ] ||
    note "the archive is at schema version $migration_version; ohs migrates it to $latest when it starts"
elif [ "${OHS_RESTORE_SKIP_VERSION_CHECK:-0}" = 1 ]; then
  note "OHS_RESTORE_SKIP_VERSION_CHECK=1: the schema version of the ohs image is not checked"
else
  die "cannot read the schema version of the ohs image (\`ohs migrate latest\` came after 0.0.2). Use a newer image, or set OHS_RESTORE_SKIP_VERSION_CHECK=1 if the archive was taken by this same release"
fi

if [ "$yes" != 1 ]; then
  [ -t 0 ] || die "restore replaces the database and raw files: pass --yes (or OHS_RESTORE_YES=1) to run without a terminal"
  printf 'This REPLACES database %s and the raw files of this stack. Type "restore" to go on: ' "$OHS_PG_DATABASE" >&2
  read -r answer
  [ "$answer" = restore ] || die "cancelled"
fi

# --- 3. the database ----------------------------------------------------------
log "stopping $OHS_APP_SERVICE"
dc stop "$OHS_APP_SERVICE" >/dev/null 2>&1 || true

log "recreating database $OHS_PG_DATABASE"
# From template0: the TimescaleDB image installs the extension into template1
# at its own default version, and it must be the archive's version instead.
psql_q postgres "DROP DATABASE IF EXISTS \"$OHS_PG_DATABASE\" WITH (FORCE)" >/dev/null
psql_q postgres "CREATE DATABASE \"$OHS_PG_DATABASE\" OWNER \"$OHS_PG_USER\" TEMPLATE template0" >/dev/null
psql_q "$OHS_PG_DATABASE" "CREATE EXTENSION timescaledb VERSION '$tsdb_version'" >/dev/null
psql_q "$OHS_PG_DATABASE" 'SELECT timescaledb_pre_restore()' >/dev/null

log "restoring the dump"
if ! dc exec -T "$OHS_PG_SERVICE" pg_restore -U "$OHS_PG_USER" -d "$OHS_PG_DATABASE" --exit-on-error "$in_container" >"$work/pg_restore.log" 2>&1; then
  tail -n 20 "$work/pg_restore.log" >&2
  psql_q "$OHS_PG_DATABASE" 'SELECT timescaledb_post_restore()' >/dev/null 2>&1 || true
  die "pg_restore failed: the database is incomplete, ohs is left stopped"
fi
psql_q "$OHS_PG_DATABASE" 'SELECT timescaledb_post_restore()' >/dev/null
dc exec -T "$OHS_PG_SERVICE" rm -f "$in_container" >/dev/null 2>&1 || true
restored=$(psql_q "$OHS_PG_DATABASE" 'SELECT version FROM schema_migrations')
[ "$restored" = "$migration_version" ] || die "restored schema version '$restored', the manifest says $migration_version"

# --- 4. the raw files ---------------------------------------------------------
app=$(container_id "$OHS_APP_SERVICE")
if [ -z "$app" ]; then
  dc up --no-start --no-deps "$OHS_APP_SERVICE" >"$work/create.log" 2>&1 || {
    tail -n 20 "$work/create.log" >&2; die "cannot create the $OHS_APP_SERVICE container"; }
  app=$(container_id "$OHS_APP_SERVICE")
fi
raw_path=$(raw_dir "$(raw_store_url "$app")")
if [ "$raw_included" = yes ] && [ -n "$raw_path" ]; then
  mounted_at "$app" "$raw_path" ||
    die "$raw_path is not on a volume of the $OHS_APP_SERVICE container"
  image=$(helper_image) || die "cannot find the $OHS_PG_SERVICE image"
  log "restoring the raw files into $raw_path"
  # Empty the directory first, so files written after the backup do not
  # linger; then unpack as root, which keeps the files' owner (the server's
  # user, 65532).
  # shellcheck disable=SC2016 # $1 is expanded by the container's sh
  docker run --rm -i --network none --user 0:0 --volumes-from "$app" --entrypoint sh "$image" \
    -c 'find "$1" -mindepth 1 -delete && tar -C "$1" -xzf -' sh "$raw_path" <"$work/raw.tar.gz" ||
    die "cannot restore the raw files"
elif [ "$raw_included" = yes ]; then
  note "the archive holds raw files but this stack uses an object store raw store: they were not copied."
  note "unpack raw.tar.gz from the archive and upload its contents to the bucket if you need them."
elif [ -n "$raw_path" ]; then
  note "the archive holds no raw files (it was taken with an S3 raw store); $raw_path is left as it is."
else
  note "raw files live in the object store; restore the bucket with its own versioning if needed."
fi

# --- 5. start -----------------------------------------------------------------
log "starting $OHS_APP_SERVICE"
dc up -d --wait --wait-timeout "$OHS_RESTORE_WAIT" "$OHS_APP_SERVICE" >"$work/up.log" 2>&1 || {
  tail -n 20 "$work/up.log" >&2; dc logs --tail 30 "$OHS_APP_SERVICE" >&2 || true
  die "$OHS_APP_SERVICE did not become healthy"; }
log "restored: $(dc exec -T "$OHS_APP_SERVICE" /ohs migrate version 2>/dev/null || echo 'schema version unknown')"
