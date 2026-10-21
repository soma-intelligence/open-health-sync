#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Checks that TAG may start a release: it names a version (vX.Y.Z or
# vX.Y.Z-rc.N) and its commit is on BRANCH (default origin/main), so a tag
# pushed on a side branch or an unreviewed commit builds and signs nothing.
# The release workflow runs it before any job that publishes.
#
#   scripts/check-release-tag.sh v0.2.0 [origin/main]
set -euo pipefail

tag="${1:-}"
branch="${2:-origin/main}"
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; then
  echo "check-release-tag: '$tag' is not vX.Y.Z or vX.Y.Z-rc.N" >&2
  exit 2
fi
if ! commit="$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}")"; then
  echo "check-release-tag: no tag $tag in this checkout" >&2
  exit 1
fi
if ! git rev-parse --verify --quiet "$branch^{commit}" >/dev/null; then
  echo "check-release-tag: no $branch in this checkout (fetch it first)" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "$commit" "$branch"; then
  echo "check-release-tag: $tag is on $commit, which is not on $branch" >&2
  exit 1
fi
echo "check-release-tag: $tag ok, $commit is on $branch"
