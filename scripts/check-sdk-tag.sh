#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Checks that TAG may publish an SDK to its package registry: it is
# sdk-<name>-vX.Y.Z for a known SDK, X.Y.Z is the version in that SDK's
# manifest at the tagged commit, and the commit is on BRANCH (default
# origin/main), so a tag on a side branch or a stale manifest publishes
# nothing. The sdk-publish workflow runs it before building anything.
#
# On success it prints name=, dir= and version= lines, and appends them to
# $GITHUB_OUTPUT when that is set.
#
#   scripts/check-sdk-tag.sh sdk-server-python-v0.1.0 [origin/main]
set -euo pipefail

tag="${1:-}"
branch="${2:-origin/main}"
if ! [[ "$tag" =~ ^sdk-([a-z-]+)-v([0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
  echo "check-sdk-tag: '$tag' is not sdk-<name>-vX.Y.Z" >&2
  exit 2
fi
name="${BASH_REMATCH[1]}"
want="${BASH_REMATCH[2]}"

case "$name" in
  react-native)      dir=sdks/react-native;        manifest=package.json ;;
  server-typescript) dir=sdks/server/typescript;   manifest=package.json ;;
  server-python)     dir=sdks/server/python;       manifest=pyproject.toml ;;
  flutter)           dir=sdks/flutter;             manifest=pubspec.yaml ;;
  kotlin)            dir=sdks/kotlin;              manifest=build.gradle.kts ;;
  *)
    echo "check-sdk-tag: no SDK named '$name' (react-native, server-typescript, server-python, flutter, kotlin)" >&2
    exit 2
    ;;
esac

if ! commit="$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}")"; then
  echo "check-sdk-tag: no tag $tag in this checkout" >&2
  exit 1
fi
if ! git rev-parse --verify --quiet "$branch^{commit}" >/dev/null; then
  echo "check-sdk-tag: no $branch in this checkout (fetch it first)" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "$commit" "$branch"; then
  echo "check-sdk-tag: $tag is on $commit, which is not on $branch" >&2
  exit 1
fi

if ! text="$(git show "$commit:$dir/$manifest" 2>/dev/null)"; then
  echo "check-sdk-tag: no $dir/$manifest at $commit" >&2
  exit 1
fi
case "$manifest" in
  package.json)
    got="$(printf '%s' "$text" | sed -nE 's/^  "version": "([^"]+)",?$/\1/p' | head -n 1)" ;;
  pubspec.yaml)
    got="$(printf '%s' "$text" | sed -nE 's/^version: *([^ #]+).*$/\1/p' | head -n 1)" ;;
  pyproject.toml | build.gradle.kts)
    got="$(printf '%s' "$text" | sed -nE 's/^version = "([^"]+)"$/\1/p' | head -n 1)" ;;
esac
if [ "$got" != "$want" ]; then
  echo "check-sdk-tag: $tag says $want but $dir/$manifest says '${got}'" >&2
  exit 1
fi

out="name=$name
dir=$dir
version=$want"
echo "$out"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "$out" >>"$GITHUB_OUTPUT"
fi
echo "check-sdk-tag: $tag ok, $commit is on $branch and $dir/$manifest is $want" >&2
