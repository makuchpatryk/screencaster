# All Go tooling runs in the dev image; the host needs only Docker (decision 52).
DEV_IMAGE := screencaster-dev
MODULES   := core cli mcp tests/e2e

# --user keeps files created by the container owned by the caller.
DEV_RUN := docker run --rm --user $(shell id -u):$(shell id -g) \
	-v $(CURDIR):/src -v screencaster-go-cache:/cache $(DEV_IMAGE)

# Chromium needs more than Docker's default 64 MB of /dev/shm.
E2E_RUN := docker run --rm --user $(shell id -u):$(shell id -g) --shm-size=1g \
	-v $(CURDIR):/src -v screencaster-go-cache:/cache $(DEV_IMAGE)

# Runs $(1) in every module directory, stops at the first failure.
in_modules = $(DEV_RUN) sh -c 'for m in $(MODULES); do (cd $$m && $(1)) || exit 1; done'

.PHONY: dev-image test vet lint e2e

dev-image:
	docker build --target dev -t $(DEV_IMAGE) .

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
