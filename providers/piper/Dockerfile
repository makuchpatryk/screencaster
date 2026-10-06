# Piper TTS provider layer (ARCHITECTURE §12, Decision 65). Stages: piper -> dev
# -> runtime. Built on top of the provider-free images from the root Dockerfile,
# so build those first (`make dev-image` and `make image` do both steps):
#   dev-base     -> screencaster-dev-base
#   runtime-base -> screencaster-base
# `docker build -f providers/piper/Dockerfile providers/piper` yields runtime,
# the image users run. The context holds no files: everything is downloaded.
#
# The ENV lines are the only place the Piper paths live; the adapter in
# internal/adapters/tts/piper reads them through wire.TTS.
ARG DEV_BASE=screencaster-dev-base
ARG RUNTIME_BASE=screencaster-base

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

# dev: the toolchain image plus Piper, with the same provider settings as
# runtime, so tests and the shipped image resolve voices the same way.
FROM ${DEV_BASE} AS dev
COPY --from=piper /opt/piper /opt/piper
ENV SCREENCASTER_TTS=piper \
    SCREENCASTER_PIPER_BIN=/opt/piper/piper \
    SCREENCASTER_PIPER_VOICES=/opt/piper/voices

# runtime: what users run (FR-016). Last stage, so a plain build yields it.
# Run: docker compose run --rm screencaster render demos/my-demo.yaml
FROM ${RUNTIME_BASE} AS runtime
COPY --from=piper /opt/piper /opt/piper
ENV SCREENCASTER_TTS=piper \
    SCREENCASTER_PIPER_BIN=/opt/piper/piper \
    SCREENCASTER_PIPER_VOICES=/opt/piper/voices
