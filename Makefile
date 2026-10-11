# SPDX-License-Identifier: AGPL-3.0-only
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: setup build image test integration contract e2e reliability guard release-check

setup:
	git config core.hooksPath .githooks

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ohs ./cmd/ohs

# Multi-arch image, cross-compiled by Go (no emulation). Needs docker buildx.
# Not pushed: add --push and a registry tag to publish.
image:
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-f deploy/Dockerfile -t ohs .

test:
	go test ./...

integration:
	go test -tags integration ./...

# API contract: the quickstart calls in process, checked against api/openapi.yaml
# (testcontainers; also part of `make integration`).
contract:
	go test -tags integration -count=1 ./test/contract/

# Quickstart end to end on the compose stack (project OHS_E2E_PROJECT, default
# ohs-e2e, port OHS_E2E_PORT, default 18080). Needs docker, curl, jq. The helper tests run
# first.
e2e:
	test/e2e/lib_test.sh
	test/e2e/quickstart.sh

# kill -9 crash recovery on the compose stack (project OHS_E2E_PROJECT, default ohs-rel, port
# OHS_E2E_PORT, default 18081). Takes over 10 minutes: a job cut by the crash
# is reclaimed after the queue's visibility timeout. CI runs it on main and
# nightly (.github/workflows/reliability.yml).
reliability:
	test/e2e/reliability.sh

guard:
	scripts/guard_test.sh
	scripts/guard.sh

# Checks to run before tagging a release, with the tag to come:
#   make release-check RELEASE_TAG=v0.0.1
# The tag's release notes and the compose default version, build, vet, unit
# tests, the forbidden-terms guard (set OHS_TERMS_FILE or
# OHS_FORBIDDEN_TERMS), a secret scan when gitleaks is installed, and the
# generated data model doc. Docker suites (integration, e2e, reliability)
# stay separate.
release-check: build
	@test -n "$(RELEASE_TAG)" || { echo "release-check: set RELEASE_TAG=vX.Y.Z" >&2; exit 2; }
	scripts/check-release-version.sh $(RELEASE_TAG)
	go vet ./...
	go test ./...
	scripts/guard.sh
	@if command -v gitleaks >/dev/null 2>&1; then \
		gitleaks detect --redact --config .gitleaks.toml; \
	else \
		echo "release-check: gitleaks not installed, skipping secret scan" >&2; \
	fi
	go test -count=1 -run TestCommittedDocIsCurrent ./internal/model/cmd/gendocs/
