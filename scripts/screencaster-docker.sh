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

# --init forwards SIGTERM. Nothing else is needed: Playwright starts Chromium
# without /dev/shm, and an app on this machine is reached by its bridge IP,
# written into the demo's goto URLs (decision 79).
exec docker run --rm --init \
  -v "$PWD:/work" -w /work \
  --entrypoint screencaster \
  ghcr.io/makuchpatryk/screencaster:@VERSION@ "$@"
