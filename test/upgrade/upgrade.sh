#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Upgrade test: an install of the last release, with real data in it, moves to
# the code of this checkout without losing or changing anything.
#
#   1. build the old release from its tag's source, and this checkout
#   2. start Postgres with TimescaleDB (the image of deploy/docker-compose.yml)
#   3. old binary: migrate up, tenant and API key, user, upload token, the
#      Apple Health fixture of that release, wait for the ETL, check the
#      counts against that release's test/e2e/expect.go, then record every
#      table's row count and the API reads; stop it
#   4. new binary: migrate up on top of that data (row counts unchanged), then
#      `ohs up`: /readyz, the same API key and upload token, the same reads
#      return the same data, the endpoints added since answer, a new user's
#      upload is processed
#   5. down migrations back to the old release's schema version, the old
#      binary serves the same reads again, then up again with the new binary
#
# The binaries run on the host (no image build); only Postgres runs in Docker.
#
# Usage: test/upgrade/upgrade.sh          (removes the container on exit)
#        KEEP=1 test/upgrade/upgrade.sh   (leaves Postgres and the work dir)
# Env:   OHS_UPGRADE_FROM  release tag to upgrade from (default: the latest v*
#                          tag before HEAD, so a release commit is tested
#                          against the release before it)
#        OHS_UPGRADE_PORT  host port of the server (default 18082)
#        OHS_E2E_TIMEOUT   seconds to wait for processing (default 120)
# Needs docker, git, go, curl, jq, gzip.
set -euo pipefail

E2E_PROJECT=ohs-upgrade # lib.sh wants one; no compose project is created
export OHS_E2E_PORT="${OHS_UPGRADE_PORT:-18082}"
# shellcheck source=test/e2e/lib.sh
source "$(dirname "$0")/../e2e/lib.sh"
cd "$E2E_ROOT" || exit 1

TIMEOUT="${OHS_E2E_TIMEOUT:-120}"
DAY=2026-01-10
PG_IMAGE=$(sed -n 's/^ *image: *\(timescale\/timescaledb[^ ]*\).*/\1/p' deploy/docker-compose.yml | head -n 1)
PG_NAME="ohs-upgrade-pg-$$"
OLD_DIR="$E2E_WORK/old"
OLD_BIN="$E2E_WORK/ohs-old"
NEW_BIN="$E2E_WORK/ohs-new"
RAW_DIR="$E2E_WORK/raw"
SERVER_PID=""
SERVER_LOG=""

cleanup() {
  if [ -n "$SERVER_PID" ]; then kill -TERM "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi
  if [ "$E2E_FAILS" -gt 0 ] && [ -n "$SERVER_LOG" ] && [ -f "$SERVER_LOG" ]; then
    echo "--- last server logs ($SERVER_LOG, debug lines left out)"
    grep -v '"level":"DEBUG"' "$SERVER_LOG" | tail -n 40 || true
  fi
  if [ "${KEEP:-0}" = "1" ]; then
    say "KEEP=1: container $PG_NAME and $E2E_WORK left in place"
    return 0
  fi
  docker rm -f -v "$PG_NAME" >/dev/null 2>&1 || true
  rm -rf "$E2E_WORK"
}
trap cleanup EXIT

e2e_need
command -v git >/dev/null || die "git is required"
[ -n "$PG_IMAGE" ] || die "no timescale/timescaledb image in deploy/docker-compose.yml"

# latest_release: the newest v* tag reachable from HEAD's parent. On a commit
# that is itself tagged, that is the release before it.
latest_release() {
  git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD^ 2>/dev/null ||
    git tag --list 'v[0-9]*' --sort=-v:refname | head -n 1
}
FROM="${OHS_UPGRADE_FROM:-$(latest_release)}"
[ -n "$FROM" ] || die "no release tag found: fetch tags (git fetch --tags, or checkout with fetch-depth: 0) or set OHS_UPGRADE_FROM"
git rev-parse --verify --quiet "$FROM^{commit}" >/dev/null || die "$FROM is not a tag or commit here (git fetch --tags?)"

# ohs BIN ARGS...: one binary with only the variables below, so nothing from
# the caller's environment reaches it (an older release reads any OHS_X_Y as
# provider X's key Y).
ENC_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '\n')
DB_URL=""
ohs_env() {
  OHS_ENV=(PATH="$PATH" HOME="${HOME:-/tmp}" TZ=UTC
    OHS_DATABASE_URL="$DB_URL" OHS_ENCRYPTION_KEY="$ENC_KEY" OHS_PUBLIC_URL="$E2E_URL"
    OHS_RAW_STORE="file://$RAW_DIR" OHS_LISTEN="127.0.0.1:$OHS_E2E_PORT" OHS_LOG_LEVEL=info
    OHS_WEBHOOK_ALLOW_PRIVATE=true)
}
ohs() {
  local bin=$1
  shift
  ohs_env
  env -i "${OHS_ENV[@]}" "$bin" "$@"
}

# start BIN NAME ARGS...: runs `ohs ARGS` in the background and waits for /readyz.
# The background job is env itself (it execs the binary), not a subshell, so
# SERVER_PID is the server's and SIGTERM reaches it.
start() {
  local bin=$1 name=$2 deadline
  shift 2
  SERVER_LOG="$E2E_WORK/$name.log"
  # Something else on the port would answer for the server under test.
  if curl -s -o /dev/null "$E2E_URL/healthz"; then
    echo "something already listens on $E2E_URL: stop it or set OHS_UPGRADE_PORT" >&2
    return 1
  fi
  ohs_env
  env -i "${OHS_ENV[@]}" "$bin" "$@" >"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  deadline=$(($(date +%s) + 60))
  while :; do
    kill -0 "$SERVER_PID" 2>/dev/null || { tail -n 30 "$SERVER_LOG"; SERVER_PID=""; return 1; }
    api GET /readyz none 2>/dev/null || true
    [ "$API_CODE" = "200" ] && return 0
    [ "$(date +%s)" -lt "$deadline" ] || { echo "/readyz -> $API_CODE $API_BODY after 60 s" >&2; return 1; }
    sleep 0.5
  done
}

# stop: SIGTERM, as compose does, and wait for a clean exit.
stop() {
  local rc=0
  kill -TERM "$SERVER_PID"
  wait "$SERVER_PID" || rc=$?
  SERVER_PID=""
  [ "$rc" -eq 0 ] || { echo "server exited $rc on SIGTERM" >&2; return 1; }
}

psql_q() {
  docker exec "$PG_NAME" psql -v ON_ERROR_STOP=1 -U ohs -d ohs -tAqc "$1"
}

# schema_version BIN: "N" or "N (dirty)" from `ohs migrate version`.
schema_version() {
  ohs "$1" migrate version | sed -n 's/^schema version //p'
}

# table_counts: "schema.table count" for every table of the three schemas.
table_counts() {
  psql_q "SELECT table_schema || '.' || table_name || ' ' ||
      (xpath('/row/c/text()', query_to_xml(format('SELECT count(*) AS c FROM %I.%I', table_schema, table_name), false, true, '')))[1]::text
    FROM information_schema.tables
    WHERE table_schema IN ('ohs_core', 'ohs_sync', 'ohs_data') AND table_type = 'BASE TABLE'
    ORDER BY 1"
}

# The reads compared before and after: every list route that holds the
# fixture's data, plus the user and the coverage days.
READS=(
  '/samples?type=heart_rate'
  '/samples?type=step_count'
  '/samples?type=oxygen_saturation'
  '/category-samples'
  '/sleep'
  '/workouts'
  '/daily'
)

# snapshot DIR KEY USER: every READS route, one JSON object per line sorted,
# plus the user record and the coverage days.
snapshot() {
  local dir=$1 i=0 r
  mkdir -p "$dir"
  for r in "${READS[@]}"; do
    e2e_collect "$2" "$3" "$r" '. | tojson' | sort >"$dir/$i.ndjson" || { echo "read $r failed" >&2; return 1; }
    i=$((i + 1))
  done
  api GET "/v1/users/$3" "$2" || true
  [ "$API_CODE" = "200" ] || { echo "GET user -> $API_CODE $API_BODY" >&2; return 1; }
  jq -c . <<<"$API_BODY" >"$dir/user.json"
  api GET "/v1/users/$3/coverage?start=$DAY&end=$DAY" "$2" || true
  [ "$API_CODE" = "200" ] || { echo "GET coverage -> $API_CODE $API_BODY" >&2; return 1; }
  jq -c '[.days[] | select(.provider == "apple_health")]' <<<"$API_BODY" >"$dir/coverage.json"
}

# same_as OLD NEW: true when every object of OLD is in NEW with the same
# value for each of OLD's fields, and NEW has no other object. Fields a newer
# release adds are allowed; changed or missing ones are not.
same_as() {
  jq -n --slurpfile a "$1" --slurpfile b "$2" '
    def key: if type == "object" and has("id") then (.id | tostring) else tojson end;
    ($a | map({key: key, value: .}) | from_entries) as $A |
    ($b | map({key: key, value: .}) | from_entries) as $B |
    ($A | keys) == ($B | keys) and
    all($A | to_entries[]; .value as $o | $B[.key] as $n |
      if ($o | type) == "object" then all($o | to_entries[]; $n[.key] == .value) else $o == $n end)' |
    grep -qx true
}

# compare_reads OLD_DIR NEW_DIR LABEL
compare_reads() {
  local i=0 r
  for r in "${READS[@]}"; do
    if same_as "$1/$i.ndjson" "$2/$i.ndjson"; then
      ok "$3: $r, $(wc -l <"$1/$i.ndjson" | tr -d ' ') records unchanged"
    else
      fail "$3: $r differs (old $1/$i.ndjson, new $2/$i.ndjson)"
      diff <(jq -S . "$1/$i.ndjson") <(jq -S . "$2/$i.ndjson") | head -n 20 || true
    fi
    i=$((i + 1))
  done
  same_as "$1/user.json" "$2/user.json" && ok "$3: user record unchanged" || fail "$3: user record $(cat "$2/user.json"), was $(cat "$1/user.json")"
  # coverage.json holds one array: compare its elements by tojson.
  if same_as <(jq -c '.[]' "$1/coverage.json") <(jq -c '.[]' "$2/coverage.json"); then
    ok "$3: coverage days unchanged"
  else
    fail "$3: coverage $(cat "$2/coverage.json"), was $(cat "$1/coverage.json")"
  fi
}

step "1. build $FROM and this checkout"
mkdir -p "$OLD_DIR" "$RAW_DIR"
git archive --format=tar "$FROM" | tar -x -C "$OLD_DIR"
t=$(date +%s)
(cd "$OLD_DIR" && go build -ldflags "-X main.version=$FROM" -o "$OLD_BIN" ./cmd/ohs) || die "build $FROM"
go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o "$NEW_BIN" ./cmd/ohs || die "build this checkout"
ok "built in $(($(date +%s) - t)) s: $(ohs "$OLD_BIN" version) -> $(ohs "$NEW_BIN" version)"
OLD_MIGRATION=$(find "$OLD_DIR/internal/db/migrations" -name '*.up.sql' -exec basename {} \; | sort | tail -n 1 | sed 's/_.*//; s/^0*//')
NEW_MIGRATION=$(find internal/db/migrations -name '*.up.sql' -exec basename {} \; | sort | tail -n 1 | sed 's/_.*//; s/^0*//')
say "schema versions: $FROM $OLD_MIGRATION, this checkout $NEW_MIGRATION"
FIXTURE="$OLD_DIR/connectors/applehealth/testdata/e2e"
[ -d "$FIXTURE" ] || die "$FROM has no Apple Health fixture at connectors/applehealth/testdata/e2e"
EXPECT=$(cd "$OLD_DIR" && go run ./test/e2e/expect.go "$FIXTURE") || die "expect.go of $FROM"
eval "$EXPECT"
say "$(tr '\n' ' ' <<<"$EXPECT")"

step "2. Postgres ($PG_IMAGE)"
docker run -d --name "$PG_NAME" -e POSTGRES_USER=ohs -e POSTGRES_PASSWORD=ohs -e POSTGRES_DB=ohs \
  -p 127.0.0.1::5432 "$PG_IMAGE" >/dev/null || die "docker run postgres"
PG_PORT=$(docker port "$PG_NAME" 5432/tcp | head -n 1 | sed 's/.*://')
DB_URL="postgres://ohs:ohs@127.0.0.1:$PG_PORT/ohs?sslmode=disable&pool_max_conns=20"
deadline=$(($(date +%s) + 120))
# -h 127.0.0.1: the image's init server listens on the socket only, so TCP
# answers once the real server is up.
until docker exec "$PG_NAME" pg_isready -q -h 127.0.0.1 -U ohs -d ohs 2>/dev/null; do
  [ "$(date +%s)" -lt "$deadline" ] || die "postgres not ready after 120 s"
  sleep 1
done
ok "postgres ready on 127.0.0.1:$PG_PORT"

step "3. $FROM: install, data, snapshot"
ohs "$OLD_BIN" migrate up >/dev/null || die "$FROM migrate up"
check "$FROM schema version" "$(schema_version "$OLD_BIN")" "$OLD_MIGRATION"
KEY=$(ohs "$OLD_BIN" admin tenant create --name upgrade | grep -o 'ohs_sk_[A-Za-z0-9]*' | head -n 1) || KEY=""
[ -n "$KEY" ] && ok "tenant and API key" || die "no API key printed"
start "$OLD_BIN" old serve || die "$FROM serve"
ok "$FROM serving on $E2E_URL"
USER_ID=$(e2e_create_user "$KEY" upgrade-user) || die "create user"
TOKEN=$(e2e_mint_token "$KEY" "$USER_ID") || die "mint token"
ok "user $USER_ID with an upload token"
e2e_upload_dir "$TOKEN" "$FIXTURE" "$DAY" before || die "upload"
e2e_wait_processed "$KEY" "$USER_ID" "$TIMEOUT" || die "processing on $FROM"
ok "fixture processed"
check "heart rate samples" "$(e2e_count "$KEY" "$USER_ID" '/samples?type=heart_rate')" "$HR_COUNT"
check "step count samples" "$(e2e_count "$KEY" "$USER_ID" '/samples?type=step_count')" "$STEP_COUNT"
check "step sum" "$(e2e_sum "$KEY" "$USER_ID" '/samples?type=step_count')" "$STEP_SUM"
check "SpO2 samples" "$(e2e_count "$KEY" "$USER_ID" '/samples?type=oxygen_saturation')" "$SPO2_COUNT"
check "sleep sessions" "$(e2e_count "$KEY" "$USER_ID" /sleep)" "$SLEEP_SESSIONS"
check "workouts" "$(e2e_count "$KEY" "$USER_ID" /workouts)" "$WORKOUTS"
snapshot "$E2E_WORK/before" "$KEY" "$USER_ID" || die "snapshot on $FROM"
stop || fail "$FROM did not stop cleanly"
table_counts >"$E2E_WORK/tables-before.txt" || die "table counts"
ok "$FROM stopped; $(wc -l <"$E2E_WORK/tables-before.txt" | tr -d ' ') tables recorded"

step "4. this checkout: migrate up on $FROM's data"
t=$(date +%s)
ohs "$NEW_BIN" migrate up >/dev/null || die "migrate up from $FROM failed: $(schema_version "$NEW_BIN" || true)"
ok "migrations $((OLD_MIGRATION + 1))..$NEW_MIGRATION applied in $(($(date +%s) - t)) s"
check "schema version" "$(schema_version "$NEW_BIN")" "$NEW_MIGRATION"
table_counts >"$E2E_WORK/tables-after.txt" || die "table counts"
# Every table the old release had keeps its row count. Code catalogs
# (*_codes) may grow, as migrations seed new codes; the sync schema holds
# queues and cursors a migration may rewrite, so changes there are reported.
while read -r tbl n; do
  m=$(awk -v t="$tbl" '$1 == t {print $2}' "$E2E_WORK/tables-after.txt")
  if [ "$m" = "$n" ]; then continue; fi
  case "$tbl" in
    ohs_sync.*) say "note $tbl: $n rows before, ${m:-no table} after" ;;
    *_codes) if [ -n "$m" ] && [ "$m" -gt "$n" ]; then say "note $tbl: $n codes before, $m after (seeded)"
             else fail "$tbl: $n rows before, ${m:-no table} after"; fi ;;
    *) fail "$tbl: $n rows before, ${m:-no table} after" ;;
  esac
done <"$E2E_WORK/tables-before.txt"
ok "row counts compared ($(awk '{s += $2} END {print s}' "$E2E_WORK/tables-before.txt") rows before)"

start "$NEW_BIN" new up || die "ohs up after the upgrade"
ok "/readyz 200 after ohs up"
api GET /v1/providers "$KEY" || true
[ "$API_CODE" = "200" ] && ok "the $FROM API key still works" || fail "API key -> $API_CODE $API_BODY"
snapshot "$E2E_WORK/after" "$KEY" "$USER_ID" || die "snapshot after the upgrade"
compare_reads "$E2E_WORK/before" "$E2E_WORK/after" "after upgrade"

# Endpoints added since the oldest supported release, for the old user.
endpoint() { # LABEL PATH [JQ_TEST]: 200, and JQ_TEST true on the body when given
  api GET "$2" "$KEY" || true
  if [ "$API_CODE" != "200" ]; then fail "$1: GET $2 -> $API_CODE $API_BODY"; return; fi
  if [ -n "${3:-}" ] && ! jq -e "$3" <<<"$API_BODY" >/dev/null 2>&1; then
    fail "$1: GET $2 body fails $3: $(head -c 300 <<<"$API_BODY")"; return
  fi
  ok "$1: 200"
}
endpoint freshness "/v1/users/$USER_ID/freshness" '.data | type == "array"'
endpoint "FHIR Observations" "/v1/users/$USER_ID/fhir/Observation?type=heart_rate" '.resourceType == "Bundle" and ((.entry // []) | length > 0)'
endpoint "Open mHealth data points" "/v1/users/$USER_ID/omh/data-points?schema=heart-rate" '.data | length > 0'
api GET "/v1/users/$USER_ID/exports/samples?format=csv&type=heart_rate" "$KEY" || true
if [ "$API_CODE" = "200" ] && [ "$(($(wc -l <<<"$API_BODY") - 1))" = "$HR_COUNT" ]; then
  ok "CSV export: $HR_COUNT heart rate rows"
else
  fail "CSV export -> $API_CODE, $(($(wc -l <<<"$API_BODY") - 1)) rows, want $HR_COUNT"
fi
api POST /v1/webhooks "$KEY" '{"url":"http://127.0.0.1:9/upgrade-test"}' || true
[ "$API_CODE" = "201" ] && ok "webhook registered" || fail "POST /v1/webhooks -> $API_CODE $API_BODY"
endpoint webhooks /v1/webhooks '.data | length == 1'

# The upload token minted by the old release still uploads.
e2e_upload_part "$TOKEN" "$(find "$FIXTURE" -name '*.ndjson' | sort | head -n 1)" "$DAY" after-upgrade || true
[ "$API_CODE" = "200" ] && ok "the $FROM upload token still uploads" || fail "old token upload -> $API_CODE $API_BODY"
e2e_wait_processed "$KEY" "$USER_ID" "$TIMEOUT" || fail "re-upload not processed"
check "heart rate samples after a re-upload" "$(e2e_count "$KEY" "$USER_ID" '/samples?type=heart_rate')" "$HR_COUNT"

# A new user's upload goes through the new code end to end.
NEW_FIXTURE="$E2E_ROOT/connectors/applehealth/testdata/e2e"
NEW_EXPECT=$(go run ./test/e2e/expect.go "$NEW_FIXTURE") || die "expect.go"
USER2=$(e2e_create_user "$KEY" upgrade-user-2) || die "create user 2"
TOKEN2=$(e2e_mint_token "$KEY" "$USER2") || die "mint token 2"
e2e_upload_dir "$TOKEN2" "$NEW_FIXTURE" "$DAY" new || die "upload for user 2"
e2e_wait_processed "$KEY" "$USER2" "$TIMEOUT" || die "processing for user 2"
eval "$(sed 's/^/NEW_/' <<<"$NEW_EXPECT")"
check "new user heart rate samples" "$(e2e_count "$KEY" "$USER2" '/samples?type=heart_rate')" "$NEW_HR_COUNT"
check "new user sleep sessions" "$(e2e_count "$KEY" "$USER2" /sleep)" "$NEW_SLEEP_SESSIONS"
check "new user workouts" "$(e2e_count "$KEY" "$USER2" /workouts)" "$NEW_WORKOUTS"
stop || fail "the new server did not stop cleanly"

step "5. down migrations to $FROM's schema ($OLD_MIGRATION), then up again"
go run ./test/upgrade/migrateto internal/db/migrations "${DB_URL%%&pool_max_conns=*}" "$OLD_MIGRATION" || die "down migrations to $OLD_MIGRATION"
check "schema version after the down migrations" "$(schema_version "$OLD_BIN")" "$OLD_MIGRATION"
start "$OLD_BIN" old-again serve || die "$FROM serve after the down migrations"
ok "$FROM serves again after the down migrations"
snapshot "$E2E_WORK/downgraded" "$KEY" "$USER_ID" || die "snapshot on $FROM after the down migrations"
compare_reads "$E2E_WORK/before" "$E2E_WORK/downgraded" "after down"
stop || fail "$FROM did not stop cleanly"
start "$NEW_BIN" new-again up || die "ohs up after the down and up migrations"
check "schema version after up again" "$(schema_version "$NEW_BIN")" "$NEW_MIGRATION"
snapshot "$E2E_WORK/again" "$KEY" "$USER_ID" || die "snapshot after up again"
compare_reads "$E2E_WORK/before" "$E2E_WORK/again" "after up again"
check "new user heart rate samples after up again" "$(e2e_count "$KEY" "$USER2" '/samples?type=heart_rate')" "$NEW_HR_COUNT"
stop || fail "the new server did not stop cleanly"

e2e_summary
