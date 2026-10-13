#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Quickstart end to end: the README sequence against the real compose stack.
#
#   1. cp .env.example .env, set OHS_ENCRYPTION_KEY
#   2. docker compose -f deploy/docker-compose.yml up -d
#      (here with deploy/docker-compose.build.yml: the image is built from this
#      checkout, where the README pulls the published one)
#   3. docker compose exec ohs /ohs admin tenant create --name e2e  (API key)
#   4. POST /v1/users                                      (tenant key)
#   5. POST /v1/users/{id}/tokens {"scopes":["upload"]}    (tenant key)
#   6. POST /v1/ingest/apple-health, one call per part     (user token)
#   7. GET /v1/users/{id}/coverage until pending_jobs is 0 (tenant key)
#   8. read samples (heart rate, steps, SpO2), sleep and workouts back (tenant key)
#   9. ohs admin personal setup, the same upload for that person, then every
#      script of recipes/ against it (python3)
#
# The counts read back must equal what test/e2e/expect.go computes from the
# fixture (connectors/applehealth/testdata/e2e).
#
# Under `set -e` a failed helper stops the run; a value read for a check that
# cannot be read is empty, and check fails on it.
#
# Usage: test/e2e/quickstart.sh          (tears the stack down on exit)
#        KEEP=1 test/e2e/quickstart.sh   (leaves it running)
# Env:   OHS_E2E_PORT (host port, default 18080), OHS_E2E_TIMEOUT (seconds to
#        wait for processing, default 120), OHS_E2E_PROJECT (compose project,
#        default ohs-e2e; set it and the port to run beside another checkout)
set -euo pipefail

E2E_PROJECT="${OHS_E2E_PROJECT:-ohs-e2e}"
# shellcheck source=test/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
trap e2e_down EXIT
cd "$E2E_ROOT" || exit 1

FIXTURE="$E2E_ROOT/connectors/applehealth/testdata/e2e"
DAY=2026-01-10
TIMEOUT="${OHS_E2E_TIMEOUT:-120}"

e2e_need
step "expected values from the fixture"
EXPECT=$(go run ./test/e2e/expect.go "$FIXTURE") || die "expect.go"
eval "$EXPECT"
say "$(tr '\n' ' ' <<<"$EXPECT")"

step "1-2. env file and compose up (project $E2E_PROJECT, port $OHS_E2E_PORT)"
e2e_env || die "env file"
t=$(date +%s)
e2e_build || die "image build"
ok "image $OHS_E2E_IMAGE built in $(($(date +%s) - t)) s"
t=$(date +%s)
e2e_up || die "compose up"
ok "postgres and ohs healthy in $(($(date +%s) - t)) s"
api GET /readyz none || true
[ "$API_CODE" = "200" ] && ok "/readyz 200" || fail "/readyz -> $API_CODE $API_BODY"

step "3. ohs admin tenant create"
KEY=$(e2e_tenant_key e2e) || KEY=""
[ -n "$KEY" ] && ok "tenant e2e, API key issued" || die "no API key printed"

step "4-5. user and upload token"
USER_ID=$(e2e_create_user "$KEY" e2e-user) || die "create user"
ok "user $USER_ID"
TOKEN=$(e2e_mint_token "$KEY" "$USER_ID") || die "mint token"
ok "user token minted (scope upload)"

step "6. upload every part"
t=$(date +%s)
e2e_upload_dir "$TOKEN" "$FIXTURE" "$DAY" e2e || die "upload"
ok "$PARTS parts accepted in $(($(date +%s) - t)) s"
parts=("$FIXTURE"/*.ndjson); first=${parts[0]}
e2e_upload_part "$TOKEN" "$first" "$DAY" e2e || true
if [ "$API_CODE" = "200" ] && [ "$(jq -r .duplicate <<<"$API_BODY" 2>/dev/null)" = "true" ]; then
  ok "re-sent part answered 200 duplicate"
else
  fail "re-sent part -> $API_CODE $API_BODY"
fi

step "7. poll coverage until pending_jobs is 0 (timeout ${TIMEOUT} s)"
t=$(date +%s)
e2e_wait_processed "$KEY" "$USER_ID" "$TIMEOUT" || die "processing did not finish"
ok "processed in $(($(date +%s) - t)) s after the last upload"
check "parked jobs" "$(jq -r .parked_jobs <<<"$COVERAGE" || true)" 0
days=$(jq -r '[.days[] | select(.provider == "apple_health")] | length' <<<"$COVERAGE") || days=0
[ "${days:-0}" -gt 0 ] && ok "coverage lists $days apple_health days" || fail "coverage lists no apple_health day: $COVERAGE"

step "8. read back and compare with expect.go"
check "heart rate samples" "$(e2e_count "$KEY" "$USER_ID" '/samples?type=heart_rate')" "$HR_COUNT"
check "step count samples" "$(e2e_count "$KEY" "$USER_ID" '/samples?type=step_count')" "$STEP_COUNT"
check "step sum" "$(e2e_sum "$KEY" "$USER_ID" '/samples?type=step_count')" "$STEP_SUM"
check "SpO2 samples" "$(e2e_count "$KEY" "$USER_ID" '/samples?type=oxygen_saturation')" "$SPO2_COUNT"
check "sleep sessions" "$(e2e_count "$KEY" "$USER_ID" /sleep)" "$SLEEP_SESSIONS"
check "workouts" "$(e2e_count "$KEY" "$USER_ID" /workouts)" "$WORKOUTS"

step "9. personal setup and the recipes"
command -v python3 >/dev/null || die "python3 is required for the recipes"
ENV_FILE="$E2E_WORK/ohs.env"
e2e_compose exec -T ohs /ohs admin personal setup --url "http://localhost:$OHS_E2E_PORT" >"$ENV_FILE" ||
  die "ohs admin personal setup"
P_KEY=$(sed -n 's/^OHS_KEY=//p' "$ENV_FILE")
P_USER=$(sed -n 's/^OHS_USER=//p' "$ENV_FILE")
[ -n "$P_KEY" ] && [ -n "$P_USER" ] && ok "personal setup printed OHS_URL, OHS_KEY and OHS_USER" ||
  die "personal setup output: $(sed 's/ohs_sk_[A-Za-z0-9]*/ohs_sk_.../' "$ENV_FILE")"
again=$(e2e_compose exec -T ohs /ohs admin personal setup | sed -n 's/^OHS_USER=//p') || again=""
[ "$again" = "$P_USER" ] && ok "a second run keeps the user" || fail "second run user $again, want $P_USER"
P_TOKEN=$(e2e_mint_token "$P_KEY" "$P_USER") || die "mint token for the personal user"
e2e_upload_dir "$P_TOKEN" "$FIXTURE" "$DAY" personal || die "upload for the personal user"
e2e_wait_processed "$P_KEY" "$P_USER" "$TIMEOUT" || die "processing for the personal user"
# Windows long enough to reach the fixture's day from today.
SINCE=$(python3 -c "import datetime as d; print((d.date.today() - d.date.fromisoformat('$DAY')).days + 2)")
recipe() { # recipe NAME PATTERN ARGS...: recipes/NAME.py exits 0 and prints PATTERN
  local name=$1 want=$2 out
  shift 2
  if out=$(cd "$E2E_WORK" && OHS_ENV_FILE="$ENV_FILE" python3 "$E2E_ROOT/recipes/$name.py" "$@" 2>&1) &&
    grep -q -- "$want" <<<"$out"; then
    ok "recipe $name"
  else
    fail "recipe $name: $out"
  fi
}
recipe connect "apple_health"
recipe sleep-report "(apple_health)" --days "$SINCE"
recipe compare-sources "Only one source" --metric steps --days "$SINCE"
recipe weekly-digest "## Health, week of"
recipe dashboard "wrote health-dashboard.html" --days "$SINCE"
grep -q '<svg' "$E2E_WORK/health-dashboard.html" && ok "dashboard has charts" || fail "dashboard has no chart"
recipe export-all "daily.csv" --dir "$E2E_WORK/export"
head -n 1 "$E2E_WORK/export/samples.csv" | grep -q '^id,type,value' && ok "export-all wrote samples.csv" ||
  fail "samples.csv header: $(head -n 1 "$E2E_WORK/export/samples.csv")"
recipe ai-agent "claude mcp add --transport http open-health-sync http://localhost:$OHS_E2E_PORT/mcp"
# The fixture has no resting heart rate or HRV: the recipe says so and exits 1.
if out=$(OHS_ENV_FILE="$ENV_FILE" python3 recipes/heart-trend.py 2>&1); then
  fail "heart-trend exits 0 without data: $out"
else
  grep -q "no resting heart rate or HRV" <<<"$out" && ok "recipe heart-trend explains missing data" ||
    fail "heart-trend: $out"
fi

e2e_summary
