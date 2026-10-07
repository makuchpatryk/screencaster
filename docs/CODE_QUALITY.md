# Code quality

The rules screencaster is written and reviewed against. A checklist for authors and reviewers, not a scoring system. When two rules pull in different directions, pick the simpler code and say why in the PR.

Precedence: **KISS and YAGNI first** (internal tool, one developer, ≤ 10 videos/month, PRD §9), then the rest. What the product does: [PRD](./PRD.md). How it is structured: [ARCHITECTURE.md](./ARCHITECTURE.md).

| Rule | One line |
|---|---|
| [KISS](#kiss) | Simplest thing that works |
| [YAGNI](#yagni) | Build only what the PRD asks for |
| [DRY](#dry) | One source of truth per piece of knowledge |
| [Separation of concerns](#separation-of-concerns) | Rules, tool wrappers, orchestration and entry points live apart |
| [SOLID](#solid) | Small units with seams where tests need them |
| [Law of Demeter](#law-of-demeter) | Pass flat values, don't reach through structs |
| [Composition over inheritance](#composition-over-inheritance) | Hold collaborators as fields, don't embed to inherit |

## KISS

Prefer the boring solution. Add a dependency or abstraction only when the plain version has become a problem.

- Standard library and OS first: `flock` for the render lock, `os.Rename` to publish, `go:embed` for the schema, `encoding/binary` for the WAV header, `os/exec` for Piper and ffmpeg. The TTS provider is a static `switch` in `wire.TTS`, no registry or plugin loading. `screencaster setup` is the same: a fixed list of five pieces and `apt-get`, no installer script, package manager abstraction or distro detection beyond "apt is on PATH" (decision 77).
- Short, single-purpose functions with early returns. Handle the error, then continue on the happy path.
- Plain structs and slices. No ORM, DI container, plugin system or job framework. `database/sql` with a few queries is enough (ARCHITECTURE §10).
- **Deviation:** `cobra` for the CLI (Decision 53, user choice). It is the only framework allowed; std `flag` would have been enough for one command.
- One long-lived goroutine in the MCP server: the queue worker (ARCHITECTURE §6.2). Everything else is a plain call.

## YAGNI

Build for the PRD, not for what might come.

- Delete code with no caller: unused functions, struct fields, config keys, schema properties.
- Keep PRD §10.3 out: login steps, masking, other formats, cloud TTS, web UI, auto-deletion, CI-triggered renders, schema-only tool. PNG screenshots are the one format in (decision 72): no JPEG, other sizes, device scale or several annotations per shot.
- Keep PRD §10.2 out: slides between steps, overlays in videos, multi-tenancy (the start and end cards are in, decision 63; overlays are in for screenshots only, decision 74, with fixed colours and placement and no styling options). No hooks "for later" beyond what ARCHITECTURE §15 already names.
- No second TTS engine (the `tts.Engine` seam is ready for one), browser, storage backend or output format. Only languages are open-ended (BR-002).
- `Recorder`, `Shooter`, `Synthesizer`, `Assembler`, `Cards` and `Files` (in `app/renderer`), `Store` (in `app/jobs`) and `Page`/`Session` (in `domain/executor`/`domain/recorder`/`domain/shooter`) exist as test seams, not for swapping Chromium, ffmpeg, SQLite or the disk. No interface without a test seam. **Deviation:** `tts.Engine` is also a deliberate swap seam for the TTS engine (Decision 65, user choice); it is two methods, with a static switch to pick the adapter.
- `setup` supports linux amd64 only and edits no shell profile or config file; no arm64 Piper, macOS, Windows or package-manager packaging (decision 77).
- No retries, backoff or configurable timeouts. BR-004: one 30 s timeout, then abort. (`setup` has one fixed download timeout and no retry either: a failed piece is fixed by rerunning.)
- No scale design. One worker, one SQLite file, one render at a time (BR-008).

## DRY

Each piece of knowledge has one authoritative place. Two blocks that merely look alike are not duplication.

| Knowledge | Lives in |
|---|---|
| Script format (fields, enums, limits) | `internal/domain/script/script.schema.json`, embedded in `domain/script`. Same text feeds validation and the `render_video` tool description |
| Step format (one action key per step and its shape) | `$defs/step` in the schema; the `explore_page` input schema points at it instead of inferring one from Go types |
| Cross-field script rules (narration per selected language; `screenshot` steps and the video-only fields tied to `type`) | `domain/script`: `Validate`, and `checkType` run by `Parse` so `explore_page` actions are covered too |
| Language selection, override > script > `["en"]` (BR-002) | one function, called by CLI, MCP validation and worker |
| Voice resolution rules (BR-011) | `domain/voices`, over a provider's `Catalog` |
| Built-in voices per language and Piper naming (`.onnx`, language from the name) | `adapters/tts/piper` (`Defaults`) |
| Which TTS provider runs, and its binary and voice paths | `wire.TTS` selects (unset means piper at the install dir); the provider's `providers/<name>/Dockerfile` ENV lines hold the image's paths |
| Native install dir and its layout (`piper/`, `piper/voices/`, `playwright-driver/`, `ms-playwright/`, `setup.json`) | `wire.InstallDir` (the dir) and `app/setup` (`PiperBin`, `VoicesDir`, `DriverDir`, `BrowsersDir`); `setup` writes and render reads through the same functions |
| Piper release and voice pins (version, revision, sha256) | `app/setup/pins.env` (embedded; no pin value is in Go). `LoadPins` layers `--pins-file` files and `SCREENCASTER_*` variables over it; `providers/piper/Dockerfile` ARGs repeat the file (known debt), `pins_test.go` keeps them equal |
| Step semantics (FR-005) and URL resolution against `baseUrl` (BR-010) | `domain/executor` (one type switch), shared by render and `explore_page` |
| Failure shape `{step, lang, action, target, message}` | `domain/failure` (no JSON tags), used by CLI output and MCP tool errors; its stored JSON is a record in `adapters/sqlite`, its tool JSON an output type in `adapters/mcpserver`, both pinned by golden tests |
| Output filename and timestamp format (FR-010) | one function in `app/renderer` |
| PNG names (`NN.png`, width by shot count) | `shooter.ShotName`; `renderer.shotFile` only recognizes them to remove stale ones |
| Annotation drawing (box, arrow, label, dim) | `overlay.js` in `adapters/browser`; the markers' colour and placement live nowhere else |
| Card text and the closing line per language | `domain/card` (`HTML`, `Outro`); the intro/outro defaults come from `renderer.Prepare` |
| Path base of a demo (output folder, card images) | `renderer.Prepare`: the demo's folder, then the inside-the-work-dir check |
| Render log wording | `app/renderer` (`Request.Log`); callers only choose where the lines go |
| 30 s timeout, 25 cursor steps, 60 ms/char, 1920×1080, 30 fps | named constants in the owning package |
| Job statuses and transitions | typed constants in `app/jobs`. Nothing else compares status strings |
| Audience values | `script.Audiences`; a test keeps it equal to the schema enum |
| How the real tools are wired (ffmpeg, recorder, cards, file system) | `wire.NewDeps`, called by both mains |

- Derive, don't copy: tool description from the embedded schema, `get_options` voices from the directory scan, job `position` from a SQL count.
- Extract on the third occurrence, and only if the copies change for the same reason.
- Guard drift with tests: the example script in the tool description validates and passes the `explore_page` input schema, built-in voice names (`piper.Defaults`) match the files baked into the image, every built-in voice language has a closing line.
- The recorder and explorer both launch Chromium but differ in recording and visuals. Don't merge them. The executor's `Mode{Visuals}` is the one deliberate shared flag (ARCHITECTURE §7).

## Separation of concerns

Layout: ARCHITECTURE §3.

| Package | Job | Must not |
|---|---|---|
| `domain/script` | parse and validate input into typed values | start processes or touch the browser |
| `domain/voices`, `domain/failure` | pure resolution and error types | do I/O, know a provider's file formats or carry JSON tags |
| `domain/card` | build the card page and closing line from text | do I/O, start a process or pick the language to render |
| `domain/executor` | run one step | decide timing between steps or know about narration |
| `domain/recorder` | run one language's steps, record offsets (BR-003) | publish files, pick output names or launch a browser itself |
| `domain/shooter` | run a screenshots script's steps, name the PNGs, capture at `screenshot` steps | publish files or launch a browser itself |
| `app/renderer` | orchestrate the pipeline (video and screenshots), publish outputs | contain tool flags or SQL, or import a tool package |
| `app/explorer` | explore one page | enqueue, record video or take the render lock |
| `app/jobs` | job shape, statuses, the single worker | open SQLite or import MCP SDK types |
| `app/setup` | check and install the native tools through ports; the pins | import a tool package, or be called by `render` |
| `app/wire` | composition root: pick the TTS provider, map ports onto adapters | contain rules or render logic |
| `adapters/tts/piper`, `adapters/assembler`, `adapters/browser` | wrap one external tool each (`adapters/tts/wav` reads a clip's length) | know about jobs, language policy or the queue |
| `adapters/sqlite` | the jobs table and its stored JSON | import MCP SDK types or decide job transitions |
| `adapters/mcpserver` | MCP tools and prompt, map errors to tool errors | contain render logic or SQL |
| `adapters/download`, `adapters/apt`, `adapters/host` | sha256-checked HTTP fetch and tar.gz extract; `apt-get install ffmpeg`; root, architecture and PATH lookups | know the pins, the install layout or what `setup` is for |
| `adapters/lock`, `adapters/osfs` | flock wrapper; the real file system | know why they are used |
| `cmd/screencaster` | parse args, print progress, map result to exit code | contain render logic or wire tools |
| `cmd/screencaster-mcp` | open the store, start the worker and the server | contain render logic or wire tools |

Hard rules (ARCHITECTURE §3, enforced by depguard):
- `domain` imports nothing from `app`, `adapters` or `cmd`; `app` (except `app/wire`) imports no adapter.
- Only `adapters/tts/piper`, `adapters/assembler`, `adapters/browser`, `adapters/apt` and `adapters/host` call `os/exec`; only `adapters/browser` calls playwright-go (launch and `Install`); only `adapters/download` makes HTTP calls, and only `setup` uses it; only `adapters/sqlite` opens SQLite; only `adapters/mcpserver` and `cmd/screencaster-mcp` use the MCP SDK.
- `cmd/screencaster` imports neither the MCP server nor the job store.
- Domain rules (validation, language and voice resolution) live in `domain`, never in `main` or a handler.
- Paths (`outputDir`, card images) and `baseUrl` come from the demo script via `renderer.Plan` (the `explore_page` handler gets them from its input) and are passed down. No package reads env vars or the working directory itself; `main` passes `os.Getenv` and the working directory to `wire.TTS` and `wire.InstallDir`. **Deviation:** `wire.launcher` reads `PLAYWRIGHT_DRIVER_PATH` and `PLAYWRIGHT_BROWSERS_PATH` and sets the latter, because the driver process reads it from this process's environment and both mains and the explorer launch browsers (decision 77).
- `screencaster-mcp` never writes to stdout except protocol frames. Logs go to stderr (ARCHITECTURE §12).

## SOLID

Applied to packages, functions and small structs.

**Single responsibility.** A TTS adapter turns text into a WAV plus duration. `assembler` turns recording and clips into an MP4. `executor` runs a step. `recorder` decides when the next step starts. If a unit needs "and" to describe it, split it.

**Open/closed.** Extend by data or table entry, not by editing working code.
- New language = voice file in `/work/voices`, no code (FR-018).
- New TTS provider = one adapter package, one `wire.TTS` case, one image layer (ARCHITECTURE §15).
- New step action = schema property under `$defs/step`, one action type in `domain/script`, one case in the executor's type switch. **Exception:** `screenshot` is handled by `domain/shooter`, not the executor, because a capture needs an output path and a counter and the executor runs one step without files (decision 75); validation keeps it out of the executor.
- New failure kind = a constructor for the shared `Failure`, not a new path through CLI and MCP.

**Liskov substitution.** Fakes of `Recorder`, `Synthesizer`, `Assembler`, `Cards` and `Session` return the same error types, respect `ctx` cancellation and leave no files behind, like the real ones. The executor behaves identically in both modes except visuals, so an explore selector works in a render (FR-017 AC3).

**Interface segregation.** Interfaces are small and declared by the consumer (`renderer` declares the 1–3 methods it needs). Functions take what they use: `voices.Resolve(langs, scriptVoices, catalog)` takes maps and a catalog, not the whole `Script`.

**Dependency inversion.** `renderer` depends on interfaces, unit tests pass fakes. Time and run IDs are injected so tests get deterministic filenames. Wrappers take binary and voice paths as constructor arguments; the TTS provider comes from `wire.TTS(os.Getenv, ...)` with `getenv` injected. Wire by hand in `app/wire`, the one composition root.

## Law of Demeter

- Validation produces a `Plan` (languages, voice per language, steps, paths). The pipeline uses it, not the script file and the voice directory again.
- Pass exactly what a function needs: `Synthesize(ctx, voice, text, outPath)`, not the step and the plan.
- Let a function answer the question: `queue.Position(id)`, not the caller counting rows.
- No chains like `job.Result.Outputs[0].Meta.Path`. Add an accessor or flatten the type.
- Callers never parse Piper or ffmpeg output. A TTS adapter returns a duration, `assembler` returns a path. Tool stderr appears only inside a `Failure` message.
- A short flat field path on a plain data struct (`plan.Voices[lang]`) is fine.

## Composition over inheritance

Go has no inheritance. The rule is about not rebuilding it.

- Hold collaborators as named fields: `renderer.Deps{TTS, Rec, Asm}`.
- `Render` calls small functions (`Prepare`, `renderLanguage`, `publish`). CLI and MCP both call the same `renderer.Render` (ARCHITECTURE §4).
- Don't embed a struct just to inherit its methods. Embedding is fine for plumbing like `sync.Mutex` in a private struct, never for domain types or across packages.
- No "base" type customised through overridden hooks.
- No long boolean-flag lists making one function impersonate several.

## Working agreements

- **Context.** Anything that blocks or spawns a process takes `ctx` first. Cancellation must close the browser and delete temp files (FR-008).
- **Errors.** Return, don't panic. Wrap with `%w`. Step, TTS and assembly failures become `failure.Failure` before leaving `app`. Use `errors.Is/As`, never match message text. Messages the PRD specifies (`outputDir must stay inside the working directory: ../out`, `job not found: <id>`, `tts failed at step <n> (<lang>): …`) are reproduced exactly.
- **Fail early.** Validate script, narration, voices and storageState before starting a browser or Piper (FR-002, BR-011).
- **Cleanup.** Temp files go under `.screencaster/tmp/<runId>/` and are removed with `defer` on success and abort. Write to `outputDir` only in the final publish step. Never open an existing output for writing (BR-006).
- **Subprocesses.** `exec.CommandContext`, stdout and stderr captured, never inherited. Keep the last lines of stderr for error messages.
- **Determinism.** No LLM calls, nothing random in anything that affects output, no HTTP client on the render path (BR-001, NFR-003); the one HTTP client, in `adapters/download`, serves only `setup`.
- **API surface.** Everything lives under `internal/`, so nothing is importable from outside the module. Inside it, keep exported names few and documented.
- **Comments.** Explain why, not what. Non-obvious choices (t0 after the sync marker, forced 30 fps) get one comment pointing to the ARCHITECTURE section or decision number.
- **Tests.** Table-driven, one behaviour per case, names state the rule (`TestLanguages_overrideBeatsScript`). Reference the BR/FR when a test enforces one.

## Enforcement

CI is as specified in PRD §18. `make lint`, `make vet` and `make test` run the same checks locally in the dev image.

| Check | Guards |
|---|---|
| `golangci-lint` (with a `depguard` rule for the import boundaries above) | unused code, error handling, boundary rules |
| `go test ./...` in the one module | domain and use-case rules (resolution, validation, timing math), queue order and recovery, schema accepts the example script and rejects invalid samples |
| `make e2e` in the dev image (CI `e2e` job and local, Decision 68) | ffprobe (h264, 1920×1080, 30 fps, aac), drift, NFR-001 ratio, explore selector reused in a render, screenshot sizes and annotation pixels |
| `make image` + `make image-check` (CI `image` job) | base and provider image build, built-in voices (names from `piper.Defaults`, checked by `providers/piper/image-check.sh`) present |
| `make e2e-runtime` (local) | the image's own binary, Chromium, Piper and ffmpeg render a video and take screenshots |

Everything not in this table is a review point.

## Review checklist

1. **KISS**: simpler way? Does the standard library or OS already do it?
2. **YAGNI**: does every line serve a current PRD requirement?
3. **DRY**: is any knowledge now in two places?
4. **Separation**: right package? Did a rule leak into `main`, a handler or a wrapper?
5. **SOLID**: one reason to change? Small interface? Testable without Chromium, Piper or ffmpeg?
6. **Demeter**: reaching through structs?
7. **Composition**: embedding to inherit?
8. **Safety**: `ctx` passed, errors wrapped, temp files cleaned on every path, nothing on stdout in the MCP binary, no existing output touched.
9. **Proof**: tests for the logic, lint clean, e2e green if the pipeline changed.

## Known debt

- The default Piper pins (version, sha256, voices revision and four voice sha256 values) live in `app/setup/pins.env` and in the `ARG`s of `providers/piper/Dockerfile`. `pins_test.go` fails when they differ, so bump both together. (A native user can override every pin with `--pins-file` or the environment without a rebuild.) Fix: have the Dockerfile build stage read `pins.env` (its build context is only `providers/piper` today). Fix: have the Dockerfile fetch through the Go pins, or generate one from the other.
- `PLAYWRIGHT_GO_VERSION` in the `dev-base` stage of the `Dockerfile` repeats the playwright-go version in the root `go.mod`. Bump both together (the runtime image reads it from `go.mod`).
- The playwright-go module path is `github.com/mxschmitt/playwright-go`, the one its `go.mod` declares (Decision 66). Re-check on every bump that it has not moved to `playwright-community`.
- golangci-lint is pinned to v2.12.0 (newest that builds on Go 1.25) in two places: `Dockerfile` and `.github/workflows/ci.yml`. Bump both together when the Go version moves to 1.26.
- Sync marker drift (Decision 69, ARCHITECTURE §17.1): over 20 renders the e2e drift was 25–66 ms in 19 and 132 ms in one, on the 33 ms frame grid. The one run above `maxDrift` (±100 ms, FR-007) means the CI `e2e` job can flake about 1 run in 20. The plan's ±40 ms is not reached and no bias constant is applied (the ~30 ms is mostly the measurement). Find the cause of the outlier or raise the recording frame rate, then tighten the bound.
- The CI `e2e` job has no Docker layer cache: buildx's cache needs a driver that cannot see the locally built `screencaster-dev-base`. Every run builds both images. Add a registry-backed cache if the minutes hurt.
- `synthesizeAll` has pool machinery (semaphore, lowest-failure tracking) that `ttsWorkers = 1` never uses (Decision 71). It is a KISS/YAGNI cost kept for a longer script that may show the 1.3× speedup; delete it if none does.
- `explore_page` may overlap a render (Decision 44). If timing jitters, add one shared browser semaphore at the launch point.
- `modernc.org/sqlite` is pinned to v1.50.0 in `go.mod`, the newest release that builds on Go 1.25 (v1.60 needs 1.26). Bump it together with the Go version.
- The `>> nth=<i>` suffix from `explore_page` assumes the matches appear in the snapshot in DOM order. It holds for ordinary pages; a page that reorders elements visually only is not covered.
- A screenshots run needs a TTS provider configured: `cmd/screencaster` calls `wire.TTS` when a render starts and `screencaster-mcp` at startup, though screenshots never synthesize. Both images always set one. Splitting `NewDeps` so a screenshots-only run needs none was left out as scope creep.
- Publishing screenshots overwrites by design (decision 73), so a move that fails part-way leaves a folder with a mix of new and old shots; the error names the file that failed. Rerunning fixes it.
- An MCP job snapshots only its YAML (`jobs.script`). Card images and anything else the demo points at are read again when the job runs, so editing them while a job waits does change that job.
