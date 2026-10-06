# Provider-free base. Stages: dev-base -> build -> runtime-base. `make dev-image`
# targets dev-base (screencaster-dev-base), `make image-base` targets runtime-base
# (screencaster-base: Chromium, ffmpeg, binaries, no TTS). Both are bases only:
# the binaries exit at startup without SCREENCASTER_TTS (ARCHITECTURE §12), so
# the image users run comes from a provider layer in providers/<name>/Dockerfile.
#
# TTS providers (ARCHITECTURE §15): to add one, create providers/<name>/ with a
# Dockerfile whose `dev` and `runtime` stages build on those two base images and
# set the ENV lines its adapter reads (see providers/piper/Dockerfile):
#   - local binary: a stage that fetches it, then COPY + ENV in both stages;
#   - cloud API or sidecar: ENV only (it breaks NFR-003 offline and needs a
#     BR-001 determinism review).
# No provider path lives in Go code or in this file.

# dev-base: toolchain for `make test|vet|lint`. The source is mounted at /src, not
# copied, so rebuilds are rare (decision 52).
FROM golang:1.25-bookworm AS dev-base
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
# core/assembler muxes with it.
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

# build: static Go binaries plus the playwright CLI at the version go.mod pins,
# so the runtime image cannot drift from the library (the dev image still pins
# its own copy above).
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.work ./
COPY core core
COPY cli cli
COPY mcp mcp
COPY tests/e2e/go.mod tests/e2e/go.mod
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    mkdir /out \
    && (cd cli && CGO_ENABLED=0 go build -trimpath -o /out/screencaster .) \
    && (cd mcp && CGO_ENABLED=0 go build -trimpath -o /out/screencaster-mcp .) \
    && (cd core && go build -o /out/playwright github.com/mxschmitt/playwright-go/cmd/playwright)

# runtime-base: everything but a TTS provider. Runs as root with the project
# mounted at /work. Use `docker run --init` so SIGTERM reaches the process
# (ARCHITECTURE §6.4). Without SCREENCASTER_TTS the binaries refuse to start;
# a provider layer sets it.
FROM debian:bookworm-slim AS runtime-base
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    HOME=/tmp
COPY --from=build /out/playwright /usr/local/bin/playwright
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && playwright install --with-deps chromium \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/* /usr/local/bin/playwright
COPY --from=build /out/screencaster /out/screencaster-mcp /usr/local/bin/
WORKDIR /work
