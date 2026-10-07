SHELL := /bin/bash

.DEFAULT_GOAL := build

.PHONY: build build-linux npm-binary npm-pack test test-linux test-headless-e2e test-packaging lint fmt-check vet-linux ci tools

BIN := $(CURDIR)/bin/mcparcel
NPM_BIN := $(CURDIR)/packaging/npm/dist/mcparcel
DIST := $(CURDIR)/dist
LINUX_ARCHES := amd64 arm64
CMD := ./cmd/mcparcel
PKG := github.com/dedene/mcparcel/internal/cmd

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo "")
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X $(PKG).version=$(VERSION) -X $(PKG).commit=$(COMMIT) -X $(PKG).date=$(DATE)

# packaging/npm/package.json is the release version source. The npm binary is
# <version>+<commit>, and a build from a tree with uncommitted changes adds
# .dirty.<UTC seconds>: it claims no clean commit, and two such builds never
# pass the daemon handshake as the same version. Recursive (=) so only npm
# targets run node and git status.
NPM_VERSION = $(shell node -p "require('./packaging/npm/package.json').version" 2>/dev/null)
NPM_DIRTY = $(shell test -z "$$(git status --porcelain 2>/dev/null)" || date -u +.dirty.%Y%m%d%H%M%S)
NPM_LDFLAGS = -X $(PKG).version=$(NPM_VERSION)+$(COMMIT)$(NPM_DIRTY) -X $(PKG).commit=$(COMMIT) -X $(PKG).date=$(DATE)

# The 1Password desktop integration and the Keychain binding need CGO.
export CGO_ENABLED := 1

TOOLS_DIR := $(CURDIR)/.tools
export GOLANGCI_LINT_CACHE := $(TOOLS_DIR)/golangci-lint-cache
GOFUMPT := $(TOOLS_DIR)/gofumpt
GOIMPORTS := $(TOOLS_DIR)/goimports
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint

build:
	@mkdir -p $(dir $(BIN))
	@go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) $(CMD)

# Linux binaries are static: no 1Password desktop integration or Keychain.
build-linux:
	@mkdir -p $(DIST)
	@for arch in $(LINUX_ARCHES); do \
		GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/mcparcel-linux-$$arch $(CMD) || exit 1; \
	done

# Ad-hoc signed with a stable identifier (the linker's own signature says
# a.out). Not Developer ID signed or notarized.
npm-binary:
	@test -n "$(NPM_VERSION)" -a -n "$(COMMIT)" || { echo "npm-binary needs node and a git checkout"; exit 1; }
	@mkdir -p $(dir $(NPM_BIN))
	@GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(NPM_LDFLAGS)" -o $(NPM_BIN) $(CMD)
	@codesign --force --sign - --identifier mcparcel $(NPM_BIN)
	@codesign --verify --strict $(NPM_BIN)

# A local tarball in dist/; package.json keeps private: true, so npm publish refuses.
npm-pack: npm-binary
	@mkdir -p $(DIST)
	@cd packaging/npm && npm pack --pack-destination $(DIST)

test:
	@go test -race -timeout 20m ./...

# Full go test -race ./... in a Linux container as uid 10001 (needs Docker).
test-linux:
	@tests/linux/run.sh

# Headless end-to-end proof in a pod-shaped Linux container (needs Docker).
# CLAW_WRAP_DIR=<claw-wrap checkout> also builds claw-wrap and runs
# TestE2EThroughClawWrap; without it that test is skipped.
test-headless-e2e:
	@CLAW_WRAP_DIR="$(CLAW_WRAP_DIR)" tests/headless/run.sh

test-packaging: npm-binary
	@node --test tests/packaging/*.mjs

tools:
	@mkdir -p $(TOOLS_DIR)
	@GOBIN=$(TOOLS_DIR) go install mvdan.cc/gofumpt@v0.9.2
	@GOBIN=$(TOOLS_DIR) go install golang.org/x/tools/cmd/goimports@v0.41.0
	@GOBIN=$(TOOLS_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.8.0

fmt-check: tools
	@test -z "$$($(GOFUMPT) -l .)" || { $(GOFUMPT) -l .; echo "run gofumpt -w ."; exit 1; }
	@test -z "$$($(GOIMPORTS) -local github.com/dedene/mcparcel -l .)" || { $(GOIMPORTS) -local github.com/dedene/mcparcel -l .; echo "run goimports -w ."; exit 1; }

vet-linux:
	@GOOS=linux CGO_ENABLED=0 go vet ./...
	@GOOS=linux CGO_ENABLED=0 go vet -tags mcparceltest ./...

lint: tools
	@$(GOLANGCI_LINT) run

ci: fmt-check lint vet-linux test test-packaging
