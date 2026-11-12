#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Checks that the image deploy/docker-compose.yml runs by default
# (`image: <registry>/<repository>:${OHS_VERSION:-X.Y.Z}`) exists in the
# registry, so the quickstart on the default branch never names an image
# nobody can pull. A release pull request moves the default to the version it
# is about to tag: between its merge and the push of the tag this check
# fails, on purpose, and says so. .github/workflows/compose-image.yml runs it
# every day on the default branch.
#
#   scripts/check-compose-image.sh [COMPOSE_FILE [TAG]]
#
# TAG overrides the default tag read from the file. Needs curl. Exit status:
# 0 the image exists, 1 it does not, 2 the check could not run (no image line,
# registry unreachable, unexpected answer). It never passes on an error.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
file="${1:-$root/deploy/docker-compose.yml}"

line="$(grep -E '^[[:space:]]*image:[[:space:]]*[^[:space:]]+:\$\{OHS_VERSION:-[^}]+\}' "$file" | head -n 1 || true)"
if [ -z "$line" ]; then
  echo "check-compose-image: no 'image: ...:\${OHS_VERSION:-X.Y.Z}' line in $file" >&2
  exit 2
fi
image="$(printf '%s' "$line" | sed -E 's/^[[:space:]]*image:[[:space:]]*//; s/:\$\{OHS_VERSION:-.*$//')"
tag="${2:-$(printf '%s' "$line" | sed -E 's/^.*\$\{OHS_VERSION:-([^}]+)\}.*$/\1/')}"
registry="${image%%/*}"
repository="${image#*/}"
case "$tag" in
  *@*) reference="${tag#*@}" ;; # tag@sha256:...: the digest decides
  *) reference="$tag" ;;
esac
if [ -z "$registry" ] || [ -z "$repository" ] || [ "$registry" = "$image" ] || [ -z "$reference" ]; then
  echo "check-compose-image: cannot read a registry, repository and tag from '$line'" >&2
  exit 2
fi

# An anonymous pull token, as `docker pull` gets one for a public image.
token_json="$(curl -fsS --retry 3 --max-time 30 \
  "https://$registry/token?service=$registry&scope=repository:$repository:pull")" || {
  echo "check-compose-image: no pull token from $registry for $repository" >&2
  exit 2
}
token="$(printf '%s' "$token_json" | sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
if [ -z "$token" ]; then
  echo "check-compose-image: the token answer of $registry holds no token" >&2
  exit 2
fi

code="$(curl -sS --retry 3 --max-time 30 -o /dev/null -w '%{http_code}' -I \
  -H "Authorization: Bearer $token" \
  -H "Accept: application/vnd.oci.image.index.v1+json" \
  -H "Accept: application/vnd.docker.distribution.manifest.list.v2+json" \
  -H "Accept: application/vnd.oci.image.manifest.v1+json" \
  -H "Accept: application/vnd.docker.distribution.manifest.v2+json" \
  "https://$registry/v2/$repository/manifests/$reference")" || {
  echo "check-compose-image: $registry did not answer for $image:$tag" >&2
  exit 2
}
case "$code" in
  200)
    echo "check-compose-image: $image:$tag exists"
    ;;
  404)
    {
      echo "check-compose-image: $image:$tag does not exist."
      echo "  ${file#"$root"/} runs it by default, so the quickstart fails at 'docker compose up'."
      echo "  If the release pull request for $tag was just merged, push the tag v${tag%%@*} now;"
      echo "  otherwise set the default back to the latest published version."
    } >&2
    exit 1
    ;;
  *)
    echo "check-compose-image: $registry answered HTTP $code for $image:$tag" >&2
    exit 2
    ;;
esac
