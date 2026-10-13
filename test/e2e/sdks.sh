#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Client SDKs end to end: each SDK's quickstart example against the real
# compose stack. Every example creates a user with the tenant key, mints a
# user token, uploads one heart rate reading with its part builder, waits for
# processing through the coverage route and reads exactly one sample back.
#
# Usage: test/e2e/sdks.sh [kotlin] [swift] [react-native] [flutter]
#        (no argument: every SDK whose toolchain is installed)
# Env:   OHS_E2E_PORT (default 18082), OHS_E2E_PROJECT (default ohs-sdks),
#        KEEP=1 leaves the stack running.
set -euo pipefail

E2E_PROJECT="${OHS_E2E_PROJECT:-ohs-sdks}"
export OHS_E2E_PORT="${OHS_E2E_PORT:-18082}"
# shellcheck source=test/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
trap e2e_down EXIT
cd "$E2E_ROOT" || exit 1

declare -A tool=([kotlin]=java [swift]=swift [react-native]=npm [flutter]=dart)
sdks=("$@")
if [ ${#sdks[@]} -eq 0 ]; then
  for s in kotlin swift react-native flutter; do
    if command -v "${tool[$s]}" >/dev/null 2>&1; then sdks+=("$s"); else say "skip $s (${tool[$s]} not installed)"; fi
  done
fi
[ ${#sdks[@]} -gt 0 ] || die "no SDK toolchain installed"

e2e_need
step "compose up (project $E2E_PROJECT, port $OHS_E2E_PORT)"
e2e_env || die "env file"
e2e_build || die "image build"
e2e_up || die "compose up"
KEY=$(e2e_tenant_key sdks) || KEY=""
[ -n "$KEY" ] || die "no API key printed"
export OHS_URL="$E2E_URL" OHS_KEY="$KEY"

for s in "${sdks[@]}"; do
  step "$s quickstart example"
  t=$(date +%s)
  case "$s" in
    kotlin) (cd sdks/kotlin && ./gradlew --no-daemon -q runExample) ;;
    swift) (cd sdks/swift && swift run -c release OHSExample) ;;
    react-native) (cd sdks/react-native && npm ci --no-audit --no-fund >/dev/null && npm run -s example) ;;
    flutter) (cd sdks/flutter && dart pub get >/dev/null && dart run example/quickstart.dart) ;;
    *) die "unknown SDK $s" ;;
  esac && ok "$s in $(($(date +%s) - t)) s" || fail "$s example"
done

[ "$E2E_FAILS" -eq 0 ] || exit 1
say "every SDK example passed"
