#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Backup and restore end to end on the compose stack:
#
#   1. build the image from this checkout, start the stack, create a tenant and
#      a user, upload the fixture the way quickstart.sh does, wait until it is
#      processed;
#   2. record the row count of every table, API reads (counts, the step sum, a
#      digest of the heart rate samples) and the raw files with their checksums
#      and owner;
#   3. scripts/backup.sh, then scripts/restore.sh --verify on the archive; no
#      secret in their output;
#   4. destroy the stack with its volumes (`down -v`), start it empty;
#   5. scripts/restore.sh refuses an archive whose schema version is newer
#      than the image, and changes nothing;
#   6. scripts/restore.sh --yes; the same tenant key works, every recorded value
#      comes back, the raw files are back byte for byte, and a re-sent part is
#      still answered as a duplicate.
#
# Usage: test/e2e/backup_restore.sh          (tears the stack down on exit)
#        KEEP=1 test/e2e/backup_restore.sh   (leaves it running)
# Env:   OHS_E2E_PROJECT (compose project, default ohs-backup), OHS_E2E_PORT
#        (default 18082), OHS_E2E_TIMEOUT (seconds to wait for processing,
#        default 120)
# ok and fail always succeed, so `test && ok || fail` is a plain if-else.
# shellcheck disable=SC2015
set -euo pipefail

E2E_PROJECT="${OHS_E2E_PROJECT:-ohs-backup}"
export OHS_E2E_PORT="${OHS_E2E_PORT:-18082}"
# shellcheck source=test/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
trap e2e_down EXIT
cd "$E2E_ROOT" || exit 1

FIXTURE="$E2E_ROOT/connectors/applehealth/testdata/e2e"
DAY=2026-01-10
TIMEOUT="${OHS_E2E_TIMEOUT:-120}"
RAW=/var/lib/ohs/raw

# The scripts drive the same compose project as lib.sh.
export OHS_COMPOSE_PROJECT="$E2E_PROJECT"
export OHS_COMPOSE_FILES="deploy/docker-compose.yml:deploy/docker-compose.build.yml:test/e2e/compose.e2e.yml"
export OHS_COMPOSE_ENV_FILE=""
export OHS_BACKUP_DIR="$E2E_WORK/backups"

# table_counts: "schema.table count" for every table of the ohs_* schemas.
table_counts() {
  local t
  for t in $(psql_q "SELECT table_schema || '.' || table_name FROM information_schema.tables
                     WHERE table_schema LIKE 'ohs\_%' AND table_type = 'BASE TABLE' ORDER BY 1"); do
    echo "$t $(psql_q "SELECT count(*) FROM $t")"
  done
}

# raw_files: "owner sha256 path" for every raw file, read through a throwaway
# container of the database image (the server image has no shell).
raw_files() {
  local app img
  app=$(e2e_compose ps -a -q ohs) && img=$(docker inspect --format '{{.Image}}' "$(e2e_compose ps -a -q postgres)") || return 1
  # shellcheck disable=SC2016 # expanded by the container's sh
  docker run --rm --network none --volumes-from "$app:ro" --entrypoint sh "$img" -c \
    'cd "$1" && find . -type f ! -name ".ohs-tmp-*" | sort | while read -r f; do
       echo "$(stat -c %u "$f") $(sha256sum "$f" | cut -d" " -f1) $f"; done' sh "$RAW"
}

# api_reads KEY USER: the values read back through the API.
api_reads() {
  echo "heart_rate $(e2e_count "$1" "$2" '/samples?type=heart_rate')"
  echo "step_count $(e2e_count "$1" "$2" '/samples?type=step_count')"
  echo "step_sum $(e2e_sum "$1" "$2" '/samples?type=step_count')"
  echo "spo2 $(e2e_count "$1" "$2" '/samples?type=oxygen_saturation')"
  echo "sleep $(e2e_count "$1" "$2" /sleep)"
  echo "workouts $(e2e_count "$1" "$2" /workouts)"
  echo "heart_rate_digest $(e2e_collect "$1" "$2" '/samples?type=heart_rate' 'tojson' | sort | { sha256sum 2>/dev/null || shasum -a 256; } | cut -d' ' -f1)"
}

# same LABEL GOT WANT: GOT and WANT are identical, non-empty texts.
same() {
  if [ -n "$3" ] && [ "$2" = "$3" ]; then
    ok "$1 ($(wc -l <<<"$3" | tr -d ' ') lines)"
  else
    fail "$1 differs"; diff <(echo "$3") <(echo "$2") | head -n 20 || true
  fi
}

e2e_need
step "expected values from the fixture"
EXPECT=$(go run ./test/e2e/expect.go "$FIXTURE") || die "expect.go"
eval "$EXPECT"

step "1. stack up, tenant, user, upload (project $E2E_PROJECT, port $OHS_E2E_PORT)"
e2e_env || die "env file"
e2e_build || die "image build"
e2e_up || die "compose up"
KEY=$(e2e_tenant_key backup) || KEY=""
[ -n "$KEY" ] || die "no API key printed"
USER_ID=$(e2e_create_user "$KEY" backup-user) || die "create user"
TOKEN=$(e2e_mint_token "$KEY" "$USER_ID") || die "mint token"
e2e_upload_dir "$TOKEN" "$FIXTURE" "$DAY" backup || die "upload"
e2e_wait_processed "$KEY" "$USER_ID" "$TIMEOUT" || die "processing did not finish"
ok "fixture uploaded and processed"

step "2. record what must come back"
READS_BEFORE=$(api_reads "$KEY" "$USER_ID") || die "API reads"
check "heart rate samples" "$(sed -n 's/^heart_rate //p' <<<"$READS_BEFORE")" "$HR_COUNT"
check "workouts" "$(sed -n 's/^workouts //p' <<<"$READS_BEFORE")" "$WORKOUTS"
COUNTS_BEFORE=$(table_counts) || die "table counts"
RAW_BEFORE=$(raw_files) || die "raw files"
n_raw=$(grep -c . <<<"$RAW_BEFORE" || true)
check "raw files" "$n_raw" "${PARTS:?}"
grep -q '^65532 ' <<<"$RAW_BEFORE" && ok "raw files owned by the server's user" || fail "raw file owner: $RAW_BEFORE"
SCHEMA_BEFORE=$(psql_q 'SELECT version FROM schema_migrations') || die "schema version"
say "schema version $SCHEMA_BEFORE, $(wc -l <<<"$COUNTS_BEFORE" | tr -d ' ') tables"

step "3. backup"
t=$(date +%s)
ARCHIVE=$(scripts/backup.sh 2>"$E2E_WORK/backup.log") || { cat "$E2E_WORK/backup.log"; die "backup.sh"; }
ok "backup.sh wrote $(basename "$ARCHIVE") in $(($(date +%s) - t)) s"
members=$(tar -tf "$ARCHIVE" | sort | tr '\n' ' ')
[ "$members" = "database.dump manifest.txt raw.tar.gz " ] && ok "archive holds $members" || fail "archive members: $members"
manifest=$(tar -xOf "$ARCHIVE" manifest.txt)
check "manifest schema version" "$(sed -n 's/^migration_version=//p' <<<"$manifest")" "$SCHEMA_BEFORE"
grep -q '^raw_included=yes$' <<<"$manifest" && ok "manifest: raw files included" || fail "manifest: $manifest"
grep -q '^ohs_version=ohs ' <<<"$manifest" && ok "manifest: $(grep '^ohs_version=' <<<"$manifest")" || fail "manifest has no ohs version"
# GNU stat takes -c, BSD stat (macOS) -f.
mode=$(stat -c %a "$ARCHIVE" 2>/dev/null || stat -f %Lp "$ARCHIVE")
[ "$mode" = 600 ] && ok "archive mode 600" || fail "archive mode $mode"
scripts/restore.sh --verify "$ARCHIVE" 2>"$E2E_WORK/verify.log" && ok "restore.sh --verify accepts it" ||
  { cat "$E2E_WORK/verify.log"; fail "restore.sh --verify"; }
ENC_KEY=$(sed -n 's/^OHS_ENCRYPTION_KEY=//p' "$OHS_E2E_ENV_FILE")
if grep -qF -e "$ENC_KEY" -e "$KEY" "$E2E_WORK/backup.log" "$E2E_WORK/verify.log"; then
  fail "a secret appears in the scripts' output"
else
  ok "no secret in the scripts' output"
fi

step "4. destroy the stack and its volumes, start it empty"
e2e_compose down -v --remove-orphans >"$E2E_WORK/down.log" 2>&1 || die "compose down -v"
left=$(docker volume ls -q --filter "label=com.docker.compose.project=$E2E_PROJECT")
[ -z "$left" ] && ok "no volume of $E2E_PROJECT left" || fail "volumes left: $left"
e2e_up || die "compose up (empty)"
api GET "/v1/users/$USER_ID/coverage" "$KEY" || true
[ "$API_CODE" = "401" ] && ok "empty stack: the old key is refused (401)" || fail "empty stack answered $API_CODE"
[ -z "$(raw_files)" ] && ok "empty stack: no raw file" || fail "empty stack has raw files"

step "5. restore refuses an archive newer than the image"
mkdir -p "$E2E_WORK/newer"
tar -C "$E2E_WORK/newer" -xf "$ARCHIVE"
# No sed -i: GNU and BSD sed disagree on its argument.
sed 's/^migration_version=.*/migration_version=99999/' "$E2E_WORK/newer/manifest.txt" >"$E2E_WORK/newer/manifest.new"
mv "$E2E_WORK/newer/manifest.new" "$E2E_WORK/newer/manifest.txt"
tar -C "$E2E_WORK/newer" -cf "$E2E_WORK/newer.tar" manifest.txt database.dump raw.tar.gz
if scripts/restore.sh --yes "$E2E_WORK/newer.tar" >"$E2E_WORK/newer.log" 2>&1; then
  fail "restore.sh accepted schema version 99999"
elif grep -q 'newer than this ohs image' "$E2E_WORK/newer.log"; then
  ok "refused: $(grep -o 'schema version 99999 is newer[^:]*' "$E2E_WORK/newer.log")"
else
  cat "$E2E_WORK/newer.log"; fail "restore.sh failed for another reason"
fi
api GET /readyz none || true
[ "$API_CODE" = "200" ] && ok "the refused restore left ohs running" || fail "/readyz -> $API_CODE after a refused restore"

step "6. restore"
t=$(date +%s)
scripts/restore.sh --yes "$ARCHIVE" 2>"$E2E_WORK/restore.log" || { cat "$E2E_WORK/restore.log"; die "restore.sh"; }
ok "restore.sh done in $(($(date +%s) - t)) s"
if grep -qF -e "$ENC_KEY" -e "$KEY" "$E2E_WORK/restore.log"; then fail "a secret appears in restore.sh's output"; fi
api GET /readyz none || true
[ "$API_CODE" = "200" ] && ok "/readyz 200" || fail "/readyz -> $API_CODE $API_BODY"
check "schema version" "$(psql_q 'SELECT version FROM schema_migrations')" "$SCHEMA_BEFORE"
# Counted before the reads below: every authenticated read adds an audit event.
same "row count of every table" "$(table_counts)" "$COUNTS_BEFORE"
same "API reads with the old tenant key" "$(api_reads "$KEY" "$USER_ID")" "$READS_BEFORE"
same "raw files (owner, SHA-256, path)" "$(raw_files)" "$RAW_BEFORE"
parts=("$FIXTURE"/*.ndjson)
e2e_upload_part "$TOKEN" "${parts[0]}" "$DAY" backup || true
if [ "$API_CODE" = "200" ] && [ "$(jq -r .duplicate <<<"$API_BODY" 2>/dev/null)" = "true" ]; then
  ok "the old upload token works and a re-sent part is a duplicate"
else
  fail "re-sent part -> $API_CODE $API_BODY"
fi

e2e_summary
