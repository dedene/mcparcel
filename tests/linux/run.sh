#!/usr/bin/env bash
# Runs go test -race ./... in a Linux container as uid 10001 (make test-linux).
# Step 1 (with network) fills the mcparcel-gomod volume; step 2 runs the tests
# without network. The mcparcel-gocache volume keeps the build cache between
# runs, so -race compiles only what changed. -count=1 still runs every test on
# every run: tests/cli builds ./cmd/mcparcel in a subprocess, so Go's test
# result cache does not see CLI changes and would report a stale "(cached)"
# pass; the volume is also shared by every worktree mounted at /src. tests/cli
# needs ~8 of the default 10 minutes in the container, so the timeout is
# raised. Extra arguments go to go test.
#
# Both volumes must be writable by uid 10001. A volume first created by an
# older image (or by hand) is owned by root and fails with "permission
# denied"; remove it with `docker volume rm mcparcel-gomod mcparcel-gocache`
# and run again.
set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
	echo "make test-linux: docker not found in PATH; install Docker and try again." >&2
	exit 2
fi

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="${MCPARCEL_LINUX_IMAGE:-mcparcel-test-linux}"
cd "$repo"

docker build -t "$image" tests/linux

# --init: tests check that killed descendants are gone (kill -0); without a
# reaping PID 1, orphans reparented to `go test` stay zombies and look alive.
common=(--rm --init -u 10001:10001 -v "$PWD":/src:ro -v mcparcel-gomod:/go/pkg/mod -v mcparcel-gocache:/go/cache -e GOFLAGS=-buildvcs=false -e GOCACHE=/go/cache)
docker run "${common[@]}" "$image" go mod download
docker run "${common[@]}" --network none "$image" go test -race -count=1 -timeout 20m ./... "$@"
