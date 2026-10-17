#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Example apps end to end: one app runs on an emulator or simulator, uploads
# heart rate data through its own screens with an upload-scope user token, and
# the samples are read back with the tenant key.
#
# Usage: test/e2e/apps.sh android|react-native|ios
#   android       examples/android's instrumented test, on the running emulator (adb)
#   react-native  examples/react-native as a debug build with Metro, driven by
#                 Maestro (e2e/upload.yaml) on the running emulator
#   ios           examples/ios's UI test on an iOS simulator (macOS, Xcode,
#                 XcodeGen); OHS_IOS_DESTINATION picks it (default: the first
#                 available iPhone)
# Server: with OHS_URL and OHS_KEY set, the app uses that server, which must
# listen on this machine; otherwise the compose stack is built from this
# checkout (as sdks.sh does).
# Env:   OHS_E2E_PORT (default 18083), OHS_E2E_PROJECT (default ohs-apps),
#        KEEP=1 leaves the stack running, OHS_E2E_ARTIFACTS names a folder
#        that keeps the iOS result bundle, the Maestro output and Metro's log.
set -euo pipefail

platform="${1:-}"
case "$platform" in
  android | react-native | ios) ;;
  *) echo "usage: $0 android|react-native|ios" >&2; exit 2 ;;
esac

E2E_PROJECT="${OHS_E2E_PROJECT:-ohs-apps-$platform}"
export OHS_E2E_PORT="${OHS_E2E_PORT:-18083}"
# shellcheck source=test/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
cd "$E2E_ROOT" || exit 1
ARTIFACTS="${OHS_E2E_ARTIFACTS:-$E2E_WORK}"
mkdir -p "$ARTIFACTS"
ARTIFACTS="$(cd "$ARTIFACTS" && pwd)"

METRO_PID=""
STACK=""
cleanup() {
  [ -z "$METRO_PID" ] || kill "$METRO_PID" 2>/dev/null || true
  if [ -n "$STACK" ]; then e2e_down; else rm -rf "$E2E_WORK"; fi
}
trap cleanup EXIT

if [ -n "${OHS_URL:-}" ] && [ -n "${OHS_KEY:-}" ]; then
  E2E_URL="${OHS_URL%/}"
  KEY="$OHS_KEY"
  step "server $E2E_URL (given)"
else
  STACK=1
  e2e_need
  step "compose up (project $E2E_PROJECT, port $OHS_E2E_PORT)"
  e2e_env || die "env file"
  e2e_build || die "image build"
  e2e_up || die "compose up"
  KEY=$(e2e_tenant_key "apps-$platform") || KEY=""
  [ -n "$KEY" ] || die "no API key printed"
fi
port="${E2E_URL##*:}"
[[ "$port" =~ ^[0-9]+$ ]] || die "the server URL $E2E_URL has no port"

step "user and upload token"
# A unique reference, so runs against the same given server never collide.
USER_ID=$(e2e_create_user "$KEY" "apps-$platform-$(date +%s)-$$") || die "create user"
TOKEN=$(e2e_mint_token "$KEY" "$USER_ID") || die "mint token"
ok "user $USER_ID"

# verify_heart_rate EXPECTED SOURCE_FIELD SOURCE: the user holds exactly the
# heart rates in EXPECTED (space separated, any order), each from SOURCE.
verify_heart_rate() {
  e2e_wait_processed "$KEY" "$USER_ID" 120 || { fail "processing"; return; }
  local got want
  got=$(e2e_collect "$KEY" "$USER_ID" "/samples?type=heart_rate" "[.value, .$2] | @tsv") || { fail "read samples"; return; }
  want=$(for v in $1; do printf '%s\t%s\n' "$v" "$3"; done)
  if [ "$(sort <<<"$got" | awk -F'\t' '{printf "%g\t%s\n", $1, $2}')" = "$(sort <<<"$want" | awk -F'\t' '{printf "%g\t%s\n", $1, $2}')" ]; then
    ok "heart_rate samples read back: $(tr '\t\n' '/ ' <<<"$got")"
  else
    fail "heart_rate samples: got [$(tr '\t\n' '/ ' <<<"$got")], want [$(tr '\t\n' '/ ' <<<"$want")]"
  fi
}

run_android() {
  command -v adb >/dev/null 2>&1 || die "adb is required, with an emulator running"
  step "examples/android instrumented test"
  # 10.0.2.2 is the host's loopback seen from the emulator.
  (cd examples/android && ./gradlew --no-daemon -q connectedDebugAndroidTest \
    -Pandroid.testInstrumentationRunnerArguments.ohsServerUrl="http://10.0.2.2:$port" \
    -Pandroid.testInstrumentationRunnerArguments.ohsToken="$TOKEN" \
    -Pandroid.testInstrumentationRunnerArguments.ohsBpm=61) || {
    # Gradle only points at the report; show the failure itself.
    find examples/android/app/build/outputs/androidTest-results -name '*.xml' -exec grep -h -A8 '<failure' {} + 2>/dev/null | head -n 40
    die "instrumented test"; }
  ok "upload through the app"
  step "read back with the tenant key"
  verify_heart_rate "61" source_name "Android example"
}

run_react_native() {
  local t
  for t in adb maestro npm; do command -v "$t" >/dev/null 2>&1 || die "$t is required"; done
  local app=examples/react-native apk=examples/react-native/android/app/build/outputs/apk/debug/app-debug.apk
  step "examples/react-native debug build"
  (cd sdks/react-native && npm ci --no-audit --no-fund >/dev/null) || die "SDK dependencies"
  (cd "$app" && npm install --no-audit --no-fund >/dev/null &&
    CI=1 npx expo prebuild --platform android --no-install --clean >/dev/null &&
    cd android && ./gradlew --no-daemon -q assembleDebug) || die "debug build"
  adb install -r "$apk" >/dev/null || die "install"
  ok "installed"

  step "Metro"
  (cd "$app" && CI=1 exec npx expo start --port 8081 >"$ARTIFACTS/metro.log" 2>&1) &
  METRO_PID=$!
  local deadline=$(($(date +%s) + 120))
  until curl -fsS http://127.0.0.1:8081/status 2>/dev/null | grep -q running; do
    [ "$(date +%s)" -lt "$deadline" ] || { tail -n 30 "$ARTIFACTS/metro.log"; die "Metro did not start"; }
    sleep 1
  done
  adb reverse tcp:8081 tcp:8081 >/dev/null || die "adb reverse"
  ok "Metro on 8081"

  step "Maestro flow e2e/upload.yaml"
  maestro test -e OHS_URL="http://10.0.2.2:$port" -e OHS_TOKEN="$TOKEN" -e OHS_BPM=62 \
    --debug-output "$ARTIFACTS/maestro" "$app/e2e/upload.yaml" || {
    tail -n 30 "$ARTIFACTS/metro.log"; die "Maestro flow"; }
  ok "upload, then the same part again (duplicate)"
  step "read back with the tenant key"
  verify_heart_rate "62" source_name "OHS React Native example"
}

run_ios() {
  local t
  for t in xcodebuild xcrun xcodegen; do command -v "$t" >/dev/null 2>&1 || die "$t is required"; done
  local destination="${OHS_IOS_DESTINATION:-}"
  if [ -z "$destination" ]; then
    local id
    id=$(xcrun simctl list devices available -j |
      jq -r '[.devices | to_entries[] | select(.key | test("iOS")) | .value[] | select(.name | startswith("iPhone"))][0].udid // empty')
    [ -n "$id" ] || die "no iPhone simulator available"
    destination="id=$id"
  fi
  rm -rf "$ARTIFACTS/ios.xcresult"
  step "examples/ios UI test ($destination)"
  (cd examples/ios && xcodegen generate --quiet &&
    TEST_RUNNER_OHS_URL="http://localhost:$port" TEST_RUNNER_OHS_TOKEN="$TOKEN" TEST_RUNNER_OHS_BPM="63,64,65" \
      xcodebuild test -project OHSExample.xcodeproj -scheme OHSExampleApp -destination "$destination" \
      -resultBundlePath "$ARTIFACTS/ios.xcresult" -quiet) || {
    # -quiet hides the assertion messages; read them from the result bundle.
    xcrun xcresulttool get test-results summary --path "$ARTIFACTS/ios.xcresult" 2>/dev/null |
      jq -r '.testFailures[]? | "\(.testName): \(.failureText)"' | head -c 6000
    die "UI test"; }
  ok "settings, Health access and two syncs through the app"
  step "read back with the tenant key"
  verify_heart_rate "63 64 65" source_bundle_id "org.openhealthsync.example"
}

case "$platform" in
  android) run_android ;;
  react-native) run_react_native ;;
  ios) run_ios ;;
esac
e2e_summary
