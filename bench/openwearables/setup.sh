#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Prepares the benchmark against Open Wearables, from the repository root:
#   bench/openwearables/setup.sh
# - a TimescaleDB Postgres 17 container (the image docker-compose.yml pins),
#   published on 127.0.0.1:$BENCH_PG_PORT (default 15432), shared by both systems
# - bin/ohs built from this checkout
# - Open Wearables cloned at $OW_REF into $BENCH_WORK/open-wearables, with its
#   locked Python environment (uv sync --frozen)
# Needs docker, go, git, uv, psql and redis-server on the host.
set -euo pipefail

WORK="${BENCH_WORK:-/tmp/ohs-bench}"
PORT="${BENCH_PG_PORT:-15432}"
CONTAINER="${BENCH_PG_CONTAINER:-ohs-bench-pg}"
OW_REPO="${OW_REPO:-https://github.com/the-momentum/open-wearables.git}"
OW_REF="${OW_REF:-d07f0d534051998d7a8a567795f5d6ecc8e685a8}"
IMAGE="timescale/timescaledb:latest-pg17@sha256:b346edcdb51a1fd6020e3965e0bd1c9f3406fa6d5fbce1e28f4852587ef934e2"

for tool in docker go git uv psql redis-server redis-cli; do
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
# Both roles are superusers, as each project's own compose file makes them.
for role in ohs open-wearables; do
  psql -h 127.0.0.1 -p "$PORT" -U postgres -q -c \
    "DO \$\$BEGIN CREATE ROLE \"$role\" LOGIN SUPERUSER PASSWORD '$role'; EXCEPTION WHEN duplicate_object THEN NULL; END\$\$"
done

make build

if [ ! -d "$WORK/open-wearables/.git" ]; then
  git clone --quiet "$OW_REPO" "$WORK/open-wearables"
fi
git -C "$WORK/open-wearables" fetch --quiet origin "$OW_REF" 2>/dev/null || true
git -C "$WORK/open-wearables" checkout --quiet "$OW_REF"
# bench.py passes every setting it needs as environment variables; the file
# only has to exist for pydantic-settings.
cp "$WORK/open-wearables/backend/config/.env.example" "$WORK/open-wearables/backend/config/.env"
(cd "$WORK/open-wearables/backend" && uv sync --frozen --quiet)

echo "setup: ready. Postgres on 127.0.0.1:$PORT, Open Wearables at $(git -C "$WORK/open-wearables" rev-parse --short HEAD)"
