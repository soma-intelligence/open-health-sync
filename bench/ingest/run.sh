#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs the whole benchmark from the repository root, after setup.sh:
#   bench/ingest/run.sh
# For each dataset profile, BENCH_RUNS (default 3) runs with BENCH_CONCURRENCY
# (default 4) clients, then prints the median table.
set -euo pipefail

WORK="${BENCH_WORK:-/tmp/ohs-bench}"
RUNS="${BENCH_RUNS:-3}"
CONC="${BENCH_CONCURRENCY:-4}"
OUT="$WORK/results"
mkdir -p "$OUT"
go build -o "$WORK/ingest" ./bench/ingest

# Profiles: name, days, seconds between heart rate samples.
#   baseline: 14 days, heart rate every minute (2,040 records a day)
#   dense:    14 days, heart rate every 5 seconds (17,880 records a day)
for profile in "baseline 14 60" "dense 14 5"; do
  set -- $profile
  name=$1 days=$2 hr=$3
  "$WORK/ingest" gen -out "$WORK/data-$name" -days "$days" -hr-interval "$hr"
  for run in $(seq "$RUNS"); do
    echo "== $name run $run"
    "$WORK/ingest" run -data "$WORK/data-$name" -work "$WORK" -concurrency "$CONC" \
      -out "$OUT/$name-$run.json" >/dev/null
  done
done

"$WORK/ingest" report "$OUT"
