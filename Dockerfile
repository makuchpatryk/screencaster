# Stages: dev (M1+) -> build (M4) -> runtime (M4). Only dev exists so far.
# dev grows with the milestones: Chromium + ffmpeg (M2), Piper (M3).

# dev: toolchain for `make test|vet|lint`. The source is mounted at /src, not
# copied, so rebuilds are rare (decision 52).
FROM golang:1.25-bookworm AS dev
# Pinned to the newest golangci-lint that builds on Go 1.25 (v2.13+ needs 1.26).
ARG GOLANGCI_LINT_VERSION=v2.12.0
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && mv /go/bin/golangci-lint /usr/local/bin/ \
    && rm -rf /go/pkg /root/.cache
# Chromium for the recorder tests (M2). The module path is mxschmitt, not
# playwright-community: the project moved from v0.6100.0 on. The driver and
# browser live outside $HOME so any --user can run them (core/browser takes the
# driver directory as an argument; the driver itself reads PLAYWRIGHT_BROWSERS_PATH).
ARG PLAYWRIGHT_GO_VERSION=v0.6201.1
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    HOME=/tmp
RUN GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomod GOBIN=/usr/local/bin \
      go install github.com/mxschmitt/playwright-go/cmd/playwright@${PLAYWRIGHT_GO_VERSION} \
    && playwright install --with-deps chromium \
    && chmod -R a+rX /opt/playwright-driver /opt/ms-playwright \
    && rm -rf /tmp/gocache /tmp/gomod /var/lib/apt/lists/*
# ffmpeg/ffprobe: the e2e tests read frames from the recording (spike S1) and
# M3 assembles with it. Piper joins in M3.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
# World-writable so a named volume mounted here is usable by `--user $(id -u)`.
RUN mkdir -m 777 /cache
ENV GOPATH=/cache/go \
    GOCACHE=/cache/build \
    GOMODCACHE=/cache/mod \
    GOLANGCI_LINT_CACHE=/cache/lint \
    GOFLAGS=-buildvcs=false
WORKDIR /src
