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

- Standard library and OS first: `flock` for the render lock, `os.Rename` to publish, `go:embed` for the schema, `encoding/binary` for the WAV header, `os/exec` for Piper and ffmpeg. The TTS provider is a static `switch` in `provider.FromEnv`, no registry or plugin loading.
- Short, single-purpose functions with early returns. Handle the error, then continue on the happy path.
- Plain structs and slices. No ORM, DI container, plugin system or job framework. `database/sql` with a few queries is enough (ARCHITECTURE §10).
- **Deviation:** `cobra` for the CLI (Decision 53, user choice). It is the only framework allowed; std `flag` would have been enough for one command.
- One long-lived goroutine in the MCP server: the queue worker (ARCHITECTURE §6.2). Everything else is a plain call.

## YAGNI

Build for the PRD, not for what might come.

- Delete code with no caller: unused functions, struct fields, config keys, schema properties.
- Keep PRD §10.3 out: login steps, masking, other formats, cloud TTS, web UI, auto-deletion, CI-triggered renders, schema-only tool.
- Keep PRD §10.2 out: slides between steps, overlays, multi-tenancy (the start and end cards are in, decision 63). No hooks "for later" beyond what ARCHITECTURE §15 already names.
- No second TTS engine (the `provider.Engine` seam is ready for one), browser, storage backend or output format. Only languages are open-ended (BR-002).
- `Recorder`, `Synthesizer`, `Assembler` and `Cards` interfaces (in `core/renderer`) and `Page`/`Session` (in `core/executor`/`core/recorder`) exist as test seams, not for swapping Chromium or ffmpeg. No interface without a test seam. **Deviation:** `provider.Engine` is also a deliberate swap seam for the TTS engine (Decision 65, user choice); it is two methods, with a static switch to pick the adapter.
- No retries, backoff or configurable timeouts. BR-004: one 30 s timeout, then abort.
- No scale design. One worker, one SQLite file, one render at a time (BR-008).

## DRY

Each piece of knowledge has one authoritative place. Two blocks that merely look alike are not duplication.

| Knowledge | Lives in |
|---|---|
| Script format (fields, enums, limits) | `core/script/script.schema.json`, embedded in `core/script`. Same text feeds validation and the `render_video` tool description |
| Cross-field script rules (narration per selected language, scroll/wait exclusivity) | `core/script`, one validate function |
| Language selection, override > script > `["en"]` (BR-002) | one function, called by CLI, MCP validation and worker |
| Voice resolution rules (BR-011) | `core/voices`, over a provider's `Catalog` |
| Built-in voices per language and Piper naming (`.onnx`, language from the name) | `core/provider/piper` (`Defaults`) |
| Which TTS provider runs, and its binary and voice paths | `provider.FromEnv` selects; the Dockerfile ENV lines hold the paths |
| Step semantics (FR-005) and URL resolution against `baseUrl` (BR-010) | `core/executor`, shared by render and `explore_page` |
| Failure shape `{step, lang, action, target, message}` | `core/failure`, used by CLI output, MCP tool errors and `jobs.error_json` |
| Output filename and timestamp format (FR-010) | one function in `core/renderer` |
| Card text and the closing line per language | `core/card` (`HTML`, `Outro`); the intro/outro defaults come from `renderer.Prepare` |
| Path base of a demo (output folder, card images) | `renderer.Prepare`: the demo's folder, then the inside-the-work-dir check |
| Render log wording | `core/renderer` (`Request.Log`); callers only choose where the lines go |
| 30 s timeout, 25 cursor steps, 60 ms/char, 1920×1080, 30 fps | named constants in the owning package |
| Job statuses and transitions | typed constants in `mcp/queue`. Nothing else compares status strings |
| Audience values | `script.Audiences`; a test keeps it equal to the schema enum |

- Derive, don't copy: tool description from the embedded schema, `get_options` voices from the directory scan, job `position` from a SQL count.
- Extract on the third occurrence, and only if the copies change for the same reason.
- Guard drift with tests: the example script in the tool description validates, built-in voice names (`piper.Defaults`) match the files baked into the image.
- The recorder and explorer both launch Chromium but differ in recording and visuals. Don't merge them. The executor's `Mode{Visuals}` is the one deliberate shared flag (ARCHITECTURE §7).

## Separation of concerns

Layout: ARCHITECTURE §3.

| Package | Job | Must not |
|---|---|---|
| `core/script` | parse and validate input into typed values | start processes or touch the browser |
| `core/support/*` | small shared helpers (today: `wav`, a clip's length); one concern per package | know about providers, languages, jobs or steps |
| `core/voices`, `core/failure` | pure resolution and error types | do I/O or know a provider's file formats |
| `core/card` | build the card page and closing line from text | do I/O, start a process or pick the language to render |
| `core/provider/piper`, `core/assembler`, `core/browser` | wrap one external tool each (`core/provider` picks the adapter, `core/support/wav` reads a clip's length) | know about jobs, language policy or the queue |
| `core/executor` | run one step | decide timing between steps or know about narration |
| `core/recorder` | run one language's steps, record offsets (BR-003) | publish files or pick output names |
| `core/renderer` | orchestrate the pipeline, publish outputs | contain tool flags or SQL |
| `core/explorer` | explore one page | enqueue, record video or take the render lock |
| `core/lock` | flock wrapper | know why it locks |
| `cli` | parse args, print progress, map result to exit code | contain render logic |
| `mcp/server` | MCP tools and prompt, map errors to tool errors | contain render logic or SQL |
| `mcp/queue` | SQLite store and the single worker | import MCP SDK types |

Hard rules:
- `cli` → `core`, `mcp` → `core`. Never the reverse. `cli` never imports `mcp`.
- `core` imports neither SQLite nor the MCP SDK.
- Only `core/provider/piper`, `core/assembler` and `core/browser` call `os/exec` or playwright-go.
- Domain rules (validation, language and voice resolution) live in `core`, never in `main` or a handler.
- Paths (`outputDir`, card images) and `baseUrl` come from the demo script via `renderer.Plan` (the `explore_page` handler gets them from its input) and are passed down. No package reads env vars or the working directory itself; `main` passes `os.Getenv` and the working directory to `provider.FromEnv`.
- `screencaster-mcp` never writes to stdout except protocol frames. Logs go to stderr (ARCHITECTURE §12).

## SOLID

Applied to packages, functions and small structs.

**Single responsibility.** A TTS adapter turns text into a WAV plus duration. `assembler` turns recording and clips into an MP4. `executor` runs a step. `recorder` decides when the next step starts. If a unit needs "and" to describe it, split it.

**Open/closed.** Extend by data or table entry, not by editing working code.
- New language = voice file in `/work/voices`, no code (FR-018).
- New TTS provider = one adapter package, one `FromEnv` case, one image layer (ARCHITECTURE §15).
- New step action = schema entry plus one case in the executor's single dispatch.
- New failure kind = a constructor for the shared `Failure`, not a new path through CLI and MCP.

**Liskov substitution.** Fakes of `Recorder`, `Synthesizer`, `Assembler`, `Cards` and `Session` return the same error types, respect `ctx` cancellation and leave no files behind, like the real ones. The executor behaves identically in both modes except visuals, so an explore selector works in a render (FR-017 AC3).

**Interface segregation.** Interfaces are small and declared by the consumer (`renderer` declares the 1–3 methods it needs). Functions take what they use: `voices.Resolve(langs, scriptVoices, catalog)` takes maps and a catalog, not the whole `Script`.

**Dependency inversion.** `renderer` depends on interfaces, unit tests pass fakes. Time and run IDs are injected so tests get deterministic filenames. Wrappers take binary and voice paths as constructor arguments; the TTS provider comes from `provider.FromEnv(os.Getenv, ...)` with `getenv` injected. Wire by hand in `main`.

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
- **Errors.** Return, don't panic. Wrap with `%w`. Step, TTS and assembly failures become `core/failure.Failure` before leaving `core`. Use `errors.Is/As`, never match message text. Messages the PRD specifies (`outputDir must stay inside the working directory: ../out`, `job not found: <id>`, `tts failed at step <n> (<lang>): …`) are reproduced exactly.
- **Fail early.** Validate script, narration, voices and storageState before starting a browser or Piper (FR-002, BR-011).
- **Cleanup.** Temp files go under `.screencaster/tmp/<runId>/` and are removed with `defer` on success and abort. Write to `outputDir` only in the final publish step. Never open an existing output for writing (BR-006).
- **Subprocesses.** `exec.CommandContext`, stdout and stderr captured, never inherited. Keep the last lines of stderr for error messages.
- **Determinism.** No LLM calls, no HTTP clients, nothing random in anything that affects output (BR-001, NFR-003).
- **API surface.** `cli` and `mcp` are separate modules, so `core` packages are exported. Keep exported names few and documented.
- **Comments.** Explain why, not what. Non-obvious choices (t0 before page creation, forced 30 fps) get one comment pointing to the ARCHITECTURE section or decision number.
- **Tests.** Table-driven, one behaviour per case, names state the rule (`TestLanguages_overrideBeatsScript`). Reference the BR/FR when a test enforces one.

## Enforcement

CI is as specified in PRD §18. `make lint`, `make vet` and `make test` run the same checks locally in the dev image.

| Check | Guards |
|---|---|
| `golangci-lint` (with a `depguard` rule for the import boundaries above) | unused code, error handling, boundary rules |
| `go test ./...` for each workspace module | `core` rules (resolution, validation, timing math), queue order and recovery, schema accepts the example script and rejects invalid samples |
| `make e2e` in the dev image (local, not in CI, Decision 54) | ffprobe (h264, 1920×1080, 30 fps, aac), drift, NFR-001 ratio, explore selector reused in a render |
| `make image` + `make image-check` (CI `image` job) | image builds, built-in voices (names from `piper.Defaults`) present |
| `make e2e-runtime` (local) | the image's own binary, Chromium, Piper and ffmpeg render a video |

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

- `PLAYWRIGHT_GO_VERSION` in the `dev` stage of the `Dockerfile` repeats the playwright-go version in `core/go.mod`. Bump both together (the runtime image reads it from `go.mod`).
- golangci-lint is pinned to v2.12.0 (newest that builds on Go 1.25) in two places: `Dockerfile` and `.github/workflows/ci.yml`. Bump both together when the Go version moves to 1.26.
- The fixed 90 ms lead-in (ADR-46) is wrong when the WebM start lands in its late mode (~590 ms, ARCHITECTURE §17.1), so narration plays late in some renders. The e2e drift bound is ±150 ms, not FR-007's ±100 ms (`maxDrift`). Measure the lead-in per recording, then tighten the bound.
- `explore_page` may overlap a render (Decision 44). If timing jitters, add one shared browser semaphore at the launch point.
- `modernc.org/sqlite` is pinned to v1.50.0 in `mcp/go.mod`, the newest release that builds on Go 1.25 (v1.60 needs 1.26). Bump it together with the Go version.
- `cli/main.go` and `mcp/main.go` both wire ffmpeg, the recorder, the `cards` adapter (`browser.Shot` from `renderer.Shot`) and `renderer.Deps` by hand. That is two copies; extract a shared constructor on the third. (The TTS part is already one shared call, `provider.FromEnv`.)
- The `>> nth=<i>` suffix from `explore_page` assumes the matches appear in the snapshot in DOM order. It holds for ordinary pages; a page that reorders elements visually only is not covered.
- `explore_page` actions go through `script.ValidateSteps`, so a `fill` with an empty `value` is reported as a missing `value` (the empty string is dropped when the steps are encoded). Clearing a field is not needed to find selectors.
