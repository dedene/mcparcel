#!/usr/bin/env bash
# Builds the headless e2e image and runs tests/headless in it
# (make test-headless-e2e [CLAW_WRAP_DIR=<claw-wrap checkout>]).
#
# The container mirrors the target pod: read-only root fs, the state root on
# a world-writable tmpfs (an emptyDir), /run and /tmp as sticky tmpfs, uid
# 10001 without a passwd entry, --init as the reaping PID 1 for the
# auto-started daemon, and no network beyond loopback (the fake Front runs in
# the test process). HOME=/tmp/home is created by the test.
#
# With CLAW_WRAP_DIR the image also builds claw-wrap from that checkout
# (BuildKit named context) and TestE2EThroughClawWrap runs; without it that
# test is skipped by name and the script says so. Extra arguments go to the
# test binary (e.g. -test.run TestE2EProactiveRefresh).
set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
	echo "make test-headless-e2e: docker not found in PATH; install Docker and try again." >&2
	exit 2
fi

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="${MCPARCEL_HEADLESS_IMAGE:-mcparcel-headless-e2e}"
cd "$repo"

build=(docker build -f tests/headless/Dockerfile -t "$image")
filter=()
if [[ -n "${CLAW_WRAP_DIR:-}" ]]; then
	if [[ ! -f "$CLAW_WRAP_DIR/go.mod" || ! -d "$CLAW_WRAP_DIR/cmd/claw-wrap" ]]; then
		echo "make test-headless-e2e: CLAW_WRAP_DIR=$CLAW_WRAP_DIR is not a claw-wrap checkout." >&2
		exit 2
	fi
	build+=(--build-context "clawwrap=$CLAW_WRAP_DIR" --build-arg WITH_CLAW_WRAP=1)
else
	echo "make test-headless-e2e: CLAW_WRAP_DIR is not set; the image has no claw-wrap and TestE2EThroughClawWrap is skipped. Set CLAW_WRAP_DIR=<claw-wrap checkout> to run it." >&2
	filter=(-test.skip '^TestE2EThroughClawWrap$')
fi

"${build[@]}" .
echo "image $image $(docker image inspect --format '{{.Id}}' "$image")"

docker run --rm --read-only --init -u 10001:10001 --network none \
	--tmpfs /var/lib/mcparcel:mode=0777 --tmpfs /run:mode=1777 --tmpfs /tmp:mode=1777 \
	-e HOME=/tmp/home "$image" -test.v -test.count=1 -test.timeout 10m ${filter[@]+"${filter[@]}"} "$@"
