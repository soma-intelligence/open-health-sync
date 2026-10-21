#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Tests of scripts/check-release-tag.sh on a scratch repository.
set -euo pipefail

check="$(cd "$(dirname "$0")" && pwd)/check-release-tag.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/ohs-release-tag-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT

repo="$work/repo"
git init -q -b main "$repo"
g() { git -C "$repo" -c user.name=test -c user.email=test@example.com "$@"; }
g commit -q --allow-empty -m one
g tag v0.1.0
g tag -a v0.2.0-rc.1 -m "annotated"
g checkout -q -b side
g commit -q --allow-empty -m "not on main"
g tag v0.2.0
g tag v0.2
g checkout -q main
g commit -q --allow-empty -m two
g tag v0.3.0

fails=0
# expect DESCRIPTION WANT_STATUS ARGS...: runs the check in the scratch repository.
expect() {
  local what=$1 want=$2 got=0
  shift 2
  (cd "$repo" && "$check" "$@" >/dev/null 2>&1) || got=$?
  if [ "$got" = "$want" ]; then
    echo "ok   $what"
  else
    echo "FAIL $what: wanted status $want, got $got"
    fails=$((fails + 1))
  fi
}

expect "a tag on main passes" 0 v0.1.0 main
expect "the tip of main passes" 0 v0.3.0 main
expect "an annotated rc tag on main passes" 0 v0.2.0-rc.1 main
expect "a tag on a side branch fails" 1 v0.2.0 main
expect "a tag that does not exist fails" 1 v9.9.9 main
expect "a missing branch fails" 1 v0.1.0 origin/main
expect "a version without a patch number is refused" 2 v0.2 main
expect "a branch name is refused" 2 main main
expect "no tag is refused" 2

if [ "$fails" -gt 0 ]; then
  echo "$fails check-release-tag test(s) failed" >&2
  exit 1
fi
