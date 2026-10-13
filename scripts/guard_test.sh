#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Tests of scripts/guard.sh on a scratch repository, with a made-up term
# list (never the private one).
set -euo pipefail

guard="$(cd "$(dirname "$0")" && pwd)/guard.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/ohs-guard-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT

fails=0
# expect DESCRIPTION WANT_STATUS TERMS CONTENT: runs the guard on a repository
# whose only file holds CONTENT, with TERMS as the list.
expect() {
  local what=$1 want=$2 terms=$3 content=$4 repo got=0
  repo="$work/repo$RANDOM"
  git init -q "$repo"
  printf '%s\n' "$content" >"$repo/file.txt"
  git -C "$repo" add file.txt
  (cd "$repo" && env -u GITHUB_ACTIONS OHS_FORBIDDEN_TERMS="$terms" "$guard" >/dev/null 2>&1) || got=$?
  if [ "$got" = "$want" ]; then
    echo "ok   $what"
  else
    echo "FAIL $what: wanted status $want, got $got"
    fails=$((fails + 1))
  fi
}

# The owner name is assembled at run time so this file holds no term of a
# real list. T is the made-up term; R is the allowed repository name.
T="s""oma"
R="${T}-intelligence/open-health-sync"

expect "clean file passes" 0 "$T" 'nothing to see'
expect "a term is flagged" 1 "$T" "$T inside"
expect "image name is allowed" 0 "$T" "docker pull ghcr.io/$R:0.0.1"
expect "repository URL is allowed" 0 "$T" "https://github.com/$R/releases"
expect "Go import path is allowed" 0 "$T" "\"github.com/$R/internal/db\""
expect "clone URL with .git is allowed" 0 "$T" "git clone https://github.com/$R.git"
expect "MCP registry name is allowed" 0 "$T" "\"name\": \"io.github.$R\""
expect "a longer repository name is flagged" 1 "$T" "$R-internal"
expect "a prefixed owner is flagged" 1 "$T" "evil-$R"
expect "a dotted prefix is flagged" 1 "$T" "x.$R"
expect "an invalid term fails closed" 2 "($T" 'nothing to see'
expect "an invalid term fails closed even with a match" 2 "($T"$'\n'"$T" "$T inside"

# In GitHub Actions the output names the file and line only: CI logs are
# public, and neither the matched text nor the term may appear in them.
repo="$work/ci"
git init -q "$repo"
printf 'line with %s inside\n' "$T" >"$repo/file.txt"
git -C "$repo" add file.txt
out="$(cd "$repo" && GITHUB_ACTIONS=true OHS_FORBIDDEN_TERMS="$T" "$guard" 2>&1)" || true
if printf '%s' "$out" | grep -qi "$T"; then
  echo "FAIL CI output hides the term and the matched text"
  fails=$((fails + 1))
elif ! printf '%s' "$out" | grep -q '^file.txt:1$'; then
  echo "FAIL CI output names the file and line"
  fails=$((fails + 1))
else
  echo "ok   CI output names the place, not the term"
fi

if [ "$fails" -gt 0 ]; then
  echo "guard_test: $fails failure(s)"
  exit 1
fi
echo "guard_test: all passed"
