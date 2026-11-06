# Provider-free base. Stages: playwright, dev-base -> build -> runtime-base. `make dev-image`
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

# playwright: the Playwright driver (Node.js + the playwright-core package) and
# the browsers (Chromium, its headless shell, Playwright's ffmpeg), fetched from
# pinned URLs and checked with sha256sum before anything is unpacked, shared by
# dev-base and runtime-base so both run the same bytes. This is what
# the library's installer did before; nothing here runs a downloaded file, and the
# layout is the one `screencaster setup` writes (internal/app/setup/pins.env
# repeats these ARGs, pins_test.go keeps them equal; decision 82). The values are
# the ones playwright-go PLAYWRIGHT_GO_VERSION downloads; keep that version
# equal to go.mod's (guarded).
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS playwright
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/*
ARG PLAYWRIGHT_GO_VERSION=v0.6201.1
ARG NODE_VERSION=24.19.0
ARG NODE_URL_BASE=https://nodejs.org/dist/
ARG NODE_SHA256=f625d97cd707df4ff96254916fbc5ff014f09c09effe5a1e0ca8f6d41a8789d4
ARG PLAYWRIGHT_CORE_VERSION=1.62.1
ARG PLAYWRIGHT_CORE_URL_BASE=https://registry.npmjs.org/
ARG PLAYWRIGHT_CORE_SHA256=954be1e183d0ddb9748fe0d2d08b0b66a9210c74dd75c397aeb70303b9f08a00
ARG CHROMIUM_REVISION=1234
ARG CHROMIUM_VERSION=151.0.7922.34
ARG CHROMIUM_URL_BASE=https://cdn.playwright.dev/
ARG CHROMIUM_SHA256=ae8736ac28bc69278551500f219fc749575648263c43ec5990749eff43b9fcf8
ARG HEADLESS_SHELL_SHA256=3cfc2bd00d1bafcf8a68dc74c9c92bb7150ddc8d26ade948a776316e1cec4f14
ARG FFMPEG_REVISION=1011
ARG FFMPEG_SHA256=ebc74fc5b94830176a3c2914ae96bd8bc7f6a91f4f33890230f84a172ee61ccc
RUN set -eu; \
    cd /tmp; \
    d=/opt/playwright-driver; \
    mkdir -p $d; \
    curl -fsSL -o node.tgz ${NODE_URL_BASE}v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.gz; \
    echo "${NODE_SHA256}  node.tgz" | sha256sum -c -; \
    tar xzf node.tgz -C $d --strip-components=2 node-v${NODE_VERSION}-linux-x64/bin/node; \
    curl -fsSL -o core.tgz ${PLAYWRIGHT_CORE_URL_BASE}playwright-core/-/playwright-core-${PLAYWRIGHT_CORE_VERSION}.tgz; \
    echo "${PLAYWRIGHT_CORE_SHA256}  core.tgz" | sha256sum -c -; \
    tar xzf core.tgz -C $d; \
    rm node.tgz core.tgz; \
    bundle=$d/package/lib/coreBundle.js; \
    sed -i \
      -e 's/pageError\.location\.url/pageError.location?.url || ""/g' \
      -e 's/pageError\.location\.lineNumber/pageError.location?.lineNumber || 0/g' \
      -e 's/pageError\.location\.columnNumber/pageError.location?.columnNumber || 0/g' \
      $bundle; \
    grep -q 'pageError\.location?\.url || ""' $bundle; \
    grep -q 'pageError\.location?\.lineNumber || 0' $bundle; \
    grep -q 'pageError\.location?\.columnNumber || 0' $bundle
RUN set -eu; \
    cd /tmp; \
    b=/opt/ms-playwright; \
    mkdir -p $b; \
    cft=${CHROMIUM_URL_BASE}builds/cft/${CHROMIUM_VERSION}/linux64; \
    curl -fsSL -o chromium.zip $cft/chrome-linux64.zip; \
    echo "${CHROMIUM_SHA256}  chromium.zip" | sha256sum -c -; \
    unzip -q chromium.zip -d $b/chromium-${CHROMIUM_REVISION}; \
    curl -fsSL -o shell.zip $cft/chrome-headless-shell-linux64.zip; \
    echo "${HEADLESS_SHELL_SHA256}  shell.zip" | sha256sum -c -; \
    unzip -q shell.zip -d $b/chromium_headless_shell-${CHROMIUM_REVISION}; \
    curl -fsSL -o ffmpeg.zip ${CHROMIUM_URL_BASE}builds/ffmpeg/${FFMPEG_REVISION}/ffmpeg-linux.zip; \
    echo "${FFMPEG_SHA256}  ffmpeg.zip" | sha256sum -c -; \
    unzip -q ffmpeg.zip -d $b/ffmpeg-${FFMPEG_REVISION}; \
    rm chromium.zip shell.zip ffmpeg.zip; \
    for dir in $b/*/; do touch "$dir/INSTALLATION_COMPLETE"; done; \
    chmod -R a+rX /opt/playwright-driver /opt/ms-playwright

# dev-base: toolchain for `make test|vet|lint`. The source is mounted at /src, not
# copied, so rebuilds are rare (decision 52).
FROM golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473 AS dev-base
# golangci-lint stays at v2.12.0 although Go is 1.26.10; bump it together with
# ci.yml (CODE_QUALITY, known debt).
ARG GOLANGCI_LINT_VERSION=v2.12.0
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && mv /go/bin/golangci-lint /usr/local/bin/ \
    && rm -rf /go/pkg /root/.cache
# Chromium for the recorder tests (M2): the driver and browsers come from the
# pinned playwright stage and live outside $HOME so any --user can run them
# (adapters/browser takes the driver directory as an argument; the driver itself
# reads PLAYWRIGHT_BROWSERS_PATH). install-deps adds Chromium's system libraries
# and DEPENDENCIES_VALIDATED records it, so a launch skips re-validating.
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    HOME=/tmp
COPY --from=playwright /opt/playwright-driver /opt/playwright-driver
COPY --from=playwright /opt/ms-playwright /opt/ms-playwright
RUN DEBIAN_FRONTEND=noninteractive /opt/playwright-driver/node /opt/playwright-driver/package/cli.js install-deps chromium \
    && for dir in /opt/ms-playwright/*/; do touch "$dir/DEPENDENCIES_VALIDATED"; done \
    && chmod -R a+rX /opt/playwright-driver /opt/ms-playwright \
    && rm -rf /var/lib/apt/lists/*
# ffmpeg/ffprobe: the e2e tests read frames from the recording (spike S1) and
# adapters/assembler muxes with it.
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

# build: the static Go binaries. No playwright CLI: the driver and browsers come
# from the pinned playwright stage (decision 82).
FROM golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd cmd
COPY internal internal
# The release workflow passes the version (ARCHITECTURE §12); `dev` otherwise.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    mkdir /out \
    && CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=${VERSION}" -o /out/ ./cmd/...

# runtime-base: everything but a TTS provider. Non-root by default (M8):
# USER 65532:65532 below, an arbitrary unassigned UID/GID so it never collides
# with a container-local account. The wrapper, README, compose.yaml and make
# still pass the caller's ids (--user $(id -u):$(id -g)) so a bind-mounted
# /work stays writable by its host owner (ARCHITECTURE §12); only a plain
# `docker run` with no --user falls back to 65532:65532. The driver and
# browser are readable and executable by any uid. Use `docker run --init` so
# SIGTERM reaches the process (ARCHITECTURE §6.4). Without SCREENCASTER_TTS
# the binaries refuse to start; a provider layer sets it.
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS runtime-base
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    HOME=/tmp
COPY --from=playwright /opt/playwright-driver /opt/playwright-driver
COPY --from=playwright /opt/ms-playwright /opt/ms-playwright
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && DEBIAN_FRONTEND=noninteractive /opt/playwright-driver/node /opt/playwright-driver/package/cli.js install-deps chromium \
    && for dir in /opt/ms-playwright/*/; do touch "$dir/DEPENDENCIES_VALIDATED"; done \
    && chmod -R a+rX /opt/playwright-driver /opt/ms-playwright \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/screencaster /out/screencaster-mcp /usr/local/bin/
WORKDIR /work
# M8: default to an unprivileged, non-root user; overridden by --user when the
# caller passes one (see the comment above this stage).
USER 65532:65532
