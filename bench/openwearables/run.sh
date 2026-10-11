#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs the whole benchmark from the repository root, after setup.sh:
#   bench/openwearables/run.sh
# For each dataset profile and each repetition, runs both systems one after the
# other (never at the same time), then prints the median table.
# BENCH_RUNS (default 3) repetitions, BENCH_CONCURRENCY (default 4) clients.
set -euo pipefail

WORK="${BENCH_WORK:-/tmp/ohs-bench}"
RUNS="${BENCH_RUNS:-3}"
CONC="${BENCH_CONCURRENCY:-4}"
HERE="bench/openwearables"
OUT="$WORK/results"
mkdir -p "$OUT"

# Profiles: name, days, seconds between heart rate samples.
#   baseline: 14 days, heart rate every minute (2,040 records a day)
#   dense:    14 days, heart rate every 5 seconds (17,880 records a day)
for profile in "baseline 14 60" "dense 14 5"; do
  set -- $profile
  name=$1 days=$2 hr=$3
  python3 "$HERE/gen.py" --out "$WORK/data-$name" --days "$days" --hr-interval "$hr"
  for run in $(seq "$RUNS"); do
    for system in ohs ow; do
      echo "== $name run $run: $system"
      python3 "$HERE/bench.py" --system "$system" --data "$WORK/data-$name" --work "$WORK" \
        --ow-dir "$WORK/open-wearables" --concurrency "$CONC" \
        --out "$OUT/$name-$system-$run.json" >/dev/null
    done
  done
done

python3 "$HERE/report.py" "$OUT"
