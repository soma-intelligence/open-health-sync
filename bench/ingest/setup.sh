#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Prepares the ingestion benchmark, from the repository root:
#   bench/ingest/setup.sh
# - a TimescaleDB Postgres 17 container (the image docker-compose.yml pins),
#   published on 127.0.0.1:$BENCH_PG_PORT (default 15432)
# - bin/ohs built from this checkout
# Needs docker, go and psql on the host.
set -euo pipefail

WORK="${BENCH_WORK:-/tmp/ohs-bench}"
PORT="${BENCH_PG_PORT:-15432}"
CONTAINER="${BENCH_PG_CONTAINER:-ohs-bench-pg}"
IMAGE="timescale/timescaledb:latest-pg17@sha256:b56a3f51991df1f79d0c1a0841cad5c5fa8058fdd6a3ee6886394a6d4c174538"

for tool in docker go psql; do
  command -v "$tool" >/dev/null || { echo "setup: $tool is required" >&2; exit 1; }
done
mkdir -p "$WORK"

if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  docker run -d --name "$CONTAINER" -p "127.0.0.1:$PORT:5432" \
    -e POSTGRES_HOST_AUTH_METHOD=trust "$IMAGE" >/dev/null
fi
for _ in $(seq 60); do
  psql -h 127.0.0.1 -p "$PORT" -U postgres -tAc 'select 1' >/dev/null 2>&1 && break
  sleep 1
done
# A superuser, as the compose file makes it.
psql -h 127.0.0.1 -p "$PORT" -U postgres -q -c \
  "DO \$\$BEGIN CREATE ROLE ohs LOGIN SUPERUSER PASSWORD 'ohs'; EXCEPTION WHEN duplicate_object THEN NULL; END\$\$"

make build

echo "setup: ready. Postgres on 127.0.0.1:$PORT, $(bin/ohs version)"
