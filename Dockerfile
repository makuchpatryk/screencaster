# Stages: piper (shared) -> dev -> build -> runtime. `docker build .` yields the
# runtime image; `make dev-image` targets dev.
# dev grows with the milestones: Chromium + ffmpeg (M2), Piper + voices (M3).

# piper: the archived C++ release and the two built-in voices (BR-011), shared
# by dev and runtime so both run the same bytes. Everything is sha256-pinned;
# the voices come from a fixed Hugging Face revision. The binary finds its libs
# and espeak-ng-data next to itself.
FROM debian:bookworm-slim AS piper
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
ARG PIPER_VERSION=2023.11.14-2
ARG PIPER_SHA256=a50cb45f355b7af1f6d758c1b360717877ba0a398cc8cbe6d2a7a3a26e225992
ARG VOICES_REV=c10ece1aade47bb51c153c893d14e5bf8e5b7117
RUN set -eu; \
    cd /tmp; \
    curl -fsSL -o piper.tgz https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/piper_linux_x86_64.tar.gz; \
    echo "${PIPER_SHA256}  piper.tgz" | sha256sum -c -; \
    tar xzf piper.tgz -C /opt; \
    rm piper.tgz; \
    mkdir /opt/piper/voices; \
    cd /opt/piper/voices; \
    base=https://huggingface.co/rhasspy/piper-voices/resolve/${VOICES_REV}; \
    curl -fsSL -o en_US-ryan-high.onnx           $base/en/en_US/ryan/high/en_US-ryan-high.onnx; \
    curl -fsSL -o en_US-ryan-high.onnx.json      $base/en/en_US/ryan/high/en_US-ryan-high.onnx.json; \
    curl -fsSL -o pl_PL-darkman-medium.onnx      $base/pl/pl_PL/darkman/medium/pl_PL-darkman-medium.onnx; \
    curl -fsSL -o pl_PL-darkman-medium.onnx.json $base/pl/pl_PL/darkman/medium/pl_PL-darkman-medium.onnx.json; \
    printf '%s\n' \
      "b3990d7606e183ec8dbfba70a4607074f162de1a0c412e0180d1ff60bb154eca  en_US-ryan-high.onnx" \
      "c6d3b98f08315cb4bebf0d49d50fc4ff491b503c64b940cd3d5ca28543b48011  en_US-ryan-high.onnx.json" \
      "db505438a5364e8e2e0242c4324130a873ed660dfbe8d9689cef428ffb1b645f  pl_PL-darkman-medium.onnx" \
      "70f999f11fa8ad13d3ef779041ee93c9f38be5abdbacdfad42449712fe91c81b  pl_PL-darkman-medium.onnx.json" \
      | sha256sum -c -; \
    chmod -R a+rX /opt/piper

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
# core/assembler muxes with it.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
# Piper and the voices (see the piper stage).
COPY --from=piper /opt/piper /opt/piper
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

# runtime: what users run (FR-016). Runs as root with the project mounted at
# /work. Use `docker run --init` so SIGTERM reaches the process (ARCHITECTURE §6.4).
FROM debian:bookworm-slim AS runtime
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    HOME=/tmp
COPY --from=build /out/playwright /usr/local/bin/playwright
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && playwright install --with-deps chromium \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/* /usr/local/bin/playwright
COPY --from=piper /opt/piper /opt/piper
COPY --from=build /out/screencaster /out/screencaster-mcp /usr/local/bin/
WORKDIR /work
