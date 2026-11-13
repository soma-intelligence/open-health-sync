#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Builds the ohs image from this checkout on a Docker without buildx.
#
#   scripts/image-without-buildx.sh [TAG]     (default TAG: ohs:local)
#
# deploy/Dockerfile cross-compiles with `FROM --platform=$BUILDPLATFORM`, which
# only BuildKit understands: the classic builder fails with
#   failed to parse platform : "" is an invalid OS component of ""
# This script cross-compiles the binary on the host with the build stage's own
# flags (checked against the Dockerfile first), then builds the Dockerfile's
# final stage around it, copied verbatim except that its two COPY --from lines
# read the host build. It needs Go 1.26 and docker; deploy/Dockerfile is never
# changed. The end to end suites (test/e2e/lib.sh) use the same functions.

# OHS_BUILD_RUN is the go build RUN of deploy/Dockerfile (one line, the
# continuation lines joined) that ohs_image_without_buildx reproduces.
# ohs_build_flags_match refuses to build when the Dockerfile drifts.
# shellcheck disable=SC2016 # the variables are the Dockerfile's, kept literal
OHS_BUILD_RUN='RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags azurekv,awskms -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT" -o /out/ohs ./cmd/ohs'

# ohs_build_flags_match DOCKERFILE: true when its go build RUN is OHS_BUILD_RUN.
ohs_build_flags_match() {
  local run
  run=$(awk '/^RUN .*go build|^RUN CGO_ENABLED/{on=1} on{line = line $0; if ($0 !~ /\\$/) {print line; exit}}' "$1" |
    sed -e 's/\\//g' -e 's/[[:space:]][[:space:]]*/ /g' -e 's/ $//')
  if [ "$run" != "$OHS_BUILD_RUN" ]; then
    echo "the go build of $1 changed; update OHS_BUILD_RUN and the host build in scripts/image-without-buildx.sh:" >&2
    echo "  have: $run" >&2
    echo "  want: $OHS_BUILD_RUN" >&2
    return 1
  fi
}

# ohs_image_without_buildx ROOT TAG WORKDIR VERSION: builds image TAG from the
# checkout at ROOT, using WORKDIR as scratch space. Output goes to stdout and
# stderr; the caller redirects it.
ohs_image_without_buildx() {
  local root=$1 tag=$2 work=$3 version=$4 ctx arch commit
  ohs_build_flags_match "$root/deploy/Dockerfile" || return 1
  ctx="$work/image"
  arch=$(docker version --format '{{.Server.Arch}}') || return 1
  commit=$(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo none)
  mkdir -p "$ctx/raw"
  # Same flags as OHS_BUILD_RUN, with TARGETOS=linux and TARGETARCH=$arch.
  (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -tags azurekv,awskms \
    -ldflags "-s -w -X main.version=$version -X main.commit=$commit" \
    -o "$ctx/ohs" ./cmd/ohs) || return 1
  awk '/^FROM /{buf=""} {buf = buf $0 "\n"} END {printf "%s", buf}' "$root/deploy/Dockerfile" |
    sed -e 's|^COPY --from=build /out/ohs /ohs$|COPY ohs /ohs|' \
        -e 's|^COPY --from=build --chown=\([0-9:]*\) /out/raw |COPY --chown=\1 raw |' >"$ctx/Dockerfile"
  if grep -q -- '--from=' "$ctx/Dockerfile"; then
    echo "deploy/Dockerfile final stage changed: update scripts/image-without-buildx.sh" >&2
    return 1
  fi
  DOCKER_BUILDKIT=0 docker build -q -t "$tag" "$ctx"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  set -euo pipefail
  root="$(cd "$(dirname "$0")/.." && pwd)"
  tag="${1:-ohs:local}"
  work="$(mktemp -d "${TMPDIR:-/tmp}/ohs-image.XXXXXX")"
  trap 'rm -rf "$work"' EXIT
  version=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)
  ohs_image_without_buildx "$root" "$tag" "$work" "$version"
  echo "built $tag"
fi
