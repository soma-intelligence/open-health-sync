# SPDX-License-Identifier: AGPL-3.0-only
#
# Shared helpers of the end to end suites (quickstart.sh, reliability.sh).
# Source it after setting E2E_PROJECT (the compose project name). It needs
# docker with compose v2.24 or later, curl, jq and go.
#
# Every container, volume and network a suite creates belongs to its compose
# project; teardown removes that project only (`down -v`). No image is pruned.
#
# The suites run under `set -euo pipefail`. Helpers return non-zero on error
# (api on a transport error, psql_q on an SQL or exec error); a caller that
# expects a failure handles it explicitly (`|| ...`).

E2E_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
: "${E2E_PROJECT:?set E2E_PROJECT before sourcing lib.sh}"
export OHS_E2E_PORT="${OHS_E2E_PORT:-18080}"
export OHS_E2E_IMAGE="${OHS_E2E_IMAGE:-ohs-e2e}"
E2E_URL="http://127.0.0.1:${OHS_E2E_PORT}"
E2E_WORK="$(mktemp -d "${TMPDIR:-/tmp}/ohs-e2e.XXXXXX")"
export OHS_E2E_ENV_FILE="$E2E_WORK/ohs.env"
E2E_T0=$(date +%s)
E2E_FAILS=0

say()  { printf '[%4ss] %s\n' "$(($(date +%s) - E2E_T0))" "$*"; }
step() { printf '\n== %s\n' "$*"; }
ok()   { say "ok   $*"; }
fail() { say "FAIL $*"; E2E_FAILS=$((E2E_FAILS + 1)); }
die()  { say "FAIL $*"; E2E_FAILS=$((E2E_FAILS + 1)); exit 1; }

e2e_need() {
  local t
  for t in docker curl jq go gzip; do
    command -v "$t" >/dev/null 2>&1 || die "$t is required"
  done
  docker compose version >/dev/null 2>&1 || die "docker compose v2 is required"
}

e2e_compose() {
  docker compose -p "$E2E_PROJECT" \
    -f "$E2E_ROOT/deploy/docker-compose.yml" \
    -f "$E2E_ROOT/deploy/docker-compose.build.yml" \
    -f "$E2E_ROOT/test/e2e/compose.e2e.yml" "$@"
}

# e2e_env writes the server's env file the way the README does
# (cp .env.example .env, then set a fresh encryption key), outside the
# repository so a developer's own .env is never touched.
e2e_env() {
  local key v
  for v in OHS_ENCRYPTION_KEY OHS_PUBLIC_URL; do
    grep -q "^$v=" "$E2E_ROOT/.env.example" || { echo ".env.example has no $v= line" >&2; return 1; }
  done
  key=$(head -c 32 /dev/urandom | base64 | tr -d '\n')
  sed -e "s|^OHS_ENCRYPTION_KEY=.*|OHS_ENCRYPTION_KEY=${key}|" \
      -e "s|^OHS_PUBLIC_URL=.*|OHS_PUBLIC_URL=${E2E_URL}|" \
      "$E2E_ROOT/.env.example" >"$OHS_E2E_ENV_FILE"
}

# The fallback without buildx, shared with users who build by hand.
# shellcheck source=scripts/image-without-buildx.sh
source "$E2E_ROOT/scripts/image-without-buildx.sh"

# e2e_build_flags_match DOCKERFILE: true when its go build RUN is the one the
# host fallback reproduces.
e2e_build_flags_match() { ohs_build_flags_match "$@"; }

# e2e_build builds the server image from this checkout. Compose builds it
# first, through the deploy/docker-compose.build.yml overlay. deploy/Dockerfile
# cross-compiles with `FROM --platform=$BUILDPLATFORM`, which only BuildKit
# understands. When compose falls back to the classic
# builder (no buildx plugin) the build is retried with buildx if present.
# Without buildx the image is built by scripts/image-without-buildx.sh (host
# cross-compile, then the Dockerfile's final stage). A classic multi-stage
# build would also work but leaves a dangling half-gigabyte build stage behind
# on every run. deploy/Dockerfile is never changed.
e2e_build() {
  local log="$E2E_WORK/build.log"
  if e2e_compose build ohs >"$log" 2>&1; then
    return 0
  fi
  if docker buildx version >/dev/null 2>&1; then
    say "compose build failed, retrying with docker buildx"
    docker buildx build --load -f "$E2E_ROOT/deploy/Dockerfile" -t "$OHS_E2E_IMAGE" "$E2E_ROOT" >>"$log" 2>&1 && return 0
  else
    say "compose build failed (classic builder), building the final stage around a host cross-compile"
    ohs_image_without_buildx "$E2E_ROOT" "$OHS_E2E_IMAGE" "$E2E_WORK" e2e >>"$log" 2>&1 && return 0
  fi
  tail -n 30 "$log"
  return 1
}

# e2e_up starts the stack and waits until both containers are healthy. The
# first call removes what a previous run of this project may have left
# (containers and volumes of this project only); later calls (a restart) keep
# the data.
E2E_STARTED=""
e2e_up() {
  if [ -z "$E2E_STARTED" ]; then
    e2e_compose down -v --remove-orphans >"$E2E_WORK/down.log" 2>&1 || { tail -n 20 "$E2E_WORK/down.log"; return 1; }
    E2E_STARTED=1
  fi
  e2e_compose up -d --no-build --wait --wait-timeout 180 >"$E2E_WORK/up.log" 2>&1 || {
    tail -n 30 "$E2E_WORK/up.log"; e2e_compose logs --tail 50 ohs || true; return 1; }
  # The container healthcheck runs `ohs doctor --quick`, which can pass before
  # the HTTP listener accepts connections, so wait for /readyz on the
  # published port too.
  local i code
  for i in $(seq 1 60); do
    code=$(curl -s -o /dev/null -w '%{http_code}' "$E2E_URL/readyz" || true)
    [ "$code" = "200" ] && return 0
    sleep 1
  done
  echo "/readyz never answered 200 on $E2E_URL (last: $code)"; e2e_compose logs --tail 50 ohs || true
  return 1
}

e2e_down() {
  if [ "$E2E_FAILS" -gt 0 ]; then
    echo "--- last server logs (debug lines left out)"
    e2e_compose logs --no-color --tail 200 ohs 2>/dev/null | grep -v '"level":"DEBUG"' | tail -n 40 || true
  fi
  if [ "${KEEP:-0}" = "1" ]; then
    say "KEEP=1: project $E2E_PROJECT left running on $E2E_URL (env file $OHS_E2E_ENV_FILE)"
    return 0
  fi
  e2e_compose down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$E2E_WORK"
}

# e2e_tenant_key runs `ohs admin tenant create` inside the server container
# and prints the API key it shows once.
e2e_tenant_key() {
  e2e_compose exec -T ohs /ohs admin tenant create --name "$1" | grep -o 'ohs_sk_[A-Za-z0-9]*' | head -n 1
}

# api METHOD PATH SECRET [JSON]: the body lands in API_BODY, the status in
# API_CODE (globals, so never call it inside $(...)). Returns non-zero when no
# HTTP answer came back (API_CODE 000); any HTTP status is a success here, the
# caller checks API_CODE.
API_CODE=""
API_BODY=""
api() {
  local args=(-sS -X "$1" "$E2E_URL$2" -H "Authorization: Bearer $3" -o "$E2E_WORK/body" -w '%{http_code}')
  [ -z "${4:-}" ] || args+=(-H 'Content-Type: application/json' --data "$4")
  rm -f "$E2E_WORK/body"
  if ! API_CODE=$(curl "${args[@]}"); then
    API_CODE=000 API_BODY=""
    return 1
  fi
  API_BODY=$(cat "$E2E_WORK/body" 2>/dev/null || true)
}

e2e_create_user() { # KEY [EXTERNAL_REF] -> user id
  api POST /v1/users "$1" "{\"external_ref\":\"${2:-e2e}\"}" || true
  [ "$API_CODE" = "201" ] || { echo "POST /v1/users -> $API_CODE $API_BODY" >&2; return 1; }
  jq -r .id <<<"$API_BODY"
}

e2e_mint_token() { # KEY USER -> token
  api POST "/v1/users/$2/tokens" "$1" '{"scopes":["upload"]}' || true
  [ "$API_CODE" = "201" ] || { echo "POST tokens -> $API_CODE $API_BODY" >&2; return 1; }
  jq -r .token <<<"$API_BODY"
}

# e2e_upload_part TOKEN FILE DAY BATCH: gzips FILE (<part>_<type>.ndjson) and
# uploads it; the body lands in API_BODY, the status in API_CODE.
e2e_upload_part() {
  local base part typ
  base=$(basename "$2" .ndjson); part=${base%%_*}; typ=${base#*_}
  gzip -c "$2" >"$E2E_WORK/part.gz"
  rm -f "$E2E_WORK/body"
  API_CODE=$(curl -sS -X POST "$E2E_URL/v1/ingest/apple-health" \
    -H "Authorization: Bearer $1" -H 'Content-Type: application/gzip' \
    -H "X-OHS-Type: $typ" -H "X-OHS-Day: $3" -H "X-OHS-Batch: $4" -H "X-OHS-Part: $part" \
    --data-binary "@$E2E_WORK/part.gz" -o "$E2E_WORK/body" -w '%{http_code}') || { API_CODE=000 API_BODY=""; return 1; }
  API_BODY=$(cat "$E2E_WORK/body" 2>/dev/null || true)
}

# e2e_upload_dir TOKEN DIR DAY BATCH: uploads every part, fails on any non-200.
e2e_upload_dir() {
  local f n=0
  for f in "$2"/*.ndjson; do
    e2e_upload_part "$1" "$f" "$3" "$4" || true
    [ "$API_CODE" = "200" ] || { echo "upload $(basename "$f") -> $API_CODE $API_BODY" >&2; return 1; }
    n=$((n + 1))
  done
  say "uploaded $n parts from $2"
}

# e2e_wait_processed KEY USER TIMEOUT_S: polls the coverage report until
# pending_jobs is 0 (ruling R4). Leaves the last report in COVERAGE.
COVERAGE=""
e2e_wait_processed() {
  local deadline=$(($(date +%s) + $3)) pending
  while :; do
    api GET "/v1/users/$2/coverage" "$1" || true  # a refused connection is retried
    COVERAGE=$API_BODY
    if [ "$API_CODE" = "200" ]; then
      pending=$(jq -r .pending_jobs <<<"$COVERAGE") || pending=""
      [ "$pending" = "0" ] && return 0
    fi
    [ "$(date +%s)" -lt "$deadline" ] || { echo "still pending after $3 s: $COVERAGE" >&2; return 1; }
    sleep 1
  done
}

# e2e_collect KEY USER PATH JQ_EXPR: pages through a list route (limit 1000)
# and prints every value JQ_EXPR yields from each page's data, one per line.
e2e_collect() {
  local cursor="" sep
  case "$3" in *\?*) sep='&' ;; *) sep='?' ;; esac
  while :; do
    api GET "/v1/users/$2$3${sep}limit=1000${cursor:+&cursor=$(jq -rn --arg c "$cursor" '$c|@uri')}" "$1" || true
    [ "$API_CODE" = "200" ] || { echo "GET $3 -> $API_CODE $API_BODY" >&2; return 1; }
    jq -r ".data[] | $4" <<<"$API_BODY"
    cursor=$(jq -r '.next_cursor // empty' <<<"$API_BODY")
    [ -n "$cursor" ] || return 0
  done
}

# e2e_count / e2e_sum KEY USER PATH: print nothing and fail when a page
# cannot be read, so check sees an empty value and fails.
e2e_count() {
  local v
  v=$(e2e_collect "$1" "$2" "$3" '.id') || return 1
  if [ -z "$v" ]; then echo 0; else wc -l <<<"$v" | tr -d ' '; fi
}
e2e_sum() {
  local v
  v=$(e2e_collect "$1" "$2" "$3" '.value') || return 1
  awk '{s += $1} END {printf "%.3f\n", s}' <<<"$v"
}

# is_number V: an optional minus, digits, optional decimals.
is_number() { [[ "$1" =~ ^-?[0-9]+(\.[0-9]+)?$ ]]; }

# same_number A B: both are numbers and agree to 1e-6.
same_number() {
  is_number "$1" && is_number "$2" &&
    awk -v a="$1" -v b="$2" 'BEGIN { d = a - b; if (d < 0) d = -d; exit !(d < 1e-6) }'
}

# check LABEL GOT WANT: counts a failure unless GOT and WANT are numbers that
# match; an empty or non-numeric value (a failed query) always fails.
check() {
  if same_number "$2" "$3"; then ok "$1: $2"; else fail "$1: got '$2', want '$3'"; fi
}

# psql_q SQL: the result of one query on the compose database; non-zero on
# any error.
psql_q() {
  e2e_compose exec -T postgres psql -v ON_ERROR_STOP=1 -U ohs -d ohs -tAqc "$1"
}

# kill_decision ROWS TOTAL UNFINISHED: when the reliability suite should
# SIGKILL the server. "partial" once some rows have landed and jobs remain,
# "late" when no job remains (too late to crash mid-processing), else "wait".
kill_decision() {
  if ! is_number "$1" || ! is_number "$2" || ! is_number "$3"; then echo wait; return; fi
  if [ "$3" -eq 0 ]; then echo late
  elif [ "$1" -gt 0 ] && [ "$1" -lt "$2" ]; then echo partial
  else echo wait
  fi
}

e2e_summary() {
  step "summary"
  if [ "$E2E_FAILS" -eq 0 ]; then
    say "all checks passed in $(($(date +%s) - E2E_T0)) s"
  else
    say "$E2E_FAILS check(s) failed"
    return 1
  fi
}
