# Runs the Go test suite on Linux as an unprivileged user (make test-linux).
# The source is mounted read-only at /src by run.sh; nothing is copied in.
FROM golang:1.26.8-bookworm@sha256:dc9ad6c05acc7a88e5b71bde60a5fe3bd4b9f0db209011711b464107438a8107

# procps: tests/cli lists daemon processes with /bin/ps.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends procps \
	&& rm -rf /var/lib/apt/lists/*

# A fresh named volume at /go/pkg/mod (mcparcel-gomod) or /go/cache
# (mcparcel-gocache, the build cache) copies this directory's owner, so both
# caches are writable by the test user. A volume created earlier is owned by
# root and stays that way: remove it with
# `docker volume rm mcparcel-gomod mcparcel-gocache` and run again.
RUN groupadd -g 10001 runner \
	&& useradd -u 10001 -g 10001 -m -d /home/runner -s /bin/sh runner \
	&& mkdir -p /go/pkg/mod /go/cache \
	&& chown 10001:10001 /go/pkg/mod /go/cache

# The image's Go is the module's pinned toolchain; never download another.
ENV HOME=/home/runner GOTOOLCHAIN=local GOCACHE=/go/cache
USER 10001:10001
WORKDIR /src
