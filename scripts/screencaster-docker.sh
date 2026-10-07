#!/bin/sh
# screencaster-docker: run screencaster in its Docker image, with nothing
# installed on this machine but Docker. Same arguments as `screencaster`:
#
#   ./screencaster-docker render demos/my-demo.yaml
#
# The current folder is mounted at /work, so demos and outputs live there. This
# file is a template: the release workflow fills in the image tag's version
# (decision 77).
set -eu

# --init forwards SIGTERM; --shm-size because Chromium needs more than Docker's
# 64 MB default; --add-host lets a baseUrl of http://host.docker.internal:3000
# reach an app on this machine (Linux).
exec docker run --rm --init --shm-size=1g \
  --add-host=host.docker.internal:host-gateway \
  -v "$PWD:/work" -w /work \
  --entrypoint screencaster \
  ghcr.io/makuchpatryk/screencaster:@VERSION@ "$@"
