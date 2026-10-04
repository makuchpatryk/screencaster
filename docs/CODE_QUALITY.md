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

- Standard library and OS first: `flock` for the render lock, `os.Rename` to publish, `go:embed` for the schema, `encoding/binary` for the WAV header, `os/exec` for Piper and ffmpeg.
- Short, single-purpose functions with early returns. Handle the error, then continue on the happy path.
- Plain structs and slices. No ORM, DI container, plugin system or job framework. `database/sql` with a few queries is enough (ARCHITECTURE §10).
- **Deviation:** `cobra` for the CLI (Decision 53, user choice). It is the only framework allowed; std `flag` would have been enough for one command.
- One long-lived goroutine in the MCP server: the queue worker (ARCHITECTURE §6.2). Everything else is a plain call.

## YAGNI

Build for the PRD, not for what might come.

- Delete code with no caller: unused functions, struct fields, config keys, schema properties.
- Keep PRD §10.3 out: login steps, masking, other formats, cloud TTS, web UI, auto-deletion, CI-triggered renders, schema-only tool.
- Keep PRD §10.2 out: slides, overlays, multi-tenancy. No hooks "for later" beyond what ARCHITECTURE §15 already names.
- No second TTS engine, browser, storage backend or output format. Only languages are open-ended (BR-002).
- `Recorder`, `Synthesizer` and `Assembler` interfaces (in `core/renderer`) and `Page`/`Session` (in `core/executor`/`core/recorder`) exist as test seams, not for swapping Chromium, Piper or ffmpeg. No interface without a test seam.
- No retries, backoff or configurable timeouts. BR-004: one 30 s timeout, then abort.
- No scale design. One worker, one SQLite file, one render at a time (BR-008).

## DRY

Each piece of knowledge has one authoritative place. Two blocks that merely look alike are not duplication.

| Knowledge | Lives in |
|---|---|
| Script format (fields, enums, limits) | `core/script/script.schema.json`, embedded in `core/script`. Same text feeds validation and the `render_video` tool description |
| Cross-field script rules (narration per selected language, scroll/wait exclusivity) | `core/script`, one validate function |
| Language selection, override > script > `["en"]` (BR-002) | one function, called by CLI, MCP validation and worker |
| Voice resolution and built-in `en`/`pl` defaults (BR-011) | `core/voices` |
| Step semantics (FR-005) and URL resolution against `baseUrl` (BR-010) | `core/executor`, shared by render and `explore_page` |
| Failure shape `{step, lang, action, target, message}` | `core/failure`, used by CLI output, MCP tool errors and `jobs.error_json` |
| Output filename and timestamp format (FR-010) | one function in `core/renderer` |
| 30 s timeout, 25 cursor steps, 60 ms/char, 1920×1080, 30 fps | named constants in the owning package |
| Job statuses and transitions | typed constants in `mcp/queue`. Nothing else compares status strings |

- Derive, don't copy: tool description from the embedded schema, `get_options` voices from the directory scan, job `position` from a SQL count.
- Extract on the third occurrence, and only if the copies change for the same reason.
- Guard drift with tests: the example script in the tool description validates, built-in voice names match the files baked into the image.
- The recorder and explorer both launch Chromium but differ in recording and visuals. Don't merge them. The executor's `Mode{Visuals}` is the one deliberate shared flag (ARCHITECTURE §7).

## Separation of concerns

Layout: ARCHITECTURE §3.

| Package | Job | Must not |
|---|---|---|
| `core/config`, `core/script` | parse and validate input into typed values | start processes or touch the browser |
| `core/voices`, `core/failure` | pure resolution and error types | do I/O beyond listing voice files |
| `core/tts`, `core/assembler`, `core/browser` | wrap one external tool each | know about jobs, language policy or the queue |
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
- Only `core/tts`, `core/assembler` and `core/browser` call `os/exec` or playwright-go.
- Domain rules (validation, language and voice resolution) live in `core`, never in `main` or a handler.
- Paths, `baseUrl` and `outputDir` come from `core/config` and are passed down. No package reads env vars or the working directory itself.
- `screencaster-mcp` never writes to stdout except protocol frames. Logs go to stderr (ARCHITECTURE §12).

## SOLID

Applied to packages, functions and small structs.

**Single responsibility.** `tts` turns text into a WAV plus duration. `assembler` turns recording and clips into an MP4. `executor` runs a step. `recorder` decides when the next step starts. If a unit needs "and" to describe it, split it.

**Open/closed.** Extend by data or table entry, not by editing working code.
- New language = voice file in `/work/voices`, no code (FR-018).
- New step action = schema entry plus one case in the executor's single dispatch.
- New failure kind = a constructor for the shared `Failure`, not a new path through CLI and MCP.

**Liskov substitution.** Fakes of `Recorder`, `Synthesizer`, `Assembler` and `Session` return the same error types, respect `ctx` cancellation and leave no files behind, like the real ones. The executor behaves identically in both modes except visuals, so an explore selector works in a render (FR-017 AC3).

**Interface segregation.** Interfaces are small and declared by the consumer (`renderer` declares the 1–3 methods it needs). Functions take what they use: `resolveVoices(langs, scriptVoices, cfgVoices, installed)` takes maps and a set, not `Config` and `Script`.

**Dependency inversion.** `renderer` depends on interfaces, unit tests pass fakes. Time and run IDs are injected so tests get deterministic filenames. Wrappers take binary and voice paths as constructor arguments. Wire by hand in `main`.

## Law of Demeter

- Validation produces a `Plan` (languages, voice per language, steps, paths). The pipeline uses it, not `cfg`, `script` and the voice directory again.
- Pass exactly what a function needs: `tts.Synthesize(ctx, voice, text, outPath)`, not the step and config.
- Let a function answer the question: `queue.Position(id)`, not the caller counting rows.
- No chains like `job.Result.Outputs[0].Meta.Path`. Add an accessor or flatten the type.
- Callers never parse Piper or ffmpeg output. `tts` returns a duration, `assembler` returns a path. Tool stderr appears only inside a `Failure` message.
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
- **Errors.** Return, don't panic. Wrap with `%w`. Step, TTS and assembly failures become `core/failure.Failure` before leaving `core`. Use `errors.Is/As`, never match message text. Messages the PRD specifies (`config not found: /work/screencaster.yaml`, `job not found: <id>`, `tts failed at step <n> (<lang>): …`) are reproduced exactly.
- **Fail early.** Validate config, script, narration and voices before starting a browser or Piper (FR-002, BR-011).
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
| `docker build` (CI job from M4) | image contents, built-in voices present |

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

- golangci-lint is pinned to v2.12.0 (newest that builds on Go 1.25) in two places: `Dockerfile` and `.github/workflows/ci.yml`. Bump both together when the Go version moves to 1.26.
- `explore_page` may overlap a render (Decision 44). If timing jitters, add one shared browser semaphore at the launch point.
- Open spike (ARCHITECTURE §17): `AriaSnapshot` output. Update code and decision log when it resolves.
