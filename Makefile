# All Go tooling runs in the dev image; the host needs only Docker (decision 52).
DEV_IMAGE := screencaster-dev
IMAGE     := screencaster
MODULES   := core cli mcp tests/e2e

# The TTS provider layer to build on the base images: providers/$(PROVIDER)/
# (ARCHITECTURE §12). Its Dockerfile has a `dev` and a `runtime` stage.
PROVIDER     ?= piper
PROVIDER_DIR := providers/$(PROVIDER)

# --user keeps files created by the container owned by the caller.
DEV_RUN := docker run --rm --user $(shell id -u):$(shell id -g) \
	-v $(CURDIR):/src -v screencaster-go-cache:/cache $(DEV_IMAGE)

# Chromium needs more than Docker's default 64 MB of /dev/shm.
E2E_RUN := docker run --rm --user $(shell id -u):$(shell id -g) --shm-size=1g \
	-v $(CURDIR):/src -v screencaster-go-cache:/cache $(DEV_IMAGE)

# Runs $(1) in every module directory, stops at the first failure.
in_modules = $(DEV_RUN) sh -c 'for m in $(MODULES); do (cd $$m && $(1)) || exit 1; done'

.PHONY: dev-image image image-base image-check test vet lint e2e e2e-runtime

# The toolchain base, then the provider's dev stage on top. The provider build
# context is its own folder: its Dockerfile downloads everything it needs.
dev-image:
	docker build --target dev-base -t $(DEV_IMAGE)-base .
	docker build -f $(PROVIDER_DIR)/Dockerfile --target dev \
		-t $(DEV_IMAGE):$(PROVIDER) -t $(DEV_IMAGE) $(PROVIDER_DIR)

# The runtime image users run (FR-016): the base plus the provider layer, tagged
# :$(PROVIDER) and latest.
image: image-base
	docker build -f $(PROVIDER_DIR)/Dockerfile --target runtime \
		-t $(IMAGE):$(PROVIDER) -t $(IMAGE) $(PROVIDER_DIR)

# The provider-free base: Chromium, ffmpeg and the binaries, no TTS. Every
# provider's runtime stage builds on it.
image-base:
	docker build --target runtime-base -t $(IMAGE)-base .

# Each provider owns its check (the built-in voices, for Piper), run against the
# image `make image` tagged.
image-check:
	@sh $(PROVIDER_DIR)/image-check.sh $(IMAGE):$(PROVIDER)

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

# The CLI and card e2e tests against the runtime image (PRD M4 DoD): the test binary is
# compiled in the dev image, then runs in the runtime image next to the image's
# own screencaster binary, Chromium, TTS provider and ffmpeg. The fixture is served by
# the test process itself, so no network is needed.
e2e-runtime: image
	$(DEV_RUN) sh -c 'cd tests/e2e && go test -c -tags e2e -o /src/.screencaster/e2e.test .'
	docker run --rm --init --shm-size=1g -e SCREENCASTER_BIN=/usr/local/bin/screencaster \
		-v $(CURDIR):/src -w /src/tests/e2e $(IMAGE) \
		/src/.screencaster/e2e.test -test.run 'TestRender_cli|TestScreenshot_' -test.count=1 -test.v
