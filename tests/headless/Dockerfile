# syntax=docker/dockerfile:1
#
# Headless end-to-end image (make test-headless-e2e, see run.sh). Built from
# the repository root. The runtime stage mirrors the target pod: the static
# Linux mcparcel at /opt/mcparcel/mcparcel, /etc/mcparcel laid out like a
# mounted ConfigMap, /etc/mcparcel-secrets like a mounted Secret, claw-wrap's
# config at /etc/openclaw/wrappers.yaml, and /usr/local/bin/mcparcel ->
# claw-wrap as in the agent container. run.sh runs it with a read-only root
# fs, tmpfs at /var/lib/mcparcel, /run and /tmp, as uid 10001 and with --init
# as the reaping PID 1.

# Replaced by `--build-context clawwrap=<claw-wrap checkout>` together with
# --build-arg WITH_CLAW_WRAP=1; otherwise empty and the image has no
# claw-wrap. The build arg, not the presence of go.mod, decides: BuildKit can
# serve the COPY from this empty stage out of a cache filled by a claw-wrap
# build.
FROM scratch AS clawwrap

FROM golang:1.26.8-bookworm@sha256:dc9ad6c05acc7a88e5b71bde60a5fe3bd4b9f0db209011711b464107438a8107 AS build
ENV GOTOOLCHAIN=local GOFLAGS=-buildvcs=false CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	go build -trimpath -o /out/mcparcel ./cmd/mcparcel \
	&& go test -c -tags headless_e2e -o /out/headless.test ./tests/headless

FROM golang:1.26.8-bookworm@sha256:dc9ad6c05acc7a88e5b71bde60a5fe3bd4b9f0db209011711b464107438a8107 AS clawwrap-build
ENV GOTOOLCHAIN=local GOFLAGS=-buildvcs=false CGO_ENABLED=0
ARG WITH_CLAW_WRAP=0
COPY --from=clawwrap / /claw-wrap/
WORKDIR /claw-wrap
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	mkdir -p /out \
	&& if [ "$WITH_CLAW_WRAP" = 1 ]; then go build -trimpath -o /out/claw-wrap ./cmd/claw-wrap; fi

FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
# procps: ps for inspecting the container by hand. PID 1 is docker's --init.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends procps \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build /out/mcparcel /opt/mcparcel/mcparcel
COPY --from=build /out/headless.test /e2e/headless.test
COPY --from=clawwrap-build /out/ /usr/local/bin/
COPY tests/headless/testdata/config.json tests/headless/testdata/personal.json tests/headless/testdata/selections.json /etc/mcparcel/..2026_10_07_00_00_00.000000000/
COPY tests/headless/testdata/wrappers.yaml /etc/openclaw/wrappers.yaml
# A 1Password service-account token (garbage, see e2e_onepassword_test.go)
# as a Secret volume with defaultMode 0440 and fsGroup 10001 lays it out:
# root:10001 0440 behind the ..data symlink. A second copy at root 0444 (the
# default mode without fsGroup) is refused as readable by others.
COPY --chown=0:10001 --chmod=0440 tests/headless/testdata/op-token /etc/mcparcel-secrets/..2026_10_07_00_00_00.000000000/op-token
COPY --chown=0:0 --chmod=0444 tests/headless/testdata/op-token /etc/mcparcel-world/op-token
# A ConfigMap volume as the kubelet lays it out: a timestamped directory, a
# ..data symlink to it and one top-level symlink per key, all root-owned,
# files 0644. The mount root is 0777, the worst case a kubelet produces. The
# Secret volume gets the same ..data layout.
RUN set -eu; \
	cd /etc/mcparcel; \
	ln -s ..2026_10_07_00_00_00.000000000 ..data; \
	for f in config.json personal.json selections.json; do ln -s "..data/$f" "$f"; done; \
	chown -R root:root /etc/mcparcel /etc/openclaw; \
	chmod 0755 ..2026_10_07_00_00_00.000000000; \
	chmod 0644 ..2026_10_07_00_00_00.000000000/* /etc/openclaw/wrappers.yaml; \
	chmod 0777 /etc/mcparcel; \
	cd /etc/mcparcel-secrets; \
	ln -s ..2026_10_07_00_00_00.000000000 ..data; \
	ln -s ..data/op-token op-token; \
	chmod 0755 ..2026_10_07_00_00_00.000000000; \
	chmod 0777 /etc/mcparcel-secrets; \
	chmod 0755 /etc/mcparcel-world; \
	if [ -x /usr/local/bin/claw-wrap ]; then ln -s claw-wrap /usr/local/bin/mcparcel; fi
USER 10001:10001
WORKDIR /
ENTRYPOINT ["/e2e/headless.test"]
