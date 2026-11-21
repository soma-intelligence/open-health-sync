#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# The React admin console end to end, against the real compose stack.
#
#   1. env file from .env.example with OHS_CONSOLE=true, image built from this
#      checkout, compose up (Postgres/TimescaleDB and ohs in Docker)
#   2. tenants e2e-admin (the first one, so the instance admin) and e2e-other
#   3. ohs seed --demo for e2e-admin, plus the Apple Health fixture uploaded
#      through the API for a second user, processed by the ETL
#   4. Playwright in its own container (mcr.microsoft.com/playwright, host
#      network) walks every console page in light and dark, takes a full page
#      screenshot of each and fails on a page error, a console error or a 5xx
#
# Nothing but docker, curl and jq on the host: no Node, no browser, no Go.
# Screenshots and the HTML report land in $OHS_UI_OUT (default
# test/e2e/console-ui/out).
#
# OHS_UI_AUDIT=1 also runs the audit probes (audit.spec.ts): axe, keyboard,
# empty, loading, error, offline, session, forms, phone width, large text.
#
# Usage: test/e2e/console_ui.sh          (tears the stack down on exit)
#        KEEP=1 test/e2e/console_ui.sh   (leaves it running)
set -euo pipefail

E2E_PROJECT="${OHS_E2E_PROJECT:-ohs-e2e-ui}"
export OHS_E2E_PORT="${OHS_E2E_PORT:-18081}"
# shellcheck source=test/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
trap e2e_down EXIT
cd "$E2E_ROOT" || exit 1

PW_VERSION=$(jq -r '.devDependencies["@playwright/test"]' web/console/package.json)
PW_IMAGE="mcr.microsoft.com/playwright:v${PW_VERSION}-noble"
OUT="${OHS_UI_OUT:-$E2E_ROOT/test/e2e/console-ui/out}"
FIXTURE="$E2E_ROOT/connectors/applehealth/testdata/e2e"
DAY=2026-01-10

for c in docker curl jq; do command -v "$c" >/dev/null || die "$c is required"; done
docker compose version >/dev/null 2>&1 || die "docker compose v2 is required"

step "1. env file (OHS_CONSOLE=true), image, compose up (project $E2E_PROJECT, port $OHS_E2E_PORT)"
e2e_env || die "env file"
sed -i -e 's|^OHS_CONSOLE=.*|OHS_CONSOLE=true|' "$OHS_E2E_ENV_FILE"
grep -q '^OHS_CONSOLE=true$' "$OHS_E2E_ENV_FILE" || die "OHS_CONSOLE not set in the env file"
t=$(date +%s)
e2e_build || die "image build"
ok "image $OHS_E2E_IMAGE built in $(($(date +%s) - t)) s"
e2e_up || die "compose up"
ok "postgres and ohs healthy"
code=$(curl -s -o /dev/null -w '%{http_code}' "$E2E_URL/console/sign-in")
[ "$code" = "200" ] && ok "/console/sign-in 200" || die "/console/sign-in -> $code"

step "2. tenants"
out=$(e2e_compose exec -T ohs /ohs admin tenant create --name e2e-admin) || die "tenant e2e-admin"
ADMIN_KEY=$(grep -o 'ohs_sk_[A-Za-z0-9]*' <<<"$out" | head -n 1)
ADMIN_ID=$(grep -oE '\(id [0-9a-f-]{36}' <<<"$out" | cut -c5-)
[ -n "$ADMIN_KEY" ] && [ -n "$ADMIN_ID" ] && ok "e2e-admin $ADMIN_ID" || die "no key or id: $out"
out=$(e2e_compose exec -T ohs /ohs admin tenant create --name e2e-other) || die "tenant e2e-other"
OTHER_KEY=$(grep -o "ohs_sk_[A-Za-z0-9]*" <<<"$out" | head -n 1)
ok "e2e-other"

step "3. data: seed --demo and the Apple Health fixture"
e2e_compose exec -T ohs /ohs seed --demo --tenant "$ADMIN_ID" >"$E2E_WORK/seed.log" 2>&1 || { tail -n 20 "$E2E_WORK/seed.log"; die "seed --demo"; }
ok "seed --demo"
USER_ID=$(e2e_create_user "$ADMIN_KEY" apple-health-user) || die "create user"
TOKEN=$(e2e_mint_token "$ADMIN_KEY" "$USER_ID") || die "mint token"
e2e_upload_dir "$TOKEN" "$FIXTURE" "$DAY" ui || die "upload"
e2e_wait_processed "$ADMIN_KEY" "$USER_ID" 120 || die "processing did not finish"
ok "fixture uploaded and processed for user $USER_ID"

step "4. Playwright $PW_VERSION in Docker against $E2E_URL"
rm -rf "$OUT"; mkdir -p "$OUT"
set +e
docker run --rm --network host --ipc host \
  -e CI=1 -e OHS_UI_URL="$E2E_URL" -e OHS_UI_ADMIN_KEY="$ADMIN_KEY" -e OHS_UI_OTHER_KEY="$OTHER_KEY" -e OHS_UI_AUDIT="${OHS_UI_AUDIT:-0}" -e OHS_UI_OUT=/out \
  -v "$E2E_ROOT/test/e2e/console-ui:/src:ro" -v "$OUT:/out" \
  "$PW_IMAGE" bash -c "
    set -e
    mkdir -p /w && cp /src/package.json /src/playwright.config.ts /src/*.spec.ts /w/ && cd /w
    npm install --no-audit --no-fund --loglevel=error @playwright/test@$PW_VERSION @axe-core/playwright@4.11.0
    npx playwright test
  " 2>&1 | tee "$E2E_WORK/playwright.log"
rc=${PIPESTATUS[0]}
set -e
n=$(find "$OUT" -name '*.png' | wc -l)
say "$n screenshots in $OUT"
[ "$rc" = "0" ] && ok "console UI tests passed" || fail "console UI tests failed (exit $rc)"

[ "$E2E_FAILS" -eq 0 ] || exit 1
say "console UI end to end passed"
