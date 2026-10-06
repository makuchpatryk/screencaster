#!/bin/sh
# Checks that the built-in voices from the Piper adapter are in the image
# (CODE_QUALITY DRY). Usage: image-check.sh <image>, run from the repo root.
# The voice folder is the image's own SCREENCASTER_PIPER_VOICES, so its path
# lives only in the Dockerfile. The runtime image has no Go, hence shell.
set -eu

image=$1
want=$(sed -n '/^var Defaults/,/^}/s/.*: *"\(.*\)",/\1/p' internal/adapters/tts/piper/piper.go)
test -n "$want" || { echo "no built-in voices found in internal/adapters/tts/piper"; exit 1; }
have=$(docker run --rm "$image" sh -c 'ls "$SCREENCASTER_PIPER_VOICES"')
for v in $want; do
  for ext in onnx onnx.json; do
    echo "$have" | grep -qx "$v.$ext" || { echo "missing in image: $v.$ext"; exit 1; }
  done
done
echo "image-check: ok ($(echo $want | tr '\n' ' '))"
