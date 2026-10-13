# syntax=docker/dockerfile:1
# SPDX-License-Identifier: AGPL-3.0-only
#
# Needs BuildKit (the docker buildx plugin). From the repository root:
#   docker buildx build --load -f deploy/Dockerfile -t ohs:local .
# The classic builder fails on `FROM --platform=$BUILDPLATFORM` with
# `failed to parse platform : "" is an invalid OS component`; without buildx
# run scripts/image-without-buildx.sh instead.
# The build stage always runs on the build machine and cross-compiles with Go,
# so arm64 images need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm@sha256:a688600ca24f8a4d3ca77f95b0dd40704a9fc787c826660eb7ba0b641b8b175d AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT" \
    -o /out/ohs ./cmd/ohs
RUN mkdir -p /out/raw

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
LABEL io.modelcontextprotocol.server.name="io.github.soma-intelligence/open-health-sync" \
      org.opencontainers.image.source="https://github.com/soma-intelligence/open-health-sync" \
      org.opencontainers.image.description="Self-hosted health data aggregator: thirteen sources in one canonical model, REST API, read-only MCP server, FHIR R4 and Open mHealth." \
      org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=build /out/ohs /ohs
COPY --from=build --chown=65532:65532 /out/raw /var/lib/ohs/raw
VOLUME /var/lib/ohs/raw
EXPOSE 8080
# The distroless nonroot user. Hosting platforms that mount their volumes
# owned by root (Fly.io, Render) build with --build-arg IMAGE_UID=0 so ohs can
# write its raw store there: see docs/deploy-platforms.md.
ARG IMAGE_UID=65532
USER ${IMAGE_UID}:${IMAGE_UID}
ENTRYPOINT ["/ohs"]
# Default: apply migrations, then serve. Override with any ohs command.
CMD ["up"]
