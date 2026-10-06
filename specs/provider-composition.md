# Provider Composition — Dockerfile Refactor

## Summary
Piper is baked into the Dockerfile alongside core tooling (Chromium, ffmpeg). This couples every provider change to the shared build file and makes each new provider a diff against Piper's stages. Split the build into:
- `Dockerfile`: base image only (Go binaries, Chromium, ffmpeg, playwright driver).
- `providers/<name>/Dockerfile`: TTS provider layer on top of base (fetch, install, `ENV` the provider adapter's requirements).
- `providers/piper/dev.Dockerfile` (optional): dev image with the provider, for testing.
- `providers/piper/compose.yaml` (optional): provider-specific overrides.

Each provider is self-contained: adding one means adding its directory, touching no existing file except `core/provider/provider.go` (the `FromEnv` case). The base image is provider-agnostic and reusable.

## Why
Current state:
- `Dockerfile` has 5 stages: `piper` (fetch), `dev` (toolchain + Piper), `build`, `runtime-base`, `runtime-piper`.
- Every provider adds stages and a conditional ENV setup in `dev` and `runtime-*`.
- `Makefile` targets (`make image`, `make dev-image`) and `compose.yaml` hardcode `runtime-piper` and `piper`.
- New provider means editing Dockerfile header, adding stages, and updating Makefile/compose.
- Tests run only against Piper (the dev image has it baked in).

Desired state:
- Base `Dockerfile`: 3 stages (`dev-base`, `build`, `runtime-base`), no TTS code.
- `providers/piper/Dockerfile`: fetches Piper, copies to base, sets `ENV`.
- `providers/google-tts/Dockerfile` (future): same pattern, different tool and `ENV`.
- `Makefile` and `compose.yaml`: named by provider, not hardcoded.
- Tests: build `dev-base`, then mount or add the provider.

## Success Criteria
- `Dockerfile` mentions no provider by name; no env vars tied to Piper.
- Each provider lives in `providers/<name>/` with its own `Dockerfile` (and optionally `compose.yaml`, `dev.Dockerfile`).
- `make image PROVIDER=piper` (default `PROVIDER=piper`) builds the provider image.
- `make dev-image PROVIDER=piper` builds a dev image with the provider.
- `compose.yaml` at the root references `screencaster-dev:piper` and `screencaster:piper` by default; provider-specific overrides live in `providers/piper/compose.yaml`.
- `make image-check PROVIDER=piper` validates the provider image.
- Adding a new provider: create `providers/<name>/Dockerfile`, add a case to `provider.FromEnv`, done. No Dockerfile edits.

## Design
### Dockerfile (base, no TTS)
```dockerfile
# dev-base: toolchain only (golangci-lint, playwright-go, ffmpeg, world-writable /cache).
# Used by both make test and provider-specific dev images.
FROM golang:1.25-bookworm AS dev-base
# ... (golangci-lint, playwright-go, ffmpeg, /cache setup) ...

# build: static Go binaries.
FROM golang:1.25-bookworm AS build
# ... (unchanged) ...

# runtime-base: everything but a TTS provider.
FROM debian:bookworm-slim AS runtime-base
# ... (unchanged, no SCREENCASTER_TTS) ...
```

### providers/piper/Dockerfile
```dockerfile
# Piper binary and voices (shared by dev and runtime).
FROM debian:bookworm-slim AS piper
# ... (fetch Piper + voices, everything from current piper stage) ...

# dev: FROM dev-base + Piper (for make test with Piper).
FROM screencaster-dev-base AS dev
COPY --from=piper /opt/piper /opt/piper
ENV SCREENCASTER_TTS=piper \
    SCREENCASTER_PIPER_BIN=/opt/piper/piper \
    SCREENCASTER_PIPER_VOICES=/opt/piper/voices

# runtime: FROM runtime-base + Piper (what users run).
FROM screencaster:base AS runtime
COPY --from=piper /opt/piper /opt/piper
ENV SCREENCASTER_TTS=piper \
    SCREENCASTER_PIPER_BIN=/opt/piper/piper \
    SCREENCASTER_PIPER_VOICES=/opt/piper/voices
```

### providers/piper/compose.yaml (optional)
Override or supplement root `compose.yaml` for Piper-specific settings.

### Makefile
```makefile
PROVIDER ?= piper

dev-image:
	docker build -f Dockerfile --target dev-base -t screencaster-dev-base .
	docker build -f providers/$(PROVIDER)/Dockerfile --target dev -t screencaster-dev:$(PROVIDER) .

image:
	docker build -f Dockerfile --target runtime-base -t screencaster:base .
	docker build -f providers/$(PROVIDER)/Dockerfile --target runtime -t screencaster:$(PROVIDER) -t screencaster .

image-base:
	docker build -f Dockerfile --target runtime-base -t screencaster:base .

image-check:
	docker run --rm screencaster:$(PROVIDER) ls /opt/piper/voices | ... (unchanged voice check logic) ...

e2e-runtime: image
	docker run --rm -v .:/work screencaster:$(PROVIDER) render demos/my-demo.yaml
```

### compose.yaml (root)
```yaml
services:
  screencaster:
    image: screencaster:piper  # default; user can override or use COMPOSE_PROFILES
    build:
      context: .
      dockerfile: providers/piper/Dockerfile  # or a build arg
      target: runtime
    # ...
```

Alternatively, no `build` in root `compose.yaml`, and users run `make image` first, then `docker compose run`.

### providers/piper/compose.yaml (optional, for future multi-provider setups)
Could define service variants for each provider, or be empty if the root `compose.yaml` is provider-agnostic.

## File Changes

### New files
- `providers/piper/Dockerfile` (moved from Dockerfile, + dev target)
- `providers/piper/compose.yaml` (optional, initially empty or minimal overrides)

### Modified files
- `Dockerfile`: remove piper stage, dev split into dev-base, remove runtime-piper
- `Makefile`: add `PROVIDER` var, update targets to build base + provider
- `compose.yaml`: point to providers/piper/Dockerfile or remove build section
- `ARCHITECTURE.md`: §15 Docker now has a base + provider pattern; add decision on composition
- `.gitignore`: no change (provider dirs are in the repo)

### Unchanged
- `core/provider/provider.go`: already factory-pattern; add Piper case (already exists)
- `core/provider/piper/*`: no change
- `core/provider/provider_test.go`: no change

## Dependencies
- None (base Dockerfile is provider-free; provider images depend on base)

## Risks & Trade-offs

| Risk | Mitigation |
|------|-----------|
| Two-stage build complicates local dev | `make dev-image` hides it; document in README. |
| Provider image build order (base first) | Makefile enforces order with explicit targets. |
| compose.yaml build context (needs dockerfile path) | Either remove build from root and require `make image` first, or add dockerfile build arg. |
| New provider setup copy-paste from Piper | Create a template / scaffold script. |

## Implementation Steps

1. **Refactor base `Dockerfile`:**
   - Move `piper` stage to `providers/piper/Dockerfile`.
   - Rename `dev` to `dev-base`, remove Piper-specific `ENV` and `COPY`.
   - Keep `build` and `runtime-base` unchanged.

2. **Create `providers/piper/Dockerfile`:**
   - Add `piper` stage (from current Dockerfile).
   - Add `dev` target: `FROM screencaster-dev-base` + Piper + `ENV`.
   - Add `runtime` target: `FROM screencaster:base` + Piper + `ENV`.

3. **Update `Makefile`:**
   - Add `PROVIDER ?= piper`.
   - Update `dev-image`: build base, then provider dev.
   - Update `image`: build base, then provider runtime.
   - Keep `image-base` as-is (builds runtime-base only).
   - Update `image-check` to use `PROVIDER` var.
   - Update `e2e-runtime` to use `PROVIDER` image.

4. **Update `compose.yaml`:**
   - Point `build.dockerfile` to `providers/piper/Dockerfile`.
   - Or remove build section and require `make image` first (simpler, but less convenient).

5. **Update `ARCHITECTURE.md`:**
   - §15 Docker: describe base + provider layers.
   - Add decision: composition pattern, why (reusability, no coupling).
   - Update dependency diagram to show base → provider.

6. **Tests:**
   - `make test` still uses `dev-base` + mounted Piper (no image build needed).
   - Or: `make dev-image` produces `dev:piper`, and tests run in that.
   - CI: build base once, then each provider in parallel.

7. **Documentation:**
   - `README.md`: explain `make image PROVIDER=<name>`, list available providers, new provider setup.
   - Example: add a stub `providers/google-tts/Dockerfile` (comment: "example, not implemented").

## Checkpoints (Todo List)
- [x] CP1 — Split: steps 1-2; `providers/piper/Dockerfile` has `piper`, `dev`, `runtime`; root `Dockerfile` names no provider (`grep -i piper Dockerfile` only hits the header pointer). — only line 9 hits
- [x] CP2 — Build wiring: steps 3-4; `make dev-image image image-check` green, `docker compose config` valid. — compose has no `build` (compose v2.12 cannot chain builds); base tag stays `screencaster-base`; `image-check` moved to `providers/piper/image-check.sh`
- [x] CP3 — Verify: step 6; `make test vet lint` green in the new dev image; base image exits 1 "no TTS provider"; `make e2e-runtime` (optional, slow). — vet/lint/test green, base exits 1; e2e-runtime not run
- [x] CP4 — Docs: steps 5, 7; ARCHITECTURE §12/§15/Decision 65, CODE_QUALITY, README match the new layout.

## Alternatives Considered

**A. Single Dockerfile with `PROVIDER` build arg (ARG-based composition):**
- Pro: single build file, less duplication.
- Con: Dockerfile is harder to read (conditionals), Piper leaks into the base logic, no file encapsulation per provider.
- Verdict: rejected; violates intent to decouple providers.

**C. Separate git repositories per provider (repo-per-provider):**
- Pro: zero coupling, independent release cycle.
- Con: adds CI complexity, version drift between base and providers.
- Verdict: overkill for now; revisit if providers diverge significantly.
