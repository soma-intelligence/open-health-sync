#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Tests of scripts/check-sdk-tag.sh on a scratch repository.
set -euo pipefail

check="$(cd "$(dirname "$0")" && pwd)/check-sdk-tag.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/ohs-sdk-tag-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT

repo="$work/repo"
git init -q -b main "$repo"
g() { git -C "$repo" -c user.name=test -c user.email=test@example.com "$@"; }
mkdir -p "$repo/sdks/react-native" "$repo/sdks/server/typescript" "$repo/sdks/server/python" \
  "$repo/sdks/flutter" "$repo/sdks/kotlin"
printf '{\n  "name": "x",\n  "version": "0.1.0",\n  "dependencies": {\n    "y": "2.0.0"\n  }\n}\n' >"$repo/sdks/react-native/package.json"
printf '{\n  "name": "@x/server",\n  "version": "1.0.0"\n}\n' >"$repo/sdks/server/typescript/package.json"
printf '[build-system]\nrequires = ["setuptools>=61"]\n\n[project]\nname = "x"\nversion = "1.0.0"\n' >"$repo/sdks/server/python/pyproject.toml"
printf 'name: x\nversion: 0.1.0\nenvironment:\n  sdk: ">=3.4.0 <4.0.0"\n' >"$repo/sdks/flutter/pubspec.yaml"
printf 'group = "x"\nversion = "0.1.0"\n' >"$repo/sdks/kotlin/build.gradle.kts"
g add -A
g commit -q -m one
g tag sdk-react-native-v0.1.0
g tag sdk-server-typescript-v1.0.0
g tag -a sdk-server-python-v1.0.0 -m "annotated"
g tag sdk-flutter-v0.1.0
g tag sdk-kotlin-v0.1.0
g tag sdk-kotlin-v0.2.0
g tag sdk-swift-v0.1.0
g checkout -q -b side
g commit -q --allow-empty -m "not on main"
g tag sdk-flutter-v0.1.1
g checkout -q main
g commit -q --allow-empty -m two

fails=0
# expect DESCRIPTION WANT_STATUS ARGS...: runs the check in the scratch repository.
expect() {
  local what=$1 want=$2 got=0
  shift 2
  (cd "$repo" && GITHUB_OUTPUT="" "$check" "$@" >/dev/null 2>&1) || got=$?
  if [ "$got" = "$want" ]; then
    echo "ok   $what"
  else
    echo "FAIL $what: wanted status $want, got $got"
    fails=$((fails + 1))
  fi
}

expect "react-native at its package.json version passes" 0 sdk-react-native-v0.1.0 main
expect "server-typescript at its package.json version passes" 0 sdk-server-typescript-v1.0.0 main
expect "an annotated server-python tag passes" 0 sdk-server-python-v1.0.0 main
expect "flutter at its pubspec version passes" 0 sdk-flutter-v0.1.0 main
expect "kotlin at its build.gradle.kts version passes" 0 sdk-kotlin-v0.1.0 main
expect "a version the manifest does not have fails" 1 sdk-kotlin-v0.2.0 main
expect "a tag on a side branch fails" 1 sdk-flutter-v0.1.1 main
expect "a tag that does not exist fails" 1 sdk-flutter-v9.9.9 main
expect "a missing branch fails" 1 sdk-flutter-v0.1.0 origin/main
expect "an SDK with no registry is refused" 2 sdk-swift-v0.1.0 main
expect "a release tag is refused" 2 v0.1.0 main
expect "a version without a patch number is refused" 2 sdk-flutter-v0.1 main
expect "no tag is refused" 2

out="$(cd "$repo" && GITHUB_OUTPUT="" "$check" sdk-server-python-v1.0.0 main 2>/dev/null)"
if [ "$out" = "$(printf 'name=server-python\ndir=sdks/server/python\nversion=1.0.0')" ]; then
  echo "ok   prints name, dir and version"
else
  echo "FAIL prints name, dir and version: got '$out'"
  fails=$((fails + 1))
fi

if [ "$fails" -gt 0 ]; then
  echo "$fails check-sdk-tag test(s) failed" >&2
  exit 1
fi
