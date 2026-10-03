# Stages: dev (M1+) -> build (M4) -> runtime (M4). Only dev exists so far.

# dev: toolchain for `make test|vet|lint`. The source is mounted at /src, not
# copied, so rebuilds are rare (decision 52).
FROM golang:1.25-bookworm AS dev
# Pinned to the newest golangci-lint that builds on Go 1.25 (v2.13+ needs 1.26).
ARG GOLANGCI_LINT_VERSION=v2.12.0
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && mv /go/bin/golangci-lint /usr/local/bin/ \
    && rm -rf /go/pkg /root/.cache
# World-writable so a named volume mounted here is usable by `--user $(id -u)`.
RUN mkdir -m 777 /cache
ENV GOCACHE=/cache/build \
    GOMODCACHE=/cache/mod \
    GOLANGCI_LINT_CACHE=/cache/lint \
    GOFLAGS=-buildvcs=false
WORKDIR /src
