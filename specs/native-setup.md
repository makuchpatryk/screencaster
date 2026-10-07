# Native setup in one command — Implementation Plan

## Summary
Native users today set three `SCREENCASTER_*` variables, hand-fetch Piper and two voices, install ffmpeg and run a separate `playwright install --with-deps chromium`. This plan replaces that with `sudo ./screencaster setup` after extracting the release tarball. Setup is the one explicit command that downloads; `render` stays offline. A separate `screencaster-docker` script covers Docker users. Why now: the release workflow (specs/release-distribution.md) is ready except for this step, and the README install steps are the main barrier.

## Success Criteria
- `tar xzf screencaster_X.Y.Z_linux_amd64.tar.gz && sudo ./screencaster setup` completes on a bare `debian:bookworm-slim` (amd64), and afterwards `./screencaster render demos/<fixture>.yaml` runs as a non-root user with no `SCREENCASTER_*` variables set.
- `./screencaster setup --check` prints one status line per piece (piper, voices, playwright driver, chromium, ffmpeg) and exits 0 only when all are `ok`; it changes nothing and runs without root.
- A second `setup` run changes nothing when everything is `ok` (no downloads, no Chromium reinstall).
- The release tarball holds `screencaster`, `screencaster-mcp`, `screencaster-docker` and no `playwright` binary.
- Unit tests make no network calls; the pins in code equal the ARGs in `providers/piper/Dockerfile` (test).

## Scope & Constraints
- In scope: `setup` subcommand (with `--check`); pins in Go; download, sha256 verify and tar.gz extraction; Playwright driver and Chromium install through playwright-go's Go API; ffmpeg check and apt install; compiled-in install dir `/opt/screencaster` (override `SCREENCASTER_HOME`); TTS defaults to piper at that dir; `screencaster-docker` wrapper script shipped in the tarball; release workflow changes; README Install; ARCHITECTURE and CODE_QUALITY updates.
- Out of scope: macOS, Windows, aarch64 Piper (setup refuses non-amd64 up front), package managers, Docker image changes (the Dockerfiles keep their own install layers), auto-discovery of config files, shell-profile edits, a multi-arch image.
- Hard constraints:
  - `render` makes no network call and does not change its output (BR-001, NFR-003). Only `setup` downloads.
  - Import boundaries (ARCHITECTURE §3): `domain` untouched; `app/setup` imports no tool package; each tool sits behind an adapter; only adapters start subprocesses or call playwright-go or make HTTP calls, and the list is extended below.
  - Piper pins stay sha256-checked; no unpinned download.
- Trade-offs:
  - Use case + ports (like `renderer`) over a single setup package: more types, but the steps are unit-tested with fakes and `wire` stays the one composition root.
  - Playwright installed by the library, not the shipped CLI: one binary less in the tarball. Costs a Node.js and npm download at setup time (see Risks).
  - Install dir `/opt/screencaster`, world-readable after setup, so any user can render. Chosen over `~/.cache` so the install is not tied to one user.
  - Docker wrapper is a separate script, not a subcommand: a Docker user can take just that file and does not need the binary to exist first.

## Architecture & Design

### High-Level Flow
```
sudo ./screencaster setup
  -> wire.InstallDir()                      SCREENCASTER_HOME or /opt/screencaster
  -> app/setup.Run(Options{Dir})
       refuse if not root, or not amd64 (before any work)
       Check each piece (file present + sha256 / version marker):
         piper      <dir>/piper/piper                          download.Fetch + Extract (pinned sha256)
         voices     <dir>/piper/voices/{en_US-ryan-high,pl_PL-darkman-medium}.{onnx,onnx.json}
                                                                download.Fetch (pinned sha256 each)
         driver     <dir>/playwright-driver/{node,package}     browser.Install (WithDeps off here)
         chromium   <dir>/ms-playwright/chromium-*             browser.Install(Browsers: chromium, WithDeps: apt present)
         ffmpeg     PATH: ffmpeg + ffprobe                     apt.InstallFFmpeg when root and apt present, else error with the command
       write <dir>/setup.json marker after each piece succeeds (resume after a failure)
       chmod -R a+rX <dir>
  -> print the report, exit 0 / 1

./screencaster render ...
  wire.TTS: SCREENCASTER_TTS unset -> piper at <dir>/piper/piper and <dir>/piper/voices
            env overrides SCREENCASTER_PIPER_BIN / _VOICES still win
  wire.NewDeps: browser.Launcher{DriverDir: <dir>/playwright-driver};
                PLAYWRIGHT_BROWSERS_PATH = <dir>/ms-playwright if unset

screencaster-docker render demos/x.yaml   (separate script, shipped in the tarball)
  -> docker run --rm --init --shm-size=1g --add-host=host.docker.internal:host-gateway \
       -v "$PWD:/work" -w /work --entrypoint screencaster ghcr.io/makuchpatryk/screencaster:<VERSION> "$@"
```

Install dir layout: `piper/` (the Piper tarball's own top folder, as the Dockerfile extracts it to `/opt/piper`), `piper/voices/`, `playwright-driver/`, `ms-playwright/`, `setup.json`.

### Key Changes

- **`internal/app/setup/pins.go` (new)**: constants copied from `providers/piper/Dockerfile`: `PiperVersion`, `PiperSHA256`, `VoicesRev`, and per voice the `.onnx` and `.onnx.json` names with sha256. URL builders for GitHub (Piper) and Hugging Face (voices at `VoicesRev`).
- **`internal/app/setup/setup.go` (new)**:
  - Ports (declared here, implemented by adapters): `Fetcher { Fetch(ctx, url, dst, sha256) error; Extract(ctx, tgz, dstDir) error }`, `Browser { Install(ctx, driverDir, browsersDir string, withDeps bool, out io.Writer) error }`, `Packages { HasApt() bool; InstallFFmpeg(ctx, out io.Writer) error }`, `Files` (the same port shape `renderer` uses: read, write, stat, remove), `Host { IsRoot() bool; Arch() string; HasOnPath(name string) bool }`.
  - `type Status string` with `StatusOK`, `StatusMissing`, `StatusBad`.
  - `Check(ctx, deps, dir) (Report, error)`: piece by piece, read-only.
  - `Run(ctx, deps, dir, out io.Writer) (Report, error)`: runs `Check`, then installs each piece that is not `ok`, in order piper, voices, driver, chromium, ffmpeg; writes the marker after each success; returns the final report.
  - Error text names the piece and the URL or command, e.g. `download piper: sha256 mismatch: want …, got …`.
- **`internal/app/setup/setup_test.go` (new)**: fakes for all ports, no network. See Test Strategy.
- **`internal/app/setup/pins_test.go` (new)**: parses the `ARG PIPER_VERSION=`, `PIPER_SHA256=`, `VOICES_REV=` and the four voice sha256 lines in `providers/piper/Dockerfile` and asserts they equal the Go pins. This is the guard for the duplication (CODE_QUALITY known debt).
- **`internal/adapters/download/download.go` (new)**: the only HTTP client in the code. `Fetch`: `http.Client` with a timeout, streams to `dst + ".part"`, verifies sha256, renames. `Extract`: tar.gz into a directory; rejects absolute paths and `..` entries.
- **`internal/adapters/browser/install.go` (new, same package as `browser.go`)**: `Install` wraps `playwright.Install(&playwright.RunOptions{DriverDirectory, Browsers: []string{"chromium"}, WithDeps, Stdout, Stderr})`. Verified in playwright-go v0.6201.1 `run.go:352-395`: these fields exist, and `WithDeps` maps to `--with-deps`. Keeps the rule that only `adapters/browser` calls playwright-go.
- **`internal/adapters/apt/apt.go` (new)**: `HasApt` = `apt-get` on PATH. `InstallFFmpeg`: `apt-get update`, then `DEBIAN_FRONTEND=noninteractive apt-get install -y ffmpeg`, output to `out`. Starts subprocesses, so ARCHITECTURE §3 gets this adapter added to the list.
- **`internal/app/wire/setup.go` (new)**: `InstallDir()` (env `SCREENCASTER_HOME`, else `/opt/screencaster`); `Setup(out) (setup.Deps, error)` maps the ports to the adapters; `PiperPaths(dir)`.
- **`internal/app/wire/app.go`**:
  - `TTS(getenv, workDir)`: unset `SCREENCASTER_TTS` now selects `piper` with the install-dir defaults. Unknown names still error. Env `SCREENCASTER_PIPER_BIN` and `_VOICES` still override.
  - `NewDeps`: `browser.Launcher{DriverDir: <dir>/playwright-driver}`; sets `PLAYWRIGHT_BROWSERS_PATH` to `<dir>/ms-playwright` when unset (the driver reads it from its environment, so it must be set before the first `Run`).
- **`internal/app/wire/tts_test.go`**: the test "unset errors" becomes "unset defaults to piper at the install dir"; add "env override wins".
- **`cmd/screencaster/setup.go` (new)**: `setupCmd(stdout, stderr, deps)`: flag `--check`; prints `piper: ok`, `voices: missing (pl_PL-darkman-medium.onnx)`, and so on, one line per piece; exit 1 if not all `ok`. Without `--check`: requires root (error before any work), then `setup.Run`.
- **`cmd/screencaster/render.go`**: `run()` adds `setupCmd` next to `renderCmd`. Nothing else changes.
- **`scripts/screencaster-docker.sh` (new)**: a POSIX sh script, `@VERSION@` placeholder. Runs `docker run` with the flags above. Passes `"$@"` through.
- **`cmd/screencaster/docker_script_test.go` (new)**: reads the template, substitutes a test version, checks the flags, the image tag and `--entrypoint screencaster`.
- **`.github/workflows/release.yml`**:
  - Drop the `go build .../playwright` line and `playwright` from the tar list.
  - Generate `dist/screencaster-docker` with `sed "s/@VERSION@/$VERSION/" scripts/screencaster-docker.sh`, `chmod +x`.
  - Tarball contents: `screencaster screencaster-mcp screencaster-docker`. `SHA256SUMS` unchanged in form.
- **`Dockerfile`, `providers/piper/Dockerfile`, `Makefile`**: unchanged. The Docker build still runs `playwright install` itself (`Dockerfile:34`, `:77`) and keeps its Piper fetch stage.
- **Data model / schema / MCP tools**: none. The MCP server gets the same TTS default through `wire`, so `screencaster-mcp` also works after setup without env vars.

### Fit with Project Docs
- **ARCHITECTURE.md §1, NFR-003 driver row** ("No HTTP clients in code. Chromium is the only network user"): bent. The HTTP client is in `adapters/download` and is reachable only from `setup`. Amend the row: render has no network other than Chromium; `setup` is the explicit download command.
- **ARCHITECTURE.md §3 dependency rules**: add `adapters/download` (HTTP), `adapters/apt` (subprocess) and `adapters/browser`'s `Install` to the lists of who makes HTTP calls, starts subprocesses and calls playwright-go. `app/setup` is an app use case and imports ports only. `cmd/screencaster` still imports neither the MCP server nor the job store.
- **ARCHITECTURE.md §12**: the release bullet changes (no `playwright` CLI in the tarball; `screencaster-docker` added; native needs only network, Debian/Ubuntu for ffmpeg and deps). §14: list the hosts `setup` contacts.
- **ARCHITECTURE.md §13**: add the setup tests (unit, fake ports; manual bare-container run, CP6).
- **ARCHITECTURE.md §16**: Decision 77: one `setup` command, compiled-in install dir with `SCREENCASTER_HOME`, TTS defaults to piper, Playwright via the Go API, Docker wrapper as a separate script. Alternatives: shell script `install.sh`; `pins.env` read by scripts; `~/.cache` install; a `docker-install` subcommand.
- **CODE_QUALITY.md**: DRY: pins live in two places (Dockerfile ARGs, Go pins), guarded by `pins_test.go`. Record it in Known debt until the Dockerfile reads the Go pins. KISS: no installer script, no package manager beyond apt. YAGNI: no arm64, no shell-profile edits, no distro detection beyond apt presence.
- **README.md**: replace the native section. Steps: extract, `sudo ./screencaster setup`, `./screencaster setup --check`, render. Name the hosts. Note the Debian/Ubuntu requirement for automatic ffmpeg and Chromium deps. Keep the Docker section; add `screencaster-docker` as the zero-setup path.
- **PRD**: recheck the NFR-003 wording for a "no installs" rule; amend only if it states one.

### Alternative Approaches Considered
- **Shell `install.sh` with `pins.env`** (earlier idea): fewer Go lines, but the logic (download, verify, marker, `--check`) would be untested and would duplicate the Go side. Not chosen.
- **Single adapter package called from `cmd`** (Track B): fewer types. Bypasses `wire`, and the steps are not testable without the real network. Not chosen (user decision).
- **Install into `~/.cache` as root with `SUDO_USER`**: works, but ties the install to one user and needs a home-directory lookup. Not chosen.
- **Ship the `playwright` CLI and shell out to it**: keeps one proven path, but adds a binary to the tarball and makes setup depend on the CLI being present. Not chosen; the Go API is verified to cover the same options.
- **Docker wrapper as `screencaster docker-install`**: rejected by user; a separate script means Docker users need no native binary to exist.

## Implementation Steps
1. [x] `internal/app/setup/pins.go`: Piper version, tarball sha256, voices revision, four voice sha256 values, URL builders. Values copied from `providers/piper/Dockerfile`.
2. [x] `internal/app/setup/setup.go`: `Status`, `Report`, ports (`Fetcher`, `Browser`, `Packages`, `Files`, `Host`), `Options`, `Check`, `Run`, marker read/write (`setup.json`), arch and root refusal.
3. [x] `internal/app/setup/setup_test.go`: fake ports. Cases: all ok means no downloads and no installs; piper missing fetches the pinned URL and sha; a bad voice refetches only that voice; a bad marker with present files is re-verified; ffmpeg missing and root with apt calls apt; ffmpeg missing without apt returns an error naming the command; non-root without `--check` refused before any work; non-amd64 refused before any work; a failed piece stops the run and keeps earlier markers.
4. [x] `internal/app/setup/pins_test.go`: parse `providers/piper/Dockerfile` ARGs; assert equal to the Go pins.
5. [x] `internal/adapters/download/download.go` and `download_test.go`: `Fetch` (httptest server; good sha renames into place; bad sha leaves no file; `.part` removed on error) and `Extract` (tar.gz with `piper/` top folder; reject `../x` and absolute paths).
6. [x] `internal/adapters/browser/install.go`: `Install` over `playwright.Install`. No unit test (network); checked in CP5. Add a doc comment naming the RunOptions fields used and the env vars it reads (`PLAYWRIGHT_NODEJS_PATH`, `NODE_MIRROR`, `PLAYWRIGHT_GO_NPM_REGISTRY`).
7. [x] `internal/adapters/apt/apt.go`: `HasApt` and `InstallFFmpeg` (the command runner is a package var, so a unit test checks the argument list).
8. [x] `internal/app/wire/setup.go`: `InstallDir`, `PiperPaths`, `Setup(out)` mapping ports to adapters.
9. [x] `internal/app/wire/app.go` and `tts_test.go`: TTS default to piper at the install dir; env override wins; unknown name still errors. `NewDeps` sets the driver dir and `PLAYWRIGHT_BROWSERS_PATH` when unset.
10. [x] `cmd/screencaster/setup.go` and `render.go`: `setup` subcommand with `--check`, per-piece report lines, root check, exit codes. Tests: `--check` exit 1 with an empty dir and a fake Host; `setup` without root exits 1 before any work.
11. [x] `scripts/screencaster-docker.sh` and `cmd/screencaster/docker_script_test.go`: template, flags, `--entrypoint screencaster`, image tag placeholder substitution.
12. [x] `.github/workflows/release.yml`: drop the playwright CLI build; generate `screencaster-docker` from the template; tarball contents updated. Run `actionlint` in the dev image.
13. [x] Local dry run of the tarball step in the dev image (as the release-distribution plan did): tarball lists `screencaster screencaster-mcp screencaster-docker`; `sha256sum -c` passes.
14. [x] Docs: README native section and Docker note; ARCHITECTURE §1, §3, §12, §13, §14, §16 (Decision 77); CODE_QUALITY known debt; PRD NFR-003 recheck.
15. [x] `make test vet lint` green in the dev image; `make e2e-runtime` still green (the runtime image is unchanged).

## Checkpoints (Todo List)
- [x] CP1 — Pins and use case: steps 1-4; `go test ./internal/app/setup/...` green, no network in tests. — 22 tests, race clean, lint clean
- [x] CP2 — Adapters: steps 5-7; `go test ./internal/adapters/download/... ./internal/adapters/apt/...` green; `go vet` and `golangci-lint` clean for the adapters. — tests race clean, vet + golangci-lint clean
- [x] CP3 — Wiring and defaults: steps 8-9; `wire` tests green: unset TTS gives piper at `/opt/screencaster`, the env override wins, and the driver dir and browsers path are set. — wire, osfs, host, cmd tests race clean; lint clean
- [x] CP4 — CLI and wrapper: steps 10-11; `./screencaster setup --help` works; `setup --check` on an empty dir prints five `missing`/`bad` lines and exits 1; the script test is green. — real binary in dev image: setup --help ok; --check on an empty dir prints five lines (ffmpeg ok there, the dev image has it) and exits 1; non-root setup exits 1 before any work; cmd tests race clean
- [x] CP5 — Release workflow: steps 12-13; `actionlint` clean; local tarball dry run lists the three binaries and `sha256sum -c` passes. — actionlint (rhysd/actionlint container) exit 0; dry run 0.0.1: both tarballs list screencaster screencaster-mcp screencaster-docker, sha256sum -c OK x3, extracted amd64 prints "screencaster version 0.0.1"
- [ ] CP6 — Real native run: on a bare `debian:bookworm-slim` (amd64) with network: extract the tarball, `sudo ./screencaster setup` completes, `setup --check` all `ok`, a second `setup` changes nothing, and a non-root user renders the fixture demo with no `SCREENCASTER_*` variables. Evidence recorded in the log; `ls -l` of `ms-playwright` shows it readable by the render user.
- [x] CP7 — Docs and final verification: step 14-15; `make test vet lint` and `make e2e-runtime` green; ARCHITECTURE §16 Decision 77 added. — make test vet lint green (re-run after the env-pins change), e2e-runtime green (7 tests, run before that change; render path untouched), Decision 77 in ARCHITECTURE §16

## Risks & Mitigations
- **Setup needs five hosts: github.com (Piper), huggingface.co (voices), nodejs.org (Node for the driver), registry.npmjs.org (driver package), and the Playwright CDN (Chromium).** A proxy or firewall fails one piece.
  - Mitigation: `--check` and the error name the piece and the URL; the README lists the hosts.
  - Mitigation: the marker means a rerun resumes at the failed piece.
- **Pins in two places drift.** Mitigation: `pins_test.go` fails the build when they differ (CP1).
- **`PLAYWRIGHT_BROWSERS_PATH` must reach the driver process.** If it is set after the first `Run`, or only in one entry point, Chromium is looked up in `~/.cache` and render fails with a missing-browser error. Mitigation: `NewDeps` sets it before any playwright call; CP6 renders as a non-root user with no variables, which proves the path. Unverified until CP6: whether `playwright.Install` honours the same env var during setup. CP6 checks that too.
- **Install dir readable only after `chmod`.** Mitigation: `chmod -R a+rX` at the end of `Run`; CP6 renders as a non-root user.
- **Chromium system libraries on non-Debian distros.** `WithDeps` is apt-only. Mitigation: `WithDeps` only when apt is present; otherwise `--check` reports the chromium piece with the note "system libraries not checked on this distro" and README says what to install. Not verified on other distros.
- **Upstream change breaks a pin** (Piper release or voice revision removed). Mitigation: sha256 mismatch fails loudly with the expected and actual hash; fix is a pin update in one place plus the Dockerfile ARG (parity test catches a miss).
- **Real setup is slow and large** (Node, npm packages, Chromium, several hundred MB). Mitigation: not in per-push CI; run manually in CP6 and before a release.
- **Marker says ok but a file was deleted.** Mitigation: `Check` verifies files and sha256 of the Piper tarball pin and each voice; the marker only records what was installed.

## Test Strategy
- **Unit (`go test`, no network):**
  - `app/setup`: fakes for `Fetcher`, `Browser`, `Packages`, `Files`, `Host`. Covers every Check and Run branch listed in step 3.
  - `download`: httptest server for sha match and mismatch; tar traversal rejection.
  - `apt`: argument list via the runner var.
  - `wire`: TTS default and override; driver dir and browsers path.
  - `cmd/screencaster`: `setup --check` exit codes; root refusal; script template content.
  - `pins_test`: Go pins equal the Dockerfile ARGs.
- **Integration:** the tarball dry run in the dev image (CP5). `make e2e-runtime` unchanged.
- **E2E (manual, CP6):** bare `debian:bookworm-slim` amd64 with network: extract, `sudo setup`, `setup --check`, second setup is a no-op, non-root render of the fixture demo with no env vars, ffprobe the output.
- **Not tested:** arm64 (refused up front); non-Debian distros (documented, unverified).

## Success Checklist
- [ ] All success criteria met, with evidence from CP6
- [ ] `golangci-lint`, `go vet -tags e2e`, `go test -race` clean (`make lint vet test`)
- [ ] `make e2e-runtime` green (runtime image unchanged)
- [ ] `actionlint` clean; tarball lists the three binaries
- [ ] Code review approved
- [ ] ARCHITECTURE §1, §3, §12, §13, §14, §16 and README updated; CODE_QUALITY known debt recorded
- [ ] No change to render output for existing demos

## Timeline & Estimates
- Implementation (steps 1-12): ~6 h
- Verification (step 13, CP6 on a bare container with real downloads): ~2 h
- Docs and review (steps 14-15): ~1.5 h
- **Total**: ~9–10 h, plus download time for CP6

## Open Questions
- [ ] None blocking. Verified during planning: `playwright.Install` and `RunOptions` fields exist in v0.6201.1; the image has no `ENTRYPOINT` (compose sets it), so the wrapper passes `--entrypoint screencaster`. To confirm in CP6: that `PLAYWRIGHT_BROWSERS_PATH` is honoured by the setup-time install.

## Implementation Log
(Empty. screencaster-implement appends one line per step event here, so work can resume in a new session. When running via screencaster-flow, decisions from `AskUserQuestion` answers are also recorded here as `- Decision: <question>: <answer>`)
- Decision: default install dir: /opt/screencaster
- Decision: binaries find the install dir: compiled-in default, env override
- Decision: Docker wrapper in this plan: yes
- Decision: Dockerfiles keep their own Piper and Playwright layers
- Decision: ffmpeg missing: apt install when root, else print the command
- Decision: pins compiled into the binary
- Decision: rerun: verify each piece, skip if good, repair if missing or bad
- Decision: --check covers all pieces incl. ffmpeg
- Decision: non-Debian: download what it can, print the rest
- Decision: Docker wrapper form: separate executable screencaster-docker (shipped in tarball)
- Decision: SCREENCASTER_TTS unset: default to piper at the install dir
- Decision: setup code layering: use case + ports (app/setup)
- Step 1 — started
- Step 1 — done — files: internal/app/setup/pins.go — pins, VoiceFiles, PiperURL, VoiceURL
- Step 2 — started
- Step 2 — done — files: internal/app/setup/setup.go — ports, Report, Check, Run, marker, layout funcs (PiperBin, VoicesDir, DriverDir, BrowsersDir) exported so wire reuses them. Deviations: Check/Run take `Options{Dir, Version, Out}`; one `Browser.Install` call serves the driver and chromium rows (playwright.Install does both); marker tags driver/chromium with the release version; piper "ok" = binary present + marker has pinned version/sha (tarball is not kept); Files port also has Open, ReadDir, WriteFile, MakeReadable (osfs gets WriteFile/MakeReadable in step 8)
- Step 3 — started
- Step 3 — done — files: internal/app/setup/setup_test.go — in-memory fakes for all ports, 20 tests incl. all branches listed in the step; test voices swapped in for the 4 real pins via useTestVoices; race clean
- Step 4 — started
- Step 4 — done — files: internal/app/setup/pins_test.go — ARGs, four voice shas, download paths and Piper URL checked against providers/piper/Dockerfile
- CP1 ticked: go test -race + golangci-lint on internal/app/setup pass (ran directly, foreground, in the dev image)
- Step 5 — started
- Step 5 — done — files: internal/adapters/download/download.go, download_test.go — Downloader{Fetch, Extract}, ErrChecksum; Extract handles dir/file/relative symlink (the pinned Piper tarball has exactly those, 5 symlinks, inspected), rejects the rest; 10 tests, race clean
- Step 6 — started
- Step 6 — done — files: internal/adapters/browser/install.go — `Installer.Install(ctx, driverDir, browsersDir, withDeps, out)` over playwright.Install; sets PLAYWRIGHT_BROWSERS_PATH to browsersDir for the call (restored after); Stdout/Stderr/Logger go to out; ctx only checked before start (playwright.Install takes none). No unit test, CP6 checks it
- Step 7 — started
- Step 7 — done — files: internal/adapters/apt/apt.go, apt_test.go, .golangci.yml — `Apt{HasApt, InstallFFmpeg}`, runner is a package var; depguard exec-only rule now also allows adapters/apt; 3 tests
- CP2 ticked: go test -race (apt, download), go vet and golangci-lint (apt, download, browser) pass, run in the dev image
- Step 8 — started
- Step 8 — done — files: internal/app/wire/setup.go, setup_test.go, internal/adapters/host/host.go, host_test.go, internal/adapters/osfs/osfs.go, osfs_test.go, .golangci.yml, internal/app/wire/app.go, adapters.go — `InstallDir(getenv)`, `PiperPaths`, `Setup()`; deviations: (a) Setup() takes no `out` and returns no error (out is `setup.Options.Out`); (b) Host needs LookPath, so new `adapters/host` (os/exec allowed there in depguard; ARCHITECTURE §3 to list it in step 14); (c) osfs.FS gained WriteFile and MakeReadable (a+rX); (d) the driver dir and PLAYWRIGHT_BROWSERS_PATH are set in `wire.launcher()` at each launch, not in NewDeps, so MCP explore and cards are covered too; both yield to PLAYWRIGHT_DRIVER_PATH / PLAYWRIGHT_BROWSERS_PATH when the environment sets them (the Docker images do)
- Step 9 — started
- Step 9 — done — files: internal/app/wire/app.go, tts_test.go, cmd/screencaster/main_test.go — `wire.TTS`: unset or `piper` selects piper at `<InstallDir>/piper`, env overrides win, unknown still errors; a missing default binary says "(run: sudo screencaster setup)". Driver dir and browsers path were done in step 8 (`launcher()`). main_test's "no provider" case now expects the setup hint (unset no longer errors). Docs to fix in step 14: ARCHITECTURE §12 "Provider selection" says unset is an error
- CP3 ticked: go test -race for wire, osfs, host, cmd/...; golangci-lint for internal/... and cmd/... clean
- Step 10 — started
- Step 10 — done — files: cmd/screencaster/setup.go, setup_test.go, render.go, main.go, render_test.go, main_test.go — `setupCmd(cfg, stdout, stderr)` with `--check`; report lines on stdout, progress on stderr, incomplete -> exit 1 with "setup is incomplete: run `sudo ./screencaster setup`". Deviation: `run()` takes a `setupConfig{Deps, Dir, Version}` (existing render tests pass `setupConfig{}`). 6 tests over fakes + real osfs in a temp dir
- Step 11 — started
- Step 11 — done — files: scripts/screencaster-docker.sh, cmd/screencaster/docker_script_test.go — POSIX sh template with one `@VERSION@` (image tag), flags checked by 3 tests; also ran it once against a fake `docker` on PATH to see the real argv
- CP4 ticked: see checkpoint note
- Step 12 — started
- Step 12 — done — files: .github/workflows/release.yml — no playwright CLI build or tar entry; `dist/screencaster-docker` made from the template with sed and copied into each arch's tarball; also attached to the Release and listed in SHA256SUMS (addition beyond the plan: a Docker user needs only that file). Both arch tarballs kept (arm64 binaries exist, setup refuses on arm64). actionlint clean
- Step 13 — started
- Step 13 — done — no repo files (ran the workflow's build step verbatim, extracted from release.yml, in the dev image on a scratch copy with VERSION=0.0.1): tarball contents, `sha256sum -c` and `--version` as in CP5
- CP5 ticked: see checkpoint note
- Step 14 — started
- Step 14 — done — files: README.md, docs/ARCHITECTURE.md (§1, §3 tree and dependency rules, §12 provider selection + native install + release bullets, §13, §14, §16 intro + Decision 76 amended + Decision 77), docs/CODE_QUALITY.md (KISS, YAGNI, DRY rows, Separation table and rules, determinism, Known debt: pins in two places), docs/PRD.md (NFR-003 verification wording; the PRD says "no HTTP clients besides Chromium", amended). README: native section replaced (extract, setup, --check, hosts, apt-only note, amd64-only), Docker section gets `screencaster-docker`, TTS bullet updated. The PRD decisions log was left alone (77 is architecture-only, like 76)
- Step 15 — started
- Decision: pin values are not in Go (user request, twice): they live in an embedded `internal/app/setup/pins.env`; `--pins-file` files and `SCREENCASTER_*` env variables override it, later layer wins. Supersedes "pins compiled into the binary" and the first env-override cut (compiled defaults)
- Change — files: internal/app/setup/pins.go (now `Pins`, `DefaultPins`, `PinsFromEnv`; env: SCREENCASTER_PIPER_VERSION/_SHA256/_URL_BASE, SCREENCASTER_VOICES_REV/_URL_BASE, SCREENCASTER_VOICE_FILES = `name=path=sha256;...`), setup.go (`Options.Pins`), setup_test.go, pins_test.go (+3 tests: defaults, full override, bad values), cmd/screencaster/setup.go, main.go, setup_test.go (+2 tests), README.md, docs/ARCHITECTURE.md, docs/CODE_QUALITY.md. Package-level pin consts and `VoiceFiles` var are gone. Version override requires a sha256; bad values exit 1 before any work. e2e-runtime was started before this change (render path untouched)
- Step 15 — done — `make test vet lint` green after the env-pins change; `make e2e-runtime` green (TestRender_cli*, TestScreenshot_*, TestShots_cli), run before that change
- CP7 ticked
- CP6 not run: needs the real downloads on a bare debian:bookworm-slim; waiting for the user's go-ahead
- Change 2 — files: internal/app/setup/pins.env (new, embedded), pins.go (`LoadPins(getenv, fileTexts...)`, no pin value left in Go; `DefaultPins` removed), pins_test.go, setup_test.go, cmd/screencaster/setup.go (`--pins-file`, repeatable), setup_test.go (+2 tests), README.md, docs/ARCHITECTURE.md, docs/CODE_QUALITY.md. Layers: pins.env < --pins-file < environment; unknown key and version-without-sha256 are errors. Tests, vet and lint green
