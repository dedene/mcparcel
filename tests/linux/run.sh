#!/usr/bin/env bash
# Runs go test -race ./... in a Linux container as uid 10001 (make test-linux).
# Step 1 (with network) fills the mcparcel-gomod volume; step 2 runs the tests
# without network. tests/cli needs ~8 of the default 10 minutes in the
# container, so the timeout is raised. Extra arguments go to go test.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="${MCPARCEL_LINUX_IMAGE:-mcparcel-test-linux}"
cd "$repo"

docker build -t "$image" tests/linux

# --init: tests check that killed descendants are gone (kill -0); without a
# reaping PID 1, orphans reparented to `go test` stay zombies and look alive.
common=(--rm --init -u 10001:10001 -v "$PWD":/src:ro -v mcparcel-gomod:/go/pkg/mod -e GOFLAGS=-buildvcs=false -e GOCACHE=/tmp/gocache)
docker run "${common[@]}" "$image" go mod download
docker run "${common[@]}" --network none "$image" go test -race -timeout 20m ./... "$@"
