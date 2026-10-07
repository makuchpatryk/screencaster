# GitHub release distribution — Implementation Plan

## Summary
Let other people download screencaster from GitHub: a manually triggered workflow builds Linux binaries (amd64 + arm64), publishes the runtime Docker image to GHCR and creates a GitHub Release. Today there is no release job (`ci.yml` lints, tests, builds the image without pushing and runs e2e), no version injection (`cmd/screencaster-mcp/main.go:30` has `const version = "0.1.0"`, the CLI has none), and the README only explains clone-and-build.

Refreshed 2026-10-07 against the one-module layout (`cmd/`, `internal/`, `providers/`, Decisions 65 and 67); the earlier draft assumed `cli/`, `mcp/`, `core/` modules and a single root `Dockerfile` that holds Piper.

## Success Criteria
- Running the `release` workflow with `version=1.0.0` produces a GitHub Release `v1.0.0` with `screencaster_1.0.0_linux_amd64.tar.gz`, `screencaster_1.0.0_linux_arm64.tar.gz` and `SHA256SUMS`.
- `docker pull ghcr.io/makuchpatryk/screencaster:1.0.0` (and `:latest`) works for an anonymous user, and `make image-check` passed on that exact image before the push.
- `screencaster --version` and the MCP server report the released version, `dev` when built without it.
- README has an Install section covering Docker (primary) and the native binaries with their prerequisites.
- Existing `ci.yml`, all Makefile targets and the e2e tests are unchanged in behaviour and green.

## Scope & Constraints
- In scope: `release.yml`, version injection, Dockerfile `VERSION` arg, Makefile `VERSION` variable, README install section, doc updates.
- Out of scope: macOS, Windows, installers, package managers, Pages site, tag-push or per-commit triggers, GoReleaser, signing, arm64 Docker image, a second provider's image, running the e2e suite inside the release job.
- Hard constraints: determinism and offline render (BR-001, NFR-003) untouched, nothing under `internal/` changes, import boundaries untouched, `ci.yml` untouched.
- Trade-offs: simplest workflow (plain `go build` plus `docker`/`gh`) over richer tooling (KISS/YAGNI). Native binaries ship as they are, so users supply Piper, ffmpeg and Chromium themselves; the Docker image is the supported path.

## Architecture & Design

### High-Level Flow
```
workflow_dispatch(version=X.Y.Z)
  -> guard: tag vX.Y.Z absent, or already at this commit
  -> go build x2 arches (CGO_ENABLED=0, -trimpath, -ldflags "-X main.version=X.Y.Z") -> 2 tarballs + SHA256SUMS
  -> make image VERSION=X.Y.Z          (image-base, then providers/piper; tags screencaster:piper, screencaster)
  -> make image-check                  (built-in voices present in screencaster:piper)
  -> docker tag screencaster:piper ghcr.io/makuchpatryk/screencaster:{X.Y.Z,latest} -> docker push both
  -> gh release create vX.Y.Z --target $GITHUB_SHA --generate-notes <assets>
```
The release is created last, so a failed build never leaves a half-published release.

### Key Changes
- **`.github/workflows/release.yml` (new)**: trigger `workflow_dispatch` with required string input `version` (validated `^[0-9]+\.[0-9]+\.[0-9]+$`). `permissions: contents: write, packages: write`. One job on `ubuntu-latest`, `actions/checkout@v4`, `actions/setup-go@v5` with `go-version-file: go.mod` (no third copy of the Go version next to `ci.yml` and the Dockerfile). Binaries are cross-compiled on the runner: `GOOS=linux GOARCH=<arch> CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$V" -o <dir>/ ./cmd/...` (the same `CGO_ENABLED=0` the root `Dockerfile` `build` stage uses; SQLite is `modernc.org/sqlite`). One `-X main.version` reaches both mains. Image: `docker/login-action` to `ghcr.io` with `GITHUB_TOKEN`, then the existing `make image VERSION=$V` and `make image-check`, then `docker tag` + `docker push`. Image tags are `:X.Y.Z` and `:latest` (Piper is the only provider, so no provider suffix; add one when a second provider ships).
- **`Dockerfile` (root)**: in the `build` stage add `ARG VERSION=dev` and `-ldflags "-X main.version=${VERSION}"` to the `./cmd/...` build line only (not the `playwright` line).
- **`Makefile`**: `VERSION ?= dev`; the `image-base` target passes `--build-arg VERSION=$(VERSION)` (`image` depends on it, so `make image VERSION=X` reaches the `build` stage). `dev-image` does not need it.
- **`cmd/screencaster/render.go`**: add `var version = "dev"` (in `main.go`) and set `Version: version` on the cobra root in `run`. This gives `--version`. Cobra prints it through `root.SetOut(stderr)`, so it lands on stderr like all cobra output here (user choice over a stdout `version` subcommand).
- **`cmd/screencaster-mcp/main.go:30`**: `const version = "0.1.0"` becomes `var version = "dev"`. `-X` cannot set a const.
- **`README.md`**: new Install section above Quick start: Docker pull (primary); release binaries with prerequisites. Native prerequisites as the code stands: `SCREENCASTER_TTS=piper` (both binaries exit at startup without it), `SCREENCASTER_PIPER_BIN` and `SCREENCASTER_PIPER_VOICES` (any path; the Piper layer uses `/opt/piper`, voices `en_US-ryan-high` and `pl_PL-darkman-medium`), `ffmpeg` and `ffprobe` on PATH, Chromium through the playwright driver (`PLAYWRIGHT_DRIVER_PATH`, `PLAYWRIGHT_BROWSERS_PATH`, as in the root `Dockerfile` `runtime-base` stage). The exact list is verified in step 7, not assumed.
- **Data model / schema / MCP tools**: none.

### Fit with Project Docs
- **ARCHITECTURE.md**: no module or dependency-rule change; only `cmd/` mains and build files touched. §12 gets a short note on release publishing and the `VERSION` build arg; §13 CI paragraph names the release workflow (it runs `make image-check` but not e2e; CI `e2e` already covers main); §16 gets **Decision 76** (last is 75; 65 is already the provider seam): manual-dispatch release, image amd64 only (Piper pinned to `piper_linux_x86_64` in `providers/piper/Dockerfile`), native binaries need the provider env vars and tools.
- **CODE_QUALITY.md**: KISS (plain `go build`, `docker`, `gh`; reuse `make image` / `make image-check`; no GoReleaser); YAGNI (no arm64 image, no signing, no provider tag suffix); DRY (version injected once through ldflags; image verified by the existing `image-check`; Go version read from `go.mod`). Known debt: none new if `go-version-file` is used; add a line only if the workflow ends up repeating a pin.
- **Docs to update**: ARCHITECTURE §12, §13, §16; README; PRD only if it states a distribution rule (a grep of `docs/PRD.md` found only "release notes" as an audience, so no change expected; recheck in step 8).

### Alternative Approaches Considered
- GoReleaser: checksums, archives, notes in one config. Not chosen: a second config language and tool for two binaries and one Linux target.
- Extract binaries from the Docker `build` stage: not chosen, a plain cross-compile on the runner is shorter and gives both arches without QEMU.
- Multi-arch image via buildx + QEMU: not chosen (user choice); needs a per-arch Piper tarball with its own sha256 in `providers/piper/Dockerfile`, and the arm64 image would not be covered by `make e2e-runtime`. (Piper does publish `piper_linux_aarch64.tar.gz` for 2023.11.14-2, checked by HTTP 200, so this stays possible later.)
- Tag-push trigger: not chosen (user choice, manual dispatch).
- `version` subcommand (stdout) instead of `--version`: not chosen (user choice); `--version` matches the success criterion and is one line.

## Implementation Steps
1. [x] `cmd/screencaster-mcp/main.go`: `const version` to `var version = "dev"`. `cmd/screencaster/main.go`: add `var version = "dev"`; `cmd/screencaster/render.go`: set `Version: version` on the cobra root.
2. [x] Root `Dockerfile` `build` stage: `ARG VERSION=dev`, add `-ldflags "-X main.version=${VERSION}"` to the `./cmd/...` build command.
3. [x] `Makefile`: `VERSION ?= dev`; `image-base` passes `--build-arg VERSION=$(VERSION)`.
4. [x] Unit test in `cmd/screencaster`: `run(..., []string{"--version"}, ...)` prints the `version` variable (stderr buffer), exit code 0, and works with `noEnv` (no provider needed).
5. [x] Write `.github/workflows/release.yml` per the flow above. Guard step: if `git ls-remote --tags origin v$VERSION` returns a commit different from `$GITHUB_SHA`, fail with a message. Tarball layout: `screencaster` and `screencaster-mcp` at the archive root. `SHA256SUMS` via `sha256sum` over both tarballs.
6. [ ] Move the existing `v1.0.0` tag (remote and local both point at `80d0b19`, before these changes, so building from it would ship no version injection): done by you after the changes are merged to the commit you want released (delete and recreate the tag, force-push the tag). Not done by the implementation.
7. [x] Verify the native-binary prerequisites for real: in a bare `debian:bookworm-slim` container, install Piper (amd64; also check the aarch64 tarball unpacks and runs on arm64 if a host is available), voices, ffmpeg and Chromium by hand, set the env vars, run the downloaded `screencaster render` on the fixture demo, and write the README list from what was actually needed. Record how the playwright driver was obtained: if a non-Go user cannot get it without `go install github.com/mxschmitt/playwright-go/cmd/playwright@v0.6201.1`, decide (see Open Questions) whether the tarball also ships the `playwright` CLI.
8. [x] README Install section; ARCHITECTURE §12/§13/§16 (Decision 76); recheck `docs/PRD.md` for a distribution rule.
9. [ ] Trial run: `version=0.0.0-test` is rejected by the version regex, so use `0.0.1`; inspect the Release, image pull and tarballs, then delete that release, tag and GHCR version by hand. Then run the real `1.0.0`.

## Checkpoints (Todo List)
- [x] CP1 — Version plumbing: steps 1, 4; `make test vet lint` green, `go build -ldflags "-X main.version=9.9.9" ./cmd/screencaster` then `--version` prints 9.9.9, without the flag prints `dev`. — make test vet lint green, ldflags 9.9.9 → `screencaster version 9.9.9`, default `dev`
- [x] CP2 — Image: steps 2-3; `make image VERSION=9.9.9 && make image-check` green, `docker run --rm screencaster:piper screencaster --version` prints 9.9.9; `make e2e-runtime` green. — PASS: image-check ok, `screencaster:piper --version` = 9.9.9, e2e-runtime green
- [ ] CP3 — Workflow written: step 5; YAML valid (`actionlint` run in a container, or GitHub accepts it on push), dispatch form visible in the Actions tab. — actionlint clean, build/tar/SHA256SUMS loop run locally; dispatch form still to see after push
- [x] CP4 — Native prerequisites verified: step 7; fixture demo renders from the downloaded binary in a bare container; README list matches what was needed. — bare debian:bookworm-slim amd64 render OK (h264+aac, 1920x1080, 30 fps); README Install lists exactly what that run needed. arm64 untested
- [ ] CP5 — Trial release: step 9 with `0.0.1`; Release has 2 tarballs + SHA256SUMS, `sha256sum -c` passes, anonymous `docker pull` works, trial artifacts removed.
- [ ] CP6 — Docs + real release: steps 6 and 8; ARCHITECTURE and README updated, `v1.0.0` released.

## Risks & Mitigations
- GHCR package is private on first push; anonymous pull fails. The repo is public (checked via the GitHub API), so Release assets download anonymously. Mitigation: after the first push set the package to public in its GitHub settings (manual, one time); CP5 tests an anonymous pull.
- `v1.0.0` tag points at an older commit; releasing from it ships no version injection. Mitigation: the guard step fails loudly; you move the tag (step 6).
- Release is dispatched from a commit CI has not passed. Mitigation: dispatch from a green `main`; the release job runs `make image-check` but not lint, tests or e2e.
- Native binaries unusable without the provider env vars, Piper, ffmpeg and Chromium (and possibly Go to fetch the playwright driver). Mitigation: README states prerequisites verified in step 7; Docker is documented as the supported path.
- Image push succeeds but release creation fails, leaving an image without a Release. Mitigation: re-running the workflow overwrites the image tags and then creates the release; the guard allows an existing tag at the same commit.
- Piper is pinned x86_64, so the image is amd64 only and Apple silicon or arm64 Docker hosts run it under emulation. Mitigation: documented in README; revisit if asked for.
- Image build in Actions is slow and uncached (Chromium install, Piper and voice downloads). Mitigation: accepted; manual dispatch only, same cost as CI's `image` job.

## Test Strategy
- Unit: `--version` output in `cmd/screencaster` (fake `renderFunc`, no provider).
- Integration: `make image VERSION=9.9.9` + `make image-check`; `docker run ... --version`.
- E2E: `make e2e-runtime` still passes against the image built with the new Dockerfile (pipeline unchanged).
- Manual: trial release `0.0.1` (CP5), bare-container run of the downloaded binary (CP4), anonymous `docker pull`.

## Success Checklist
- [ ] All success criteria met, with evidence from the trial release
- [ ] `golangci-lint`, `go vet -tags e2e`, `go test -race` clean (`make lint vet test`)
- [ ] `make e2e-runtime` green with the new Dockerfile
- [ ] Code review approved
- [ ] ARCHITECTURE §12, §13, §16 and README updated
- [ ] No regressions in existing demo scripts, `ci.yml` unchanged and green

## Timeline & Estimates
- Implementation (steps 1-5, 8): ~2 h
- Verification (steps 7, 9, trial release): ~1.5 h
- Review + polish: ~0.5 h
- **Total**: ~4 h, plus waiting on the first image build in Actions.

## Open Questions
- [ ] If step 7 shows a non-Go user cannot get the playwright driver, does the tarball ship the `playwright` CLI too (one more `go build github.com/mxschmitt/playwright-go/cmd/playwright`, as the root `Dockerfile` does) or does the README say `go install`? Default: README only; revisit after step 7.

Closed in this refresh: repo is public; Piper 2023.11.14-2 ships `piper_linux_aarch64.tar.gz` (still verify it runs in step 7).

## Implementation Log
- 2026-10-07 plan refreshed against current code (one module, `providers/` layer, Decision 76). Decision: CLI version via cobra `--version` flag only.
- Step 1 — started
- Step 1 — done — files: cmd/screencaster-mcp/main.go, cmd/screencaster/main.go, cmd/screencaster/render.go — `version` is a var "dev" in both mains, cobra root has `Version: version`; built in the dev image, `-X main.version=9.9.9` prints `screencaster version 9.9.9`, default `dev`. Host has no Go, all checks run through the dev image
- Step 2 — started
- Step 2 — done — files: Dockerfile — `ARG VERSION=dev` just before the build RUN (keeps the go.mod/COPY layers cached), `-ldflags "-X main.version=${VERSION}"` on the `./cmd/...` line only; `docker build --target build --build-arg VERSION=9.9.9` gives a binary printing 9.9.9
- Step 3 — started
- Step 3 — done — files: Makefile — `VERSION ?= dev` after `PROVIDER_DIR`, `image-base` passes `--build-arg VERSION=$(VERSION)`; `make image-base VERSION=9.9.9` gives an image whose `screencaster --version` prints 9.9.9 (full `make image` + e2e-runtime left to CP2 close)
- Step 4 — started
- Step 4 — done — files: cmd/screencaster/render_test.go — `TestRun_versionFlagPrintsBuildVersion`: `--version` prints the `version` var on stderr, exit 0, no render call, stdout empty, works with `renderWith(noEnv)` (no provider); vet and golangci-lint clean on ./cmd/...
- Step 5 — started
- Step 5 — done — files: .github/workflows/release.yml — dispatch input `version` checked against X.Y.Z, tag guard via `git ls-remote` (peeled tag preferred), `go-version-file: go.mod`, build loop for amd64+arm64 with tarballs (binaries at root) and SHA256SUMS (bare names), GHCR login, `make image VERSION` + `make image-check`, push `:X.Y.Z` and `:latest`, `gh release create --target $GITHUB_SHA --generate-notes` last. actionlint clean; the build/tar/sha256sum -c loop ran in the dev image (0.0.1, extracted amd64 binary prints 0.0.1). Not tested locally: tag guard, GHCR login/push, gh release (need Actions)
- Step 5 — note — the `sha256sum *.tar.gz` in the line above tripped actionlint's shellcheck (SC2035); changed to `sha256sum -- *.tar.gz` (bare names kept), actionlint now exit 0
- Step 7 — started
- Step 7 — findings — bare `debian:bookworm-slim` (amd64), release-style binaries built with `-X main.version=0.0.1`: (a) no env → `no TTS provider: set SCREENCASTER_TTS (available: piper)`; (b) with `SCREENCASTER_TTS`, `SCREENCASTER_PIPER_BIN`, `SCREENCASTER_PIPER_VOICES` + Piper 2023.11.14-2 + voices (same pinned URLs/sha256 as providers/piper/Dockerfile) + `ffmpeg` (ffprobe comes with it) → fails at `start playwright: please install the driver (v1.62.1) first`; the binary never fetches the driver itself; (c) `playwright install --with-deps chromium` (the Go CLI from `github.com/mxschmitt/playwright-go/cmd/playwright`, builds static with CGO_ENABLED=0) as root installs the driver to `~/.cache/ms-playwright-go/1.62.1` and Chromium to `~/.cache/ms-playwright`; `PLAYWRIGHT_DRIVER_PATH` and `PLAYWRIGHT_BROWSERS_PATH` are NOT needed at default paths; (d) then `screencaster render` of a 4-step narrated fixture script succeeds: h264+aac, 1920x1080, 30 fps, 9.5 s, sync marker ends 1.92 s. Not verified: arm64 (no arm64 host; Piper aarch64 tarball unpack/run untested). ca-certificates/curl were only for the downloads
- Decision (user, 2026-10-07): ship the `playwright` CLI in each tarball (Open Question closed). Workflow step 5 amended to build it per arch from the go.mod version; README (step 8) tells native users to run `./playwright install --with-deps chromium`
- Step 7 — done — no files; evidence above. CP4 render evidence obtained from a CLI built exactly as the amended workflow will (static, from go.mod); the README list is written in step 8
- Step 5 — amended — files: .github/workflows/release.yml — per the step 7 decision each tarball also holds `playwright` (built from the go.mod version, static); actionlint exit 0; the loop re-run in the dev image: `sha256sum -c` OK for both tarballs, each lists screencaster, screencaster-mcp, playwright
- Step 8 — started
- Step 8 — done — files: README.md, docs/ARCHITECTURE.md — README `## Install` above Quick start (Docker pull/run, local-name tag note, amd64-only note, native tarball prerequisites: Piper + voices + 3 env vars, ffmpeg, `./playwright install --with-deps chromium`); ARCHITECTURE §12 (release bullet, `--help`/`--version` wording), §13 (release workflow named, no lint/tests/e2e), §16 (header + Decision 76). `docs/PRD.md` rechecked: only "release notes" as audience/problem text, no distribution rule, no change. CODE_QUALITY unchanged (no new debt: Go version read from go.mod, playwright CLI version from go.mod)
- Stopped here — remaining: step 6 (you move `v1.0.0`) and step 9 (trial release `0.0.1` then real `1.0.0`) need the changes committed, pushed and the workflow dispatched on GitHub; not done from here. CP3 still needs the dispatch form seen in the Actions tab after push
