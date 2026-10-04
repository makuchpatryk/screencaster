# All Go tooling runs in the dev image; the host needs only Docker (decision 52).
DEV_IMAGE := screencaster-dev
IMAGE     := screencaster
MODULES   := core cli mcp tests/e2e

# --user keeps files created by the container owned by the caller.
DEV_RUN := docker run --rm --user $(shell id -u):$(shell id -g) \
	-v $(CURDIR):/src -v screencaster-go-cache:/cache $(DEV_IMAGE)

# Chromium needs more than Docker's default 64 MB of /dev/shm.
E2E_RUN := docker run --rm --user $(shell id -u):$(shell id -g) --shm-size=1g \
	-v $(CURDIR):/src -v screencaster-go-cache:/cache $(DEV_IMAGE)

# Runs $(1) in every module directory, stops at the first failure.
in_modules = $(DEV_RUN) sh -c 'for m in $(MODULES); do (cd $$m && $(1)) || exit 1; done'

.PHONY: dev-image image image-check test vet lint e2e e2e-runtime

dev-image:
	docker build --target dev -t $(DEV_IMAGE) .

# The runtime image users run (FR-016). Stage order puts it last, so a plain
# `docker build .` gives the same result.
image:
	docker build --target runtime -t $(IMAGE) .

# The built-in voice names come from core/voices, so the image and the code
# cannot drift apart (CODE_QUALITY DRY). The runtime image has no Go, hence shell.
image-check:
	@want=$$(sed -n '/^var builtin/,/^}/s/.*: *"\(.*\)",/\1/p' core/voices/voices.go); \
	test -n "$$want" || { echo "no built-in voices found in core/voices"; exit 1; }; \
	have=$$(docker run --rm $(IMAGE) ls /opt/piper/voices); \
	for v in $$want; do \
	  for ext in onnx onnx.json; do \
	    echo "$$have" | grep -qx "$$v.$$ext" || { echo "missing in image: $$v.$$ext"; exit 1; }; \
	  done; \
	done; \
	echo "image-check: ok ($$(echo $$want | tr '\n' ' '))"

test:
	$(call in_modules,go test -race ./...)

# -tags e2e so the e2e test files are vetted too.
vet:
	$(call in_modules,go vet -tags e2e ./...)

lint:
	$(call in_modules,golangci-lint run ./...)

# Local only, not in CI (decision 54). Slow: it drives real Chromium.
e2e:
	$(E2E_RUN) sh -c 'cd tests/e2e && go test -tags e2e -race -count=1 -v ./...'

# The CLI e2e tests against the runtime image (PRD M4 DoD): the test binary is
# compiled in the dev image, then runs in the runtime image next to the image's
# own screencaster binary, Chromium, Piper and ffmpeg. The fixture is served by
# the test process itself, so no network is needed.
e2e-runtime: image
	$(DEV_RUN) sh -c 'cd tests/e2e && go test -c -tags e2e -o /src/.screencaster/e2e.test .'
	docker run --rm --init --shm-size=1g -e SCREENCASTER_BIN=/usr/local/bin/screencaster \
		-v $(CURDIR):/src -w /src/tests/e2e $(IMAGE) \
		/src/.screencaster/e2e.test -test.run 'TestRender_cli' -test.count=1 -test.v
