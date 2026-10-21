#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# ohs-bridge end to end, on the real compose stack and without a radio: a
# spool file as `ohs-bridge run` leaves it (a heart rate strap and a blood
# pressure cuff, invented readings in the GATT layouts) is sealed and sent by
# `ohs-bridge flush`; a folder holding a FIT activity and a FIT monitoring
# file goes through `ohs-bridge files`. The server's decoded samples, RR
# intervals and workout are read back, and a second run sends nothing.
#
# Usage: test/e2e/bridge.sh   (builds bridge/ with cargo)
# Env:   OHS_E2E_PORT (default 18083), OHS_E2E_PROJECT (default ohs-bridge),
#        OHS_BRIDGE_BIN (a built ohs-bridge, skips cargo), KEEP=1 leaves the
#        stack running.
set -euo pipefail

E2E_PROJECT="${OHS_E2E_PROJECT:-ohs-bridge}"
export OHS_E2E_PORT="${OHS_E2E_PORT:-18083}"
# shellcheck source=test/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
trap e2e_down EXIT
cd "$E2E_ROOT" || exit 1

e2e_need
BRIDGE="${OHS_BRIDGE_BIN:-}"
if [ -z "$BRIDGE" ]; then
  command -v cargo >/dev/null 2>&1 || die "cargo is required (or set OHS_BRIDGE_BIN)"
  step "build ohs-bridge"
  (cd bridge && cargo build --release --locked -q) || die "cargo build"
  BRIDGE="$E2E_ROOT/bridge/target/release/ohs-bridge"
fi

step "compose up (project $E2E_PROJECT, port $OHS_E2E_PORT)"
e2e_env || die "env file"
e2e_build || die "image build"
e2e_up || die "compose up"
KEY=$(e2e_tenant_key bridge) || KEY=""
[ -n "$KEY" ] || die "no API key printed"
USER_ID=$(e2e_create_user "$KEY" bridge-e2e) || die "create user"
TOKEN=$(e2e_mint_token "$KEY" "$USER_ID") || die "mint token"

export OHS_URL="$E2E_URL" OHS_TOKEN="$TOKEN" OHS_BRIDGE_STATE="$E2E_WORK/state"
mkdir -p "$OHS_BRIDGE_STATE/spool" "$E2E_WORK/watch/GARMIN/Activity"

step "a spool left by the daemon is sealed and sent"
strap='"device":{"name":"Polar H10","manufacturer":"Polar Electro Oy","model":"H10"}'
cuff='"device":{"name":"BLEsmart","manufacturer":"OMRON HEALTHCARE Co., Ltd.","model":"HEM-7600T"}'
{
  # 72 bpm with RR 1000, 799.8 and 1000 ms; then 73 bpm.
  echo "{\"at\":\"2026-03-01T08:00:00.000+01:00\",\"char\":\"2a37\",\"value\":\"1048000433030004\",\"session\":\"e2e-strap-1\",$strap}"
  echo "{\"at\":\"2026-03-01T08:00:01.000+01:00\",\"char\":\"2a37\",\"value\":\"0049\",\"session\":\"e2e-strap-1\",$strap}"
  # 121/79 mmHg, mean 93, the cuff's clock 2026-03-01 07:30, pulse 64.
  echo "{\"at\":\"2026-03-01T08:01:00.000+01:00\",\"char\":\"2a35\",\"value\":\"0679004f005d00ea070301071e004000\",\"session\":\"e2e-cuff-1\",$cuff}"
} >"$OHS_BRIDGE_STATE/spool/0000000000000001.ndjson"
"$BRIDGE" flush >"$E2E_WORK/flush.log" 2>&1 || { cat "$E2E_WORK/flush.log"; die "ohs-bridge flush"; }
grep -q "parts: 1 sent (0 already stored), 0 refused, 0 still queued" "$E2E_WORK/flush.log" &&
  ok "one part sent" || { cat "$E2E_WORK/flush.log"; fail "flush summary"; }

step "a USB watch folder: one activity, one monitoring file"
cp connectors/activityfiles/testdata/garmin_ride.fit "$E2E_WORK/watch/GARMIN/Activity/RIDE.FIT"
cp connectors/activityfiles/testdata/monitoring.fit "$E2E_WORK/watch/GARMIN/Activity/MONITOR.fit"
"$BRIDGE" files "$E2E_WORK/watch" >"$E2E_WORK/files.log" 2>&1 || { cat "$E2E_WORK/files.log"; die "ohs-bridge files"; }
grep -q "parts: 1 sent (0 already stored), 1 refused, 0 still queued" "$E2E_WORK/files.log" &&
  ok "activity sent, monitoring file refused" || { cat "$E2E_WORK/files.log"; fail "files summary"; }
"$BRIDGE" files "$E2E_WORK/watch" >"$E2E_WORK/files2.log" 2>&1 || { cat "$E2E_WORK/files2.log"; die "ohs-bridge files again"; }
grep -q "^activity files: 0 new, 2 already sent" "$E2E_WORK/files2.log" &&
  ok "nothing sent twice" || { cat "$E2E_WORK/files2.log"; fail "second files run"; }

step "processed and read back"
e2e_wait_processed "$KEY" "$USER_ID" 120 || die "processing"
range="start=2026-03-01&end=2026-03-02&view=all"
bt_hr=$(e2e_collect "$KEY" "$USER_ID" "/samples?type=heart_rate&$range" 'select(.provider == "bluetooth") | .value' | sort -n | tr '\n' ' ')
[ "$bt_hr" = "64 72 73 " ] && ok "heart rate from the strap and the cuff: $bt_hr" || fail "bluetooth heart rate: got '$bt_hr'"
sys=$(e2e_collect "$KEY" "$USER_ID" "/samples?type=blood_pressure_systolic&$range" '"\(.value) \(.unit) \(.source) \(.start_date)"')
[[ "$sys" == "121 mmHg omron 2026-03-01T06:30:00"* ]] && ok "systolic on the cuff's clock: $sys" || fail "systolic: got '$sys'"
check "RR intervals" "$(e2e_collect "$KEY" "$USER_ID" "/rr-intervals?start=2026-03-01&end=2026-03-02" '.rr_ms' | wc -l | tr -d ' ')" 3
check "workouts" "$(e2e_count "$KEY" "$USER_ID" /workouts)" 1

e2e_summary
[ "$E2E_FAILS" -eq 0 ]
