#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Crash recovery: SIGKILL the server while the ETL is processing, restart it,
# and check that it ends with exactly what a run without a crash ends with.
#
#   1. user A uploads the generated volume and it is processed undisturbed
#      (the clean run);
#   2. user B uploads the same volume; the ohs container gets
#      `docker kill -s KILL` once some of B's rows have landed and some jobs
#      are still unfinished (0 < rows < total). If no row has landed
#      OHS_REL_KILL_WAIT seconds (default 20) after a job started running,
#      the kill happens anyway and the run says it only exercises job reclaim;
#   3. the server is started again and B's jobs are polled to the end. A job
#      that was running at the kill is reclaimed after the queue's visibility
#      timeout (10 minutes), so this step takes that long;
#   4. B's per-type counts, sums and derived daily step counts equal A's and
#      the values test/e2e/expect.go computes; no record is duplicated; no
#      ETL job is left running, pending or parked; no failed record.
#
# Usage: test/e2e/reliability.sh          (tears the stack down on exit)
#        KEEP=1 test/e2e/reliability.sh   (leaves it running)
# Env:   OHS_E2E_PROJECT (compose project, default ohs-rel), OHS_E2E_PORT (default 18081), OHS_REL_PARTS (default 6),
#        OHS_REL_LINES (lines per part, default 40000), OHS_REL_TIMEOUT
#        (seconds to wait for recovery, default 900), OHS_REL_KILL_WAIT
set -euo pipefail

E2E_PROJECT="${OHS_E2E_PROJECT:-ohs-rel}"
export OHS_E2E_PORT="${OHS_E2E_PORT:-18081}"
# shellcheck source=test/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
trap e2e_down EXIT
cd "$E2E_ROOT" || exit 1

PARTS_N="${OHS_REL_PARTS:-6}"
LINES="${OHS_REL_LINES:-40000}"
TIMEOUT="${OHS_REL_TIMEOUT:-900}"
KILL_WAIT="${OHS_REL_KILL_WAIT:-20}"
DAY=2026-02-01
VOLUME="$E2E_WORK/volume"

e2e_need
step "volume: $PARTS_N parts of $LINES lines"
go run ./test/e2e/expect.go gen "$VOLUME" "$PARTS_N" "$LINES" || die "generate"
EXPECT=$(go run ./test/e2e/expect.go "$VOLUME") || die "expect.go"
eval "$EXPECT"
say "$(tr '\n' ' ' <<<"$EXPECT")"

step "stack (project $E2E_PROJECT, port $OHS_E2E_PORT)"
e2e_env || die "env file"
e2e_build || die "image build"
e2e_up || die "compose up"
KEY=$(e2e_tenant_key reliability) || KEY=""
[ -n "$KEY" ] || die "no API key printed"
ok "stack up, tenant created"

# counts USER: one line per quantity type with count and sum, then the
# derived daily step counts.
counts() {
  psql_q "SELECT quantity_type || ' ' || count(*) || ' ' || round(sum(value)::numeric, 3)
          FROM ohs_data.quantity_samples WHERE user_id = '$1' GROUP BY quantity_type ORDER BY quantity_type"
  psql_q "SELECT 'daily ' || summary_date || ' ' || coalesce(step_count, -1)
          FROM ohs_data.daily_summaries WHERE user_id = '$1' ORDER BY summary_date"
}
# progress USER: "<rows landed> <jobs running> <jobs unfinished> <jobs done>".
progress() {
  psql_q "SELECT (SELECT count(*) FROM ohs_data.quantity_samples WHERE user_id = '$1')
                 || ' ' || count(*) FILTER (WHERE status = 'running')
                 || ' ' || count(*) FILTER (WHERE status IN ('pending', 'running'))
                 || ' ' || count(*) FILTER (WHERE status = 'done')
          FROM ohs_sync.etl_jobs WHERE user_id = '$1'"
}

step "1. clean run (user A)"
A=$(e2e_create_user "$KEY" clean) || die "create user A"
TA=$(e2e_mint_token "$KEY" "$A") || die "token A"
t=$(date +%s)
e2e_upload_dir "$TA" "$VOLUME" "$DAY" rel || die "upload A"
e2e_wait_processed "$KEY" "$A" "$TIMEOUT" || die "clean run did not finish"
CLEAN_S=$(($(date +%s) - t))
ok "clean run: upload and processing in $CLEAN_S s"
CLEAN=$(counts "$A")
printf '%s\n' "$CLEAN" | sed 's/^/    /'

step "2. crash run (user B): kill -9 while processing"
B=$(e2e_create_user "$KEY" crash) || die "create user B"
TB=$(e2e_mint_token "$KEY" "$B") || die "token B"
e2e_upload_dir "$TB" "$VOLUME" "$DAY" rel || die "upload B"
TOTAL=$((HR_COUNT + STEP_COUNT))
deadline=$(($(date +%s) + 120))
started=""
MODE=""
while [ -z "$MODE" ]; do
  read -r rows running left done_ <<<"$(progress "$B")"
  case $(kill_decision "$rows" "$TOTAL" "$left") in
    partial) MODE=partial ;;
    late) die "every job of B finished before the kill (rows $rows): raise OHS_REL_LINES" ;;
    *)
      if [ -z "$started" ] && [ "$running" -gt 0 ]; then started=$(date +%s); fi
      if [ -n "$started" ] && [ $(($(date +%s) - started)) -ge "$KILL_WAIT" ]; then MODE=reclaim; fi
      [ "$(date +%s)" -lt "$deadline" ] || die "no job of B ever ran"
      sleep 0.1
      ;;
  esac
done
CID=$(e2e_compose ps -q ohs)
docker kill -s KILL "$CID" >/dev/null || die "docker kill"
T_KILL=$(date +%s)
read -r AT_KILL_ROWS AT_KILL_RUNNING AT_KILL_LEFT AT_KILL_DONE <<<"$(progress "$B")"
say "killed: rows $AT_KILL_ROWS of $TOTAL, jobs running $AT_KILL_RUNNING unfinished $AT_KILL_LEFT done $AT_KILL_DONE"
if [ "$AT_KILL_LEFT" -gt 0 ]; then
  ok "the kill landed mid-processing"
else
  fail "every job had finished before the kill: raise OHS_REL_LINES"
fi
if [ "$MODE" = partial ]; then
  ok "rows had started landing ($AT_KILL_ROWS of $TOTAL): partial data and job reclaim are both exercised"
else
  say "note: no row had landed ${KILL_WAIT} s after a job started; this run exercises job reclaim only"
fi

step "3. restart and recover (timeout $TIMEOUT s)"
e2e_up || die "restart"
ok "server healthy again $(($(date +%s) - T_KILL)) s after the kill"
e2e_wait_processed "$KEY" "$B" "$TIMEOUT" || die "recovery did not finish"
RECOVER_S=$(($(date +%s) - T_KILL))
ok "recovered: every job of B finished $RECOVER_S s after the kill"

step "4. compare"
CRASH=$(counts "$B")
if [ "$CRASH" = "$CLEAN" ]; then
  ok "crash run equals clean run ($(wc -l <<<"$CLEAN" | tr -d ' ') lines of counts, sums and daily steps)"
else
  fail "crash run differs from clean run"; diff <(echo "$CLEAN") <(echo "$CRASH") | sed 's/^/    /' || true
fi
check "heart rate rows (B)" "$(psql_q "SELECT count(*) FROM ohs_data.quantity_samples WHERE user_id = '$B' AND quantity_type = 'heart_rate'")" "$HR_COUNT"
check "step rows (B)" "$(psql_q "SELECT count(*) FROM ohs_data.quantity_samples WHERE user_id = '$B' AND quantity_type = 'step_count'")" "$STEP_COUNT"
check "step sum (B)" "$(psql_q "SELECT round(sum(value)::numeric, 3) FROM ohs_data.quantity_samples WHERE user_id = '$B' AND quantity_type = 'step_count'")" "$STEP_SUM"
check "duplicate content hashes (B)" "$(psql_q "SELECT count(*) - count(DISTINCT content_hash) FROM ohs_data.quantity_samples WHERE user_id = '$B'")" 0
check "etl jobs running" "$(psql_q "SELECT count(*) FROM ohs_sync.etl_jobs WHERE status = 'running'")" 0
check "etl jobs pending" "$(psql_q "SELECT count(*) FROM ohs_sync.etl_jobs WHERE status = 'pending'")" 0
check "etl jobs parked" "$(psql_q "SELECT count(*) FROM ohs_sync.etl_jobs WHERE status = 'parked'")" 0
check "failed records" "$(psql_q "SELECT count(*) FROM ohs_data.failed_records")" 0
say "jobs of B reclaimed after the crash: $(psql_q "SELECT count(*) FROM ohs_sync.etl_jobs WHERE user_id = '$B' AND attempts > 0" || echo '?')"

e2e_summary
