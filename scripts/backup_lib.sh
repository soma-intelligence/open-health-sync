# SPDX-License-Identifier: AGPL-3.0-only
# shellcheck shell=bash
#
# Shared by scripts/backup.sh and scripts/restore.sh: compose settings and
# helpers. Not meant to be run on its own.
#
# Settings (environment variables, defaults match deploy/docker-compose.yml):
#   OHS_COMPOSE_FILES     compose files, colon separated, relative to the
#                         repository root (default deploy/docker-compose.yml).
#                         Add the overlays you start the stack with, e.g.
#                         deploy/docker-compose.yml:deploy/docker-compose.pi.yml
#   OHS_COMPOSE_ENV_FILE  passed to compose as --env-file (default .env at the
#                         repository root when it exists; set it empty for none)
#   OHS_COMPOSE_PROJECT   compose project name (default: COMPOSE_PROJECT_NAME,
#                         else the file's own name, ohs)
#   OHS_PG_SERVICE        database service (default postgres)
#   OHS_APP_SERVICE       server service (default ohs)
#   OHS_PG_USER           database user (default ohs)
#   OHS_PG_DATABASE       database name (default ohs)
#
# Nothing here prints a secret: container environments are read for one
# variable (OHS_RAW_STORE) and never echoed.

OHS_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OHS_COMPOSE_FILES="${OHS_COMPOSE_FILES:-deploy/docker-compose.yml}"
if [ -z "${OHS_COMPOSE_ENV_FILE+set}" ] && [ -f "$OHS_REPO_ROOT/.env" ]; then
  OHS_COMPOSE_ENV_FILE="$OHS_REPO_ROOT/.env"
fi
OHS_COMPOSE_ENV_FILE="${OHS_COMPOSE_ENV_FILE:-}"
OHS_COMPOSE_PROJECT="${OHS_COMPOSE_PROJECT:-}"
OHS_PG_SERVICE="${OHS_PG_SERVICE:-postgres}"
OHS_APP_SERVICE="${OHS_APP_SERVICE:-ohs}"
OHS_PG_USER="${OHS_PG_USER:-ohs}"
OHS_PG_DATABASE="${OHS_PG_DATABASE:-ohs}"

# The archive layout this version writes and reads.
# shellcheck disable=SC2034 # read by backup.sh and restore.sh
OHS_BACKUP_FORMAT=1

log()  { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
note() { log "note: $*"; }
die()  { log "error: $*"; exit 1; }

# Identifiers end up in SQL: keep them to plain names.
for _v in OHS_PG_USER OHS_PG_DATABASE; do
  [[ "${!_v}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || die "$_v must be a plain identifier (letters, digits, _)"
done
unset _v

OHS_COMPOSE_ARGS=()
if [ -n "$OHS_COMPOSE_PROJECT" ]; then
  OHS_COMPOSE_ARGS+=(-p "$OHS_COMPOSE_PROJECT")
fi
if [ -n "$OHS_COMPOSE_ENV_FILE" ]; then
  OHS_COMPOSE_ARGS+=(--env-file "$OHS_COMPOSE_ENV_FILE")
fi
IFS=: read -r -a _files <<<"$OHS_COMPOSE_FILES"
for _f in "${_files[@]}"; do
  [ -n "$_f" ] || continue
  case "$_f" in /*) ;; *) _f="$OHS_REPO_ROOT/$_f" ;; esac
  OHS_COMPOSE_ARGS+=(-f "$_f")
done
unset _f _files

dc() { docker compose "${OHS_COMPOSE_ARGS[@]}" "$@"; }

need_tools() {
  local t
  for t in docker tar gzip; do
    command -v "$t" >/dev/null 2>&1 || die "$t is required"
  done
  docker compose version >/dev/null 2>&1 || die "docker compose v2 is required"
  sha256_cmd >/dev/null || die "sha256sum or shasum is required"
}

# sha256_cmd prints the checksum command available on this host.
sha256_cmd() {
  if command -v sha256sum >/dev/null 2>&1; then echo sha256sum
  elif command -v shasum >/dev/null 2>&1; then echo "shasum -a 256"
  else return 1
  fi
}

# sha256_of FILE: the hex digest only.
sha256_of() {
  local cmd
  cmd=$(sha256_cmd)
  $cmd "$1" | awk '{print $1}'
}

# container_id SERVICE: the id of the service's container, running or not,
# empty when it has none.
container_id() { dc ps -a -q "$1" 2>/dev/null | head -n 1; }

# running SERVICE: true when the service's container is running.
running() { [ -n "$(dc ps -q --status running "$1" 2>/dev/null)" ]; }

# psql_q DATABASE SQL: one query on the database service, unaligned output.
psql_q() {
  dc exec -T "$OHS_PG_SERVICE" psql -X -v ON_ERROR_STOP=1 -U "$OHS_PG_USER" -d "$1" -tAqc "$2"
}

# raw_store_url CONTAINER: the OHS_RAW_STORE the container runs with
# (the server's default when unset). Only that one variable is read.
raw_store_url() {
  local v
  v=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$1" |
    sed -n 's/^OHS_RAW_STORE=//p' | tail -n 1)
  echo "${v:-file:///var/lib/ohs/raw}"
}

# raw_dir URL: the directory of a file:// raw store, empty for any other.
raw_dir() {
  case "$1" in
    file://*) echo "${1#file://}" ;;
    *) echo "" ;;
  esac
}

# mounted_at CONTAINER DIR: true when DIR is, or is under, a mount of the
# container (a named volume or a bind mount), so it survives the container.
mounted_at() {
  local d
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    case "$2/" in "${d%/}/"*) return 0 ;; esac
  done < <(docker inspect --format '{{range .Mounts}}{{println .Destination}}{{end}}' "$1")
  return 1
}

# helper_image: the image of the database service's container. It has sh,
# tar and find, and is already on the host, so no other image is pulled.
helper_image() {
  local pg
  pg=$(container_id "$OHS_PG_SERVICE")
  [ -n "$pg" ] || return 1
  docker inspect --format '{{.Image}}' "$pg"
}

# manifest_get FILE KEY: the value of KEY=... in a manifest.
manifest_get() { sed -n "s/^$2=//p" "$1" | head -n 1; }
