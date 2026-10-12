#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Checks that the tree is ready to be tagged TAG (vX.Y.Z or vX.Y.Z-rc.N):
# its release notes exist, and the compose file runs that version by default
# (`${OHS_VERSION:-X.Y.Z}`), so `docker compose up` on the tagged checkout
# pulls the image the tag publishes. server.json names the same version and image
# tag, since the release publishes it to the MCP registry.
#
#   scripts/check-release-version.sh v0.0.2
set -euo pipefail

tag="${1:-}"
case "$tag" in
  v[0-9]*) ;;
  *) echo "usage: $0 vX.Y.Z" >&2; exit 2 ;;
esac
version="${tag#v}"
root="$(cd "$(dirname "$0")/.." && pwd)"

status=0
if [ ! -s "$root/docs/release-notes/$tag.md" ]; then
  echo "check-release-version: missing docs/release-notes/$tag.md" >&2
  status=1
fi
defaults="$(grep -o 'OHS_VERSION:-[^}]*' "$root/deploy/docker-compose.yml" | sed 's/^OHS_VERSION:-//' | sort -u)"
if [ "$defaults" != "$version" ]; then
  echo "check-release-version: deploy/docker-compose.yml defaults OHS_VERSION to '${defaults//$'\n'/, }', the tag is $version" >&2
  status=1
fi
server_versions="$(grep -oE '"version": *"[^"]*"|open-health-sync:[^"]*"' "$root/server.json" | grep -oE '[0-9][^"]*' | sort -u)"
if [ "$server_versions" != "$version" ]; then
  echo "check-release-version: server.json names version '${server_versions//$'\n'/, }', the tag is $version" >&2
  status=1
fi
[ "$status" -eq 0 ] && echo "check-release-version: $tag ok"
exit "$status"
