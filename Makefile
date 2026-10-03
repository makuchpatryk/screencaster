# All Go tooling runs in the dev image; the host needs only Docker (decision 52).
DEV_IMAGE := screencaster-dev
MODULES   := core cli mcp tests/e2e

# --user keeps files created by the container owned by the caller.
DEV_RUN := docker run --rm --user $(shell id -u):$(shell id -g) \
	-v $(CURDIR):/src -v screencaster-go-cache:/cache $(DEV_IMAGE)

# Runs $(1) in every module directory, stops at the first failure.
in_modules = $(DEV_RUN) sh -c 'for m in $(MODULES); do (cd $$m && $(1)) || exit 1; done'

.PHONY: dev-image test vet lint

dev-image:
	docker build --target dev -t $(DEV_IMAGE) .

test:
	$(call in_modules,go test -race ./...)

vet:
	$(call in_modules,go vet ./...)

lint:
	$(call in_modules,golangci-lint run ./...)
