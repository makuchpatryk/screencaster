# screencaster MVP (M1–M5) — Implementation Plan

**Based on:** PRD v1.3, ARCHITECTURE v0.1, CODE_QUALITY · **Date:** 2026-10-03

## Summary

This plan implements PRD v1.3 from scratch. A YAML demo script is rendered into one narrated 1920×1080 MP4 per selected language, using playwright-go + Chromium, the Piper C++ binary and ffmpeg. The work splits into the CLI path (M1–M4) and an MCP server with a SQLite FIFO queue (M5). The repo has docs only, and the host has Docker but no Go, ffmpeg or Piper, so all builds and tests run in a dev Docker image from M1 onward. The CLI path has priority: if time runs short, M5 slips, not M1–M4.

## Success Criteria

- The Definition of Done for each milestone in PRD §19 is met, with evidence noted in the PR.
- Every FR acceptance criterion maps to a named test (table in "Test Strategy").
- **NFR-001:** e2e job wall time ≤ 2 × the sum of output durations (EN+PL run, no explore running).
- **NFR-002:** ffprobe reports `h264`, `1920x1080`, `30/1` fps and an `aac` stream on every output.
- **Drift:** the e2e marker step shows |flash frame time − (narration audio onset − Piper lead silence)| ≤ 100 ms.
- `golangci-lint` (with depguard boundaries), `go vet` and `go test -race` pass in `core`, `cli`, `mcp` and `tests/e2e`. CI is green.

## Scope & Constraints

- **In scope:** M1–M5 (FR-001…FR-019); a dev image plus the final Docker image; a Makefile; GitHub Actions (lint, unit, `docker build`); a local-only `make e2e`; fixes to PRD/README/ARCHITECTURE/CODE_QUALITY.
- **Out of scope:** PRD §10.2 (slides, overlays, multi-tenant) and §10.3 (login steps, masking, other formats, cloud TTS, auto-deletion, schema-only tool, CI-triggered renders). Also out: e2e in CI, and a subprocess MCP e2e.
- **Hard constraints:**
  - BR-001 / NFR-003: no LLM and no HTTP client in the render path.
  - BR-004 / FR-008: abort leaves no MP4 and no temp dir.
  - BR-006: never open an existing output for writing.
  - BR-008: one render at a time (queue + flock).
  - BR-009: startup recovery.
  - ADR-48: nothing on MCP stdout except protocol frames.
- **Trade-offs:**
  - e2e runs locally only: CI stays cheap and fast, at the cost of the pipeline not being verified in CI. This deviates from ARCHITECTURE §13.
  - cobra over std `flag`: user preference, deviates from CODE_QUALITY KISS.
  - The archived Piper C++ binary is frozen upstream, but it is a single binary with no Python runtime.
  - In-memory MCP tests plus a manual Claude Code check instead of a subprocess e2e: deterministic, but real stdio framing is only checked manually.

## Architecture & Design

### High-Level Flow

As in ARCHITECTURE §4. `renderer.Render` is the single path for both the CLI and the MCP worker.

```
cli render ─┐                                   ┌─ mcp render_video: renderer.Prepare (validate) → INSERT queued
            ▼                                   ▼
   lock.TryAcquire (fail fast)        worker: dequeue oldest → lock.Acquire (blocking) → running
            └──────────────► renderer.Render(ctx, req) ◄─────────────┘
                 Prepare: config → script (schema + cross-field) → languages → voices → Plan
                 for lang in plan.Languages:
                     tts.Synthesize per narrated step  → clips{path, dur}
                     recorder.Record(steps, clips)      → webm + offsets   (t0, max(actionEnd, start+clip))
                     assembler.Assemble(webm, clips@offsets, meta) → tmp/<lang>/out.mp4 + durationMs
                 publish: rename all → <outputDir>/<name>.<lang>.<ts>.mp4
                 defer: rm -rf .screencaster/tmp/<runId>
```

### Key Changes

**Repo layout:** ARCHITECTURE §3, plus the changes marked ★.

```
go.work                      ★ committed (go 1.25; use ./core ./cli ./mcp ./tests/e2e)
go.work.sum                  ★ committed
Makefile                     ★ dev-image, test, lint, vet, image, e2e
Dockerfile                   stages: dev → build → runtime
.golangci.yml                ★ v2 config, depguard boundaries
.github/workflows/ci.yml
core/  (module screencaster/core)
  config/ script/ voices/ failure/ tts/ browser/ executor/ recorder/
  assembler/ renderer/ explorer/ lock/
  script/script.schema.json  ★ moved from root schema/ (go:embed cannot use "..")
cli/   (module screencaster/cli)   main.go, render.go (cobra)
mcp/   (module screencaster/mcp)   main.go, server/, queue/
tests/e2e/ (module screencaster/e2e) ★ 4th module, //go:build e2e
testdata/fixture-app/        index.html, projects.html, marker.html, app.js
testdata/storageState.json   cookie sc_session=fixture, domain 127.0.0.1
testdata/scripts/            valid/invalid sample scripts for unit tests
```

**Dependencies (pin exact versions at implementation time):**

| Module | Deps |
|---|---|
| core | `github.com/goccy/go-yaml`, `github.com/santhosh-tekuri/jsonschema/v6`, `github.com/mxschmitt/playwright-go` (v0.6201.1; the project moved from playwright-community at v0.6100.0), `golang.org/x/sys/unix` (flock) |
| cli | `screencaster/core`, `github.com/spf13/cobra` |
| mcp | `screencaster/core`, `github.com/modelcontextprotocol/go-sdk`, `modernc.org/sqlite`, `github.com/google/uuid` |
| e2e | `screencaster/core` (tts for lead-silence calibration, explorer) |

**Core types (sketch):**

```go
// core/failure
type Failure struct {
    Step    *int   `json:"step,omitempty"`
    Lang    string `json:"lang,omitempty"`
    Action  string `json:"action,omitempty"`
    Target  string `json:"target,omitempty"`
    Message string `json:"message"`
}
func (f *Failure) Error() string
func Step(i int, lang, action, target string, err error) *Failure
func TTS(i int, lang, stderr string) *Failure        // "tts failed at step <n> (<lang>): <stderr>"
func Assembly(lang, stderrTail string) *Failure       // "assembly failed (<lang>): <last 20 lines>"
func Interrupted() *Failure                           // {message:"interrupted"}
type ValidationError struct{ Pointer, Message string }
type ValidationErrors []ValidationError               // Error() joins "pointer: message"

// core/config
type Config struct { BaseURL, StorageState, OutputDir string; Voices map[string]string; WorkDir string }
func Load(workDir string) (Config, error) // "config not found: <abs>", "baseUrl is required", storageState exists, default outputDir

// core/script
type Script struct { Name string; Languages []string; Voices map[string]string; Meta *Meta; Steps []Step }
type Step struct { Action, URL, Selector, Value, Key string; Y, Ms *int; Narration map[string]string }
func Parse(data []byte) (Script, error)               // YAML → JSON → schema → typed; ValidationErrors
func Validate(s Script, langs []string) failure.ValidationErrors // narration per selected lang; scroll/wait exclusivity is in the schema
func Languages(override, scriptLangs []string) []string // BR-002, the single implementation; drops duplicates
func SchemaJSON() []byte                              // for render_video description (DRY)
func ExampleYAML() []byte                             // embedded example.yaml, same purpose

// core/voices
type Installed map[string]string                      // voice name → .onnx path
func Discover(dirs ...string) (Installed, error)      // /opt/piper/voices, /work/voices
func Lang(voice string) string                        // prefix before first "_"
func Resolve(langs []string, scriptV, cfgV map[string]string, inst Installed) (map[string]string, failure.ValidationErrors) // BR-011, lists every failing language

// core/renderer — consumer-declared seams
type Synthesizer interface { Synthesize(ctx context.Context, voicePath, text, outPath string) (time.Duration, error) }
type Recorder    interface { Record(ctx context.Context, in recorder.Input) (recorder.Output, error) }
type Assembler   interface { Assemble(ctx context.Context, in assembler.Input) (durationMs int64, err error) }
type Deps struct { TTS Synthesizer; Rec Recorder; Asm Assembler; Now func() time.Time; RunID func() string }
type Request struct { WorkDir, ScriptPath string; LangOverride []string } // M3 adds Progress func(lang string, i, n int, action, target string)
type Plan struct { Cfg config.Config; Script script.Script; Languages []string; Voices map[string]string /*lang→path*/ }
type Output struct { Lang, Path string; DurationMs int64 }
func Prepare(req Request, installed voices.Installed) (Plan, error) // validation only, used by MCP synchronously
func Render(ctx context.Context, d Deps, req Request) ([]Output, error)
func OutputName(name, lang string, ts time.Time) string // "<name>.<lang>.20261003T101500Z.mp4"

// core/executor — Page is declared here and implemented by core/browser
type Mode struct{ Visuals bool }
type Page interface { Goto(url string) error; Click(sel string) error; Fill(sel, v string, delay time.Duration) error;
    Select(sel, v string) error; Press(sel, key string) error; Hover(sel string) error;
    ScrollIntoView(sel string) error; ScrollTo(y int) error; WaitVisible(sel string) error;
    MoveTo(sel string, steps int) error }             // every call uses Timeout = 30 s
func New(p Page, baseURL string, m Mode) *Executor
func (e *Executor) Run(ctx context.Context, i int, s script.Step) error // *failure.Failure (Lang filled by recorder)

// core/lock
func TryAcquire(path string) (*Lock, error) // ErrHeld → CLI maps it to "another render is running"
func Acquire(ctx context.Context, path string) (*Lock, error)
func (l *Lock) Release() error               // unlock + close; never deletes the file
```

**Constants** (named, in the owning package): `executor.ActionTimeout = 30*time.Second`, `executor.GlideSteps = 25`, `executor.TypeDelay = 60*time.Millisecond`, `browser.Width/Height = 1920/1080`, `assembler.FPS = 30`, `explorer.MaxSnapshot = 50000`.

**CLI:** `screencaster render <script> [--lang en,pl]` (cobra). Progress goes to stderr as `[en] step 3/12 click role=button[name="New project"]`. Absolute output paths go to stdout. Exit 0 on success. Exit 1 on validation (each `pointer: message`), step failure (BR-004 fields), lock held, or a missing config.

**MCP tools and prompt:** exactly as PRD FR-012/013/017/018/019. The `render_video` description is built at startup from `script.SchemaJSON()` + an embedded example script + the three rules. Tool errors use the `failure` types.

**Data model (mcp/queue):**

```sql
PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS jobs (
  id           TEXT PRIMARY KEY,
  script_path  TEXT NOT NULL,
  demo_name    TEXT NOT NULL,
  languages    TEXT NOT NULL,              -- JSON array, resolved at submit (BR-002)
  status       TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
  created_at   TEXT NOT NULL,              -- RFC 3339 UTC with nanoseconds
  started_at   TEXT, finished_at TEXT,
  outputs_json TEXT, error_json TEXT
);
CREATE INDEX IF NOT EXISTS jobs_status_created ON jobs(status, created_at);
-- dequeue:  SELECT ... WHERE status='queued' ORDER BY created_at, rowid LIMIT 1
-- position: SELECT COUNT(*)+1 FROM jobs WHERE status='queued' AND (created_at, rowid) < (?, ?)
```

`created_at` uses a fixed-width UTC layout with 9 fraction digits (`RFC3339Nano` trims zeros and breaks text ordering, ADR-57), and `rowid` breaks ties, so back-to-back submissions within the same second still keep FIFO order. The PRD's "RFC 3339" is satisfied. `mcp/queue` holds statuses as typed constants. The worker passes the stored `languages` as `LangOverride`, so a render uses the selection made at submit time.

**ffmpeg command (assembler, sketch; confirmed in M3):**

```
ffmpeg -y -i rec.webm -i c1.wav -i c3.wav \
  -filter_complex "[0:v]fps=30,scale=1920:1080,format=yuv420p[v];
                   [1:a]adelay=1234:all=1[a1];[2:a]adelay=5678:all=1[a2];
                   [a1][a2]amix=inputs=2:normalize=0:dropout_transition=0,apad[a]" \
  -map "[v]" -map "[a]" -shortest -c:v libx264 -preset veryfast -crf 20 \
  -c:a aac -b:a 160k -metadata title=… -metadata comment=… -movflags +faststart out.mp4
```

- `apad` + `-shortest` makes output duration = video length (FR-009.4).
- With zero narrated steps, `anullsrc=r=22050:cl=mono` is used as the audio input.
- ffprobe (in the same package) returns `durationMs` for the result.

**Piper (tts, sketch; confirmed in M3 spike S3):** `piper --model <voice>.onnx --output_file <out.wav>`, with the text on stdin. The binary path and voice path come from constructor args. Duration is read from the WAV `fmt` + `data` chunks with `encoding/binary` (ADR-49).

**Dockerfile stages:**
- `dev` (M1):
  - `golang:1.25-bookworm` + golangci-lint v2.12.0 (newest release that builds on Go 1.25; v2.13+ needs 1.26).
  - Extended in M2: playwright-go CLI built from a pinned version, then `playwright install --with-deps chromium`.
  - Extended in M3: Piper 2023.11.14-2 tarball (sha256-pinned) → `/opt/piper`; voices `en_US-ryan-high` and `pl_PL-darkman-medium` (onnx + json, pinned HF revision, sha256) → `/opt/piper/voices`; `ffmpeg`.
- `build` (M4): `CGO_ENABLED=0 go build` produces `screencaster`, `screencaster-mcp` and the playwright CLI.
- `runtime` (M4):
  - Base `debian:bookworm-slim`. Contents: ffmpeg, Piper + voices, both binaries, `playwright install --with-deps chromium` run via the copied CLI.
  - `WORKDIR /work`, no ENTRYPOINT, because the PRD command passes the binary name.
  - Driver/browser paths are passed to `core/browser` via constructor args wired in `main`. No package reads env vars.

**Makefile:** `make test` runs `go test -race ./...` per module dir inside `screencaster-dev` with the repo mounted at `/src`. The same pattern applies to `lint`, `vet` and `e2e`. `make e2e` runs `go test -tags e2e -race ./...` in `tests/e2e` inside the dev image (which has Chromium + Piper + ffmpeg by M3). `make image` builds the runtime target.

**depguard rules (`.golangci.yml`):**
- `core/**`: deny `modernc.org/sqlite`, `github.com/modelcontextprotocol/go-sdk`, `screencaster/cli`, `screencaster/mcp`.
- `core/**` except `core/{tts,assembler,browser}`: deny `os/exec`.
- `core/**` except `core/browser`: deny `github.com/mxschmitt/playwright-go`.
- `cli/**`: deny `screencaster/mcp`, the MCP SDK and SQLite.
- `mcp/queue/**`: deny the MCP SDK.

### Fit with Project Docs

- **ARCHITECTURE.md:**
  - Packages follow §3, and the dependency rules are enforced by depguard.
  - Pipeline rules §4.1–4.5, the timing model §5, lock §6.3, error model §9 and queue §10 are implemented as written.
  - **Deviations:**
    - Schema file moved to `core/script/` (§3; `go:embed` restriction).
    - 4th module `tests/e2e` (§3, user choice).
    - e2e local only (§13, user choice).
    - FIFO tie-break by `rowid` + nanosecond timestamps (§10 addition).
- **CODE_QUALITY.md:**
  - **KISS:**
    - stdlib/OS first: flock via `x/sys/unix`, `os.Rename`, `encoding/binary`, `os/exec`, `database/sql`.
    - One long-lived goroutine (the worker).
    - **Deviation:** cobra (user choice). Recorded in the KISS section.
  - **YAGNI:**
    - No retries.
    - No interface without a test seam. `Synthesizer`, `Recorder` and `Assembler` are declared in renderer; `Page` is declared in executor.
  - **DRY:**
    - The table is honoured.
    - The schema path in the table changes to `core/script/script.schema.json`.
    - Guard tests: the example script validates, and built-in voice names match the image (`docker build` + e2e).
  - **Separation:** playwright-go appears only in `core/browser`; `os/exec` only in the three wrappers.
- **Docs to update:**
  - PRD:
    - Glossary Job/Step wording.
    - FR-002 embed wording + schema path.
    - FR-014 lock mention.
    - §18 repo structure (4 modules, schema path).
    - Decisions 44–50 already present (verified in PRD §20). Add 51–55 below.
  - ARCHITECTURE:
    - §3 layout.
    - §10 tie-break.
    - §13 e2e local.
    - §16 decisions 51–55.
    - §17: tick each spike with its result.
    - §18: remove fixed items.
  - CODE_QUALITY:
    - KISS (cobra).
    - DRY table (schema path).
    - Enforcement (e2e local via `make e2e`).
    - Known debt as it appears.
  - README:
    - Go 1.25.
    - `go.work` is committed (drop the `go work use` line).
    - Dev commands → `make`.
    - Add `.screencaster/` to the target project's `.gitignore`.
    - `.mcp.json` entry with `--init`.
  - `.gitignore`: remove `go.work`, `go.work.sum`; add `.screencaster/`, `/output/`.

**New decisions to record:**

| # | Decision | Alternatives | Rationale |
|---|---|---|---|
| 51 | Commit `go.work` | Ignore + generate in CI/Docker | One source of truth for CI, image, contributors |
| 52 | Dev Docker image from M1; all make targets run in it | Host Go install | Host has only Docker; one toolchain everywhere |
| 53 | cobra for CLI | std `flag` | User choice; documented KISS deviation |
| 54 | e2e in `tests/e2e` module, local only (`make e2e`) | In-module e2e; e2e in CI | User choice; keeps CI fast |
| 55 | Schema at `core/script/script.schema.json` | Root `schema/` | `go:embed` cannot reference parent dirs |

### Alternative Approaches Considered

- **YAML parser:**
  - **goccy/go-yaml (chosen):** strict decoding, YAML→JSON for jsonschema, path/line info for messages, actively maintained.
  - **gopkg.in/yaml.v3 + sigs.k8s.io/yaml:** common, but no line info, and yaml.v3 is unmaintained.
  - Go has no stdlib YAML.
- **Dev environment:**
  - **Dev image from M1 (chosen):** no host installs; the same image grows into the e2e environment.
  - **Host Go:** faster unit loop, but two toolchains.
- **CLI parsing:**
  - **cobra (chosen, user):** help/completion; one dependency.
  - **std flag:** zero dependencies, ~20 lines; this is what CODE_QUALITY wanted.
- **E2E location:**
  - **`tests/e2e` module (chosen, user):** one place, can build and exec the CLI binary.
  - **In-module `//go:build e2e`:** no extra module, but scattered.
- **CLI timing:**
  - **M3 only (chosen):** no throwaway stub; M2 recording is driven from an e2e test.
  - **M2 skeleton:** earlier feel, but would be reworked.
- **MCP verification:**
  - **In-memory transport + DB-seeded recovery + manual Claude Code run (chosen):** deterministic.
  - **Subprocess stdio e2e:** catches framing bugs, but slow and flaky on kill timing. The stdout risk is mitigated by unit tests instead (below).
- **Audio placement:**
  - **Offline `adelay` + `amix` (chosen, ARCHITECTURE §5).**
  - **Live playback:** rejected; not deterministic.
  - **`concat`:** wrong semantics for overlapping offsets.
- **Explore snapshot:**
  - **ARIA snapshot + derived unique selectors (ADR-47).**
  - **Fallback if spike S4 fails:** DOM walk via `page.Evaluate`, computing role and name from the accessibility properties.

## Checkpoints (Todo List)

- [x] **M1** (steps 1–13): FR-001/FR-002 tests green, `make test|vet|lint` clean (CI pending: no remote configured yet)
- [x] **M2** (steps 14–21): fixture recording works; S1/S2 in ARCHITECTURE §17; `make e2e` green — 54 s, 5 e2e tests pass; S1 drift now 2 ms with compensation
- [x] **M3** (steps 22–30): `screencaster render` EN and EN+PL pass ffprobe, drift ≤ 100 ms, NFR-001; S3 recorded — e2e 7 tests green (151 s), drift 37 ms, NFR-001 ratio 1.87; lint/vet/test clean
- [x] **M4** (steps 31–35): runtime image builds, `make image-check` and runtime e2e pass — e2e-runtime 2 tests green, host.docker.internal render ok; dev-image, vet, lint, test clean
- [ ] **M5** (steps 36–43): MCP tools/queue tests green, S4 recorded, manual Claude Code run done — steps 36–42 done: S4 recorded, lint/vet/test clean, explore e2e 2 tests green, scripted stdio run on the runtime image (EN, PL, explore, 3 queued jobs one at a time, `docker kill` → `interrupted`) ok. Open: step 43 manual Claude Code run (`/mcp__screencaster__create_demo`); `make e2e` TestRender_cli FAIL: NFR-001 ratio 2.09–2.22 (render_test.go:213) under host load avg ~12 on 8 cores, render path untouched, re-run on an idle machine

## Implementation Steps

### M1 — Skeleton, config, schema (FR-001, FR-002; CI)

1. **Docs:**
   - Fix PRD glossary, FR-002, FR-014 and §18 repo structure.
   - Add decisions 51–55 to the PRD and ARCHITECTURE §16.
   - Update README for Go 1.25, committed go.work and make targets.
2. **`.gitignore`:** drop `go.work`, `go.work.sum`; add `.screencaster/`, `/output/`.
3. **Dockerfile `dev` stage:** `golang:1.25-bookworm` + golangci-lint v2.
4. **Makefile:** `dev-image`, `test`, `vet`, `lint` (per module, in the container).
5. **Workspace:**
   - `go.work` (go 1.25; core, cli, mcp, tests/e2e).
   - Four `go.mod` files with paths `screencaster/core|cli|mcp|e2e`.
   - Placeholder `main.go` in cli/mcp so the modules build.
6. **`core/failure`:** types + constructors + `Error()`. Tests cover exact PRD message formats.
7. **`core/config`:**
   - `Load` with defaults (`outputDir=output`).
   - Exact errors: `config not found: <abs path>`, `baseUrl is required`, absolute http/https check, storageState existence.
   - YAML parsed strictly (unknown keys rejected).
8. **`core/script`:**
   - `script.schema.json` (draft 2020-12, `additionalProperties:false` everywhere).
   - Per-action requirements via `if/then` on `action` (each `if` also requires `action`, so a missing action gives one error), with `unevaluatedProperties:false` on the step so fields foreign to the action are rejected. `schemaErrors` drops the artifacts a failed `then` leaves behind.
   - `languages` has no `minItems`/`uniqueItems`: an empty list counts as absent and `Languages` dedupes.
   - `scroll`/`wait` exclusivity via `oneOf`.
   - Language-code pattern `^[a-z]{2}$`.
   - `Parse`, `Validate` (narration per selected language with JSON pointer `/steps/<i>/narration`), `Languages`, `SchemaJSON`.
9. **`core/voices`:** `Discover`, `Lang`, `Resolve`. Built-in map `{en: en_US-ryan-high, pl: pl_PL-darkman-medium}`. Error `voice not installed: <name>`.
10. **`renderer.Prepare`** (validation only at this stage) + `OutputName`. Unit tests with a fake `Installed`.
11. **`testdata/scripts/`:**
    - Valid: default langs, en+pl, every action.
    - Invalid: unknown field, empty steps, missing `narration.pl`, scroll with both selector and y, wait ms=0, bad name, bad lang code.
    - Example script `core/script/example.yaml`, also embedded for the tool description.
12. **`.golangci.yml`** with the depguard rules above.
13. **`.github/workflows/ci.yml`:** setup-go 1.25 + golangci-lint-action (v2.12.0) + `go vet` + `go test -race` per module (matrix). No Docker job yet.

**DoD:** sample scripts pass/fail as specified; FR-001/FR-002 AC tests are green; CI is green.

**Status (2026-10-03):** steps 1–13 implemented. `make test|vet|lint` pass locally. The CI workflow has not run yet. Step 1: the PRD glossary and FR-014 already had the fixes, so only FR-002, §18, the testing/CI rows and decisions 51–55 changed. The depguard `os/exec` rule is verified; the sqlite, MCP SDK, playwright and cli/mcp rules get verified once those imports exist. No `go.work.sum` yet (nothing needed it).

### M2 — Recording without audio (FR-004, FR-005, FR-006, FR-008)

14. **Dev image:** add the pinned playwright-go CLI + `playwright install --with-deps chromium`.
15. **`testdata/fixture-app/`:**
    - `index.html`: shows `#logged-in` only if `document.cookie` has `sc_session=fixture`, so the cookie must be non-HttpOnly.
    - `projects.html`: "New project" button → form with name input, a select and a submit.
    - `marker.html`: button `#marker` that toggles the full viewport to white for 1 s on click.
    - Plus `testdata/storageState.json`.
16. **Spike S1 (gate): recording lead-in.**
    - Method: record `marker.html`, click at a known offset, then find the white frame with ffmpeg (`blackframe` on the inverted video, or `signalstats` YAVG jump).
    - Pass: |frame time − recorded offset| ≤ 100 ms → no compensation; note the value in ARCHITECTURE §17.
    - Fail: add `recorder.leadInCompensation` (constant, with a comment citing ADR-46) and repeat until it passes.
17. **Spike S2 (gate): strict locators.**
    - Method: click/hover/fill a selector matching 2 elements.
    - Pass: playwright-go returns a strict-mode error naming the count → use it directly.
    - Fail: the executor pre-checks `Count()==1` and builds the message `selector matched N elements`.
    - Record the result in §17.
18. **`core/browser`:**
    - `Launch(ctx, opts)` with viewport 1920×1080, optional `RecordVideo{Dir, Size}`, `StorageStatePath`, `BaseURL` and init scripts.
    - Implements `executor.Page` (all calls with Timeout 30 s).
    - `Close()` returns the video path after the context closes.
    - Cursor overlay JS is embedded here, because it is injected only when `Visuals` is on.
19. **`core/executor`:**
    - Single dispatch `switch s.Action`.
    - URL resolution: absolute stays as-is; otherwise `baseURL` + path.
    - Visuals: `MoveTo(sel, 25)` before click/hover/fill/select; fill types with a 60 ms delay (explore mode delay 0).
    - Errors are wrapped as `failure.Step`.
    - Table tests with a fake `Page` (one case per action, plus mode differences and URL resolution).
20. **`core/recorder`:**
    - `Record(ctx, Input{Steps, Clips map[int]time.Duration, Lang, Dir})`.
    - Takes `t0` just before page creation; for each step: offset, run, then wait `max(actionEnd, start+clip)` (ctx-aware timer).
    - Returns `{WebmPath, Offsets []time.Duration}`.
    - `now`/`sleep` are injected.
    - Tests:
      - `TestRecord_narratedStepWaitsForClip` (FR-007 AC1 with a fake clock).
      - `TestRecord_unnarratedStepContinuesImmediately`.
      - `TestRecord_abortClosesBrowser`.
21. **E2e (`tests/e2e/record_test.go`):**
    - httptest serves the fixture; a silent recording is made.
    - Asserts the webm exists and the logged-in marker is visible (storageState works).
    - Missing selector at step 4 → `Failure{Step:4, Lang:"en"}` within 31 s, and the temp dir is gone.

**DoD:** PRD M2 DoD; spikes S1/S2 recorded in ARCHITECTURE §17.

**Status (2026-10-03):** steps 14–21 implemented. Deviations from the plan:
- **playwright-go path:** `github.com/mxschmitt/playwright-go` v0.6201.1. The project moved from `playwright-community` at v0.6100.0 and the old path stops at v0.6000.0. The depguard rule and the dependency table use the new path.
- **`browser.Session` has `Start()` and `Abort()`.** `Launch` opens browser and context but no page, so the recorder takes `t0` right before `Start()` (ADR-46). `Abort()` stops the driver instead of closing the context: closing a recording context encodes the video first and took ~14 s after a 35 s recording, which broke the 31 s limit of FR-008. Abort took 85 ms and left no Chromium process. The recorder aborts on any failure or cancel and closes only on success.
- **`executor.New` returns `(*Executor, error)`** (it parses `baseUrl`).
- **`Recorder` is a struct** with injected `Launch`, `Now`, `Sleep`; `recorder.New` wires the real clock. `Clips` and `Offsets` are indexed by 0-based step index; failures use 1-based step numbers.
- **Dev image:** also gets ffmpeg now (S1 reads frames), `GOPATH=/cache/go` (the sumdb cache needs a writable GOPATH for `--user`), and `HOME=/tmp`. `make e2e` and `go vet -tags e2e` moved up from M3 step 30; golangci-lint runs with `build-tags: [e2e]`.
- **Temp-dir removal after an abort** is owned by `renderer.Render` (`defer os.RemoveAll`), so it is asserted in M3 (`TestRender_cancelRemovesTempDir`), not in the M2 e2e test.
- **S1 result:** video time 0 is ~90 ms after `t0` (52–129 ms over 10 runs, ±40 ms per sample). Compensation added: `recorder.LeadInCompensation = 90ms` is subtracted from every offset, clamped at 0 (user decision, option a).
- **S2 result:** playwright-go locators are strict. An ambiguous selector fails in ~10 ms with `strict mode violation: locator('a') resolved to 2 elements`, for click, hover, fill and moveTo. The executor needs no `Count()` pre-check.

### M3 — TTS, sync, assembly, CLI (FR-003, FR-007, FR-009, FR-010, FR-011)

22. **Dev image:** add Piper 2023.11.14-2 + both voices (sha256-pinned) + ffmpeg.
23. **Spike S3 (gate): Piper flags and WAV format.**
    - Run `piper --model … --output_file …` with stdin text; inspect the header (expect PCM s16le, 22050 Hz mono).
    - Check stdout is empty or capturable, and whether `--espeak_data` is needed.
    - Pass: the WAV-header duration matches ffprobe within 1 ms → ADR-49 stands.
    - Fail (non-PCM or odd chunks): fall back to ffprobe for duration, inside `core/tts`. Update ADR-49.
24. **`core/tts`:**
    - `Piper{Bin string}.Synthesize`: `exec.CommandContext`, stdin text, captured stdout/stderr, `wavDuration(path)`.
    - Unit tests: WAV header parsing on generated bytes; non-zero exit → error with stderr.
25. **`core/assembler`:**
    - `FFmpeg{Bin, Probe string}.Assemble(ctx, Input{Webm, Clips []Clip{Path, Offset}, Title, Comment, Out})`.
    - Builds args via a pure function `buildArgs(in) []string`; unit tests compare it to golden args (including zero clips and missing meta).
    - Returns `durationMs` via ffprobe.
    - Errors keep the last 20 stderr lines.
26. **`renderer.Render`:**
    - Prepare → `runDir := <work>/.screencaster/tmp/<runId>`, with `defer os.RemoveAll`.
    - Per language: tts (`failure.TTS` on error), record, assemble.
    - Publish with one timestamp from `d.Now()`, taken at job start: `os.Rename`, falling back to copy to `<name>.part` + rename on `EXDEV`.
    - Pre-check that the target does not exist; `os.Link`-style no-overwrite is not needed, because the timestamp is unique per job.
    - Tests with fakes:
      - `TestRender_validationFailsBeforeAnyWork`.
      - `TestRender_plFailureDiscardsEn` (BR-004).
      - `TestRender_publishesWithSharedTimestamp` (FR-010).
      - `TestRender_neverOverwrites` (BR-006).
      - `TestRender_cancelRemovesTempDir`.
27. **`core/lock`:** `TryAcquire` (`LOCK_EX|LOCK_NB`) and `Acquire` (poll every 500 ms with ctx). Integration tests use two fds in the same test process (flock is per open file description).
28. **`cli`:**
    - cobra root + `render`.
    - Wiring in `main`: config work dir = cwd; tool paths come from constants for the image layout (`/opt/piper/bin/piper`, `/opt/piper/voices`, `/work/voices`, ffmpeg from PATH).
    - Tries the lock, calls `renderer.Render` with a progress printer, prints paths, maps errors to exit 1.
    - Unit test: `--lang en,pl` parsing + the exit-code mapping with a fake render func.
29. **E2e (`tests/e2e/render_test.go`):**
    - Builds the `screencaster` binary.
    - Temp work dir with config (`baseUrl` = httptest URL), storageState and a script covering every action + a `marker.html` narrated step.
    - Runs the default render (EN) and `--lang en,pl`.
    - Asserts:
      - exit 0;
      - file names match the pattern; both languages share the timestamp;
      - ffprobe codec/size/fps/aac (NFR-002);
      - meta tags set;
      - no `.screencaster/tmp/*` left;
      - NFR-001 ratio;
      - drift: flash frame (`signalstats`) vs narration onset (`silencedetect`) − Piper lead silence. Lead silence is measured by synthesizing the same text with `core/tts` and running `silencedetect` on the clip.
    - Failure case: a missing selector → exit 1, output dir unchanged.
30. **Makefile `e2e` target.** Update ARCHITECTURE §17 (S3) and CODE_QUALITY Enforcement.

**DoD:** PRD M3 DoD.

**Status (2026-10-04):** steps 22–30 implemented. `make e2e` green (151 s, 7 tests). The EN+PL render took 40.2 s for 21.5 s of video (NFR-001 ratio 1.87), and drift was 37 ms. Deviations from the plan:
- **Piper path:** the release tarball is flat, so the binary is `/opt/piper/piper` (libs and `espeak-ng-data` sit next to it), not `/opt/piper/bin/piper`. Voices come from HF revision `c10ece1a…`. Everything is sha256-pinned in the Dockerfile.
- **S3 result:** passes. Output is PCM s16le, 22050 Hz mono, with a plain `fmt ` + `data` layout. The header duration matches ffprobe exactly. No `--espeak_data` flag is needed. Piper prints the output path on stdout, so stdout goes to the null device. ADR-49 stands.
- **ffmpeg:** `apad` + `-shortest` never terminates in ffmpeg 5.1, so it was dropped. The clips are mixed unpadded, and the output length is still the video length, because the recorder makes the video cover all audio. With zero clips, `anullsrc` is mapped directly with `-shortest`. Recorded in ARCHITECTURE §5.
- **`core/lock`** uses stdlib `syscall.Flock`, not `golang.org/x/sys/unix` (KISS, no new dependency).
- **`recorder.Input`** gains `BaseURL`, `StorageState` and `OnStep`. `Launch` takes `recorder.LaunchOptions`, because config is known only after `Prepare` runs inside `Render`. `recorder.New(launch)` no longer takes `baseURL`.
- **`renderer.Deps`** also carries `Voices voices.Installed`, which main discovers. `Request.Progress` is in place. `executor.Target` is exported, so progress lines and failures share one target rule.
- **Publish** removes already-moved files if a later move fails, which keeps BR-004 intact at the publish step.
- **Step 30** (Makefile `e2e`) was already done in M2. The e2e test bounds each CLI run at 4 min.
- **Flaky S1:** `TestRecord_leadInIsWithinTolerance` read −121 ms once, while lint and unit containers were loading the CPU, and 40 ms when run alone. Run `make e2e` on an idle machine.

### M4 — Docker image (FR-016 image part)

31. Dockerfile `build` + `runtime` stages (see Key Changes). Runtime runs as root with `/work`; the `--init` flag is documented.
32. Guard check `make image-check`: `docker run --rm screencaster ls /opt/piper/voices` must list the built-in voice names from `core/voices`. The runtime image has no Go, so this is a shell check, not a Go test.
33. Run `make e2e` against the runtime image as well. Add a `RUNTIME=1` variant that executes the CLI binary inside the runtime image with the fixture served from the dev container on a shared docker network.
34. Manual check of FR-016 AC2: a host app on :3000 with `--add-host=host.docker.internal:host-gateway`. Steps documented in README.
35. CI: add a `docker build --target runtime` job (no push).

**DoD:** PRD M4 DoD.

**Status (2026-10-04):** steps 31–35 implemented. `make image-check` passes; `make e2e-runtime` passes (2 CLI tests, 112 s incl. build; EN+PL ratio 1.91, drift 43 ms). FR-016 AC2 checked by hand: a render of a `python3 -m http.server :3000` host app through `--add-host=host.docker.internal:host-gateway` exited 0 and wrote the MP4. Deviations from the plan:
- **Stages:** a shared `piper` stage feeds both `dev` and `runtime`, so they run the same Piper bytes. `runtime` is last, so `docker build .` yields it.
- **Playwright CLI in the runtime image** is built from the version `core/go.mod` pins (`go build github.com/mxschmitt/playwright-go/cmd/playwright`), then removed after `playwright install --with-deps chromium`.
- **Step 33 changed (ADR-56):** the e2e test binary is compiled in the dev image and runs inside the runtime image (`SCREENCASTER_BIN` selects the image's binary), instead of a shared docker network. Same coverage, no orchestration.
- **Step 34:** the AC2 steps are in the README Docker section. Files in `output/` are owned by root (the container runs as root).
- **Step 35:** CI `image` job runs `make image` and `make image-check`.

### M5 — MCP server & queue (FR-012…FR-019)

36. **Spike S4 (gate): `Locator("body").AriaSnapshot()` on the fixture.**
    - Pass: the output is YAML-like `- role "name"` lines that a mapper can parse → ADR-47 stands.
    - Fail: an `Evaluate` DOM walk producing role/name pairs.
    - Record the result in §17.
37. **`core/explorer`:**
    - `Explore(ctx, cfg, url, actions)`: fresh non-recorded context, executor `Mode{Visuals:false}`.
    - Snapshot post-processing: for interactive roles (button, link, textbox, checkbox, radio, combobox, menuitem, tab, option), append `role=<r>[name="<n>"]`.
    - Uniqueness via a `Count` call; append `>> nth=<i>` on collision.
    - Cap at 50 000 chars with `truncated`.
    - Package-level `sync.Mutex` serializes calls (FR-017).
   - Done: initial navigation is step 0, actions are steps 1..n; actions are checked by `script.ValidateSteps`.
    - Unit test: the mapper on canned snapshots (via a `Page`-like fake).
    - E2e: the selector from explore works in a render (FR-017 AC3), in `tests/e2e/explore_test.go`.
38. **`mcp/queue`:**
    - `Open(path)` (DDL + pragmas), `Recover()` (queued/running → failed `{message:"interrupted"}` + `rm -rf tmp/*`), `Insert`, `Get`, `Position`.
    - Worker `Run(ctx, render func(ctx, Job) ([]Output, error), acquire func(ctx) (release, error))`, woken by a channel with cap 1, also draining on start.
    - Tests:
      - `TestQueue_fifoOrder` (3 jobs, same-second timestamps).
      - `TestQueue_positionCountsEarlierQueued`.
      - `TestRecover_marksInterrupted` (pre-seeded DB).
      - `TestWorker_jobStaysQueuedWhileLockHeld` (FR-014 AC2).
      - `TestWorker_oneAtATime`.
39. **`mcp/server`:** (done; tool errors are returned errors, so the SDK sets `isError` without empty structured output)
    - go-sdk server with 4 tools + the `create_demo` prompt.
    - `render_video`: `renderer.Prepare` synchronously, ValidationErrors → tool error, else insert.
    - `get_options`: `voices.Discover` + config + valid `demos/*.yaml`.
    - Prompt text embedded from `create_demo.md` with the description interpolated.
    - `slog` → stderr.
40. **`mcp/main.go`:**
    - Load config lazily per call, so a missing config is a tool error, not a startup crash.
    - Order: Open DB → Recover → start worker → `server.Run(ctx, stdio)`; ctx is cancelled on SIGTERM/EOF.
    - Stdout hygiene test: run the server's handlers with `os.Stdout` swapped for a pipe and assert zero bytes outside the transport.
41. **Integration tests (`mcp/server/server_test.go`)** over the go-sdk in-memory transport:
    - tools/prompts listed (FR-016 AC1 names);
    - invalid script → tool error, no row;
    - second job while one is running → `position:1`;
    - unknown job → `job not found: <id>`;
    - `get_options` cases from FR-018 AC (temp voice dirs);
    - `prompts/get` includes the description + instructions 1–7.
42. README: the `.mcp.json` entry (`docker run -i --rm --init --add-host=… -v <project>:/work screencaster screencaster-mcp`).
43. Manual run from Claude Code covering the PRD M5 DoD: EN, then PL; explore; `/mcp__screencaster__create_demo`; 3 queued jobs; `docker kill` mid-job + restart → `interrupted`.

**DoD:** PRD M5 DoD.

## Risks & Mitigations

- **Recording lead-in skews audio sync.**
  - Mitigation: spike S1 measures it before any audio work; the drift e2e guards it permanently.
  - Fallback: a fixed compensation constant (ADR-46). No trimming.
- **VFR WebM breaks the offset ↔ video-time mapping.**
  - Mitigation: `fps=30` in the filter graph (ADR-50); the drift e2e checks it with a real flash.
  - Fallback: `-vsync cfr` + `setpts=PTS-STARTPTS` if the first-frame PTS is non-zero.
- **Piper leading silence or speech onset fools the drift test.**
  - Mitigation: calibrate the lead silence per clip with `silencedetect` and subtract it.
  - Fallback: make the marker narration start with a plosive word for a sharp onset.
- **Ambiguous selectors silently click the first match.**
  - Mitigation: strict locators (S2) and the explorer uniqueness check + `nth`.
- **MCP stdout corruption.**
  - Mitigation: all subprocess output is captured; `slog` goes to stderr; playwright-go driver output is checked in S4 (set driver stdout/stderr to captured writers via its options); stdout-swap unit test.
- **Leaked Chromium/Piper processes or temp dirs on abort or SIGTERM.**
  - Mitigation: `exec.CommandContext`; `defer browser.Close` + `os.RemoveAll`; `--init` in docs; recovery removes `tmp/*`.
  - Test: `TestRender_cancelRemovesTempDir`.
- **FIFO ties for jobs created in the same second.**
  - Mitigation: Fixed-width nanosecond timestamp + `rowid` tie-break (ADR-57); covered by `TestQueue_fifoOrder`.
- **Archived Piper binary or HF voice URLs disappear.**
  - Mitigation: pin by sha256.
  - Fallback: vendor the tarball/voices into a release asset (later).
- **The dev image gets slow to rebuild** (Chromium + Piper layers).
  - Mitigation: order layers from least to most changing; the source is mounted, not copied, in the dev stage.

## Test Strategy

**Unit** (fakes for `Page`, `Synthesizer`, `Recorder`, `Assembler`; injected clock and runID):
- config: FR-001 AC1 `baseUrl is required`, AC2 default voices via `voices.Resolve`, missing file message.
- script: FR-002 AC1–3 + every invalid sample; the example script validates.
- voices: precedence, `Lang("pl_PL-darkman-medium")=="pl"`, uninstalled → error, unselected langs not checked.
- failure: exact message formats.
- executor: per action + Mode differences + FR-005 AC (URL join).
- recorder: FR-007 AC1/AC2 timing with a fake clock.
- tts: WAV parsing.
- assembler: golden args.
- renderer: pipeline order, abort, publish, naming (FR-010 AC).
- queue: FIFO, position, recovery (FR-015 AC), lock wait.

**Integration:** SQLite on a temp file; flock across two fds; MCP via the in-memory transport (FR-012/013/016 AC1/018/019 AC).

**E2E (local, `make e2e`, dev image; plus runtime image in M4):**
- FR-004 AC (one and two recordings), FR-006 cursor (visual check of the saved artifact), FR-008 AC (31 s, output unchanged), FR-009 ffprobe, FR-010 AC, FR-011 AC.
- NFR-001 ratio, drift ≤ 100 ms, FR-017 AC1/AC3.

**Manual:** FR-016 AC2 (host.docker.internal); M5 DoD in Claude Code; watch one EN and one PL video for cursor and typing quality.

## Success Checklist

- [ ] All success criteria met (with evidence)
- [x] `golangci-lint`, `go vet`, `go test -race` clean in core, cli, mcp, tests/e2e
- [ ] `make e2e` green in the dev image and against the runtime image
- [x] Spikes S1–S4 recorded in ARCHITECTURE §17
- [ ] Code review approved (screencaster-review)
- [ ] Docs updated: PRD fixes + decisions 51–55, ARCHITECTURE §3/§10/§13/§16/§17/§18, CODE_QUALITY KISS/DRY/Enforcement/Known debt, README, .gitignore
- [ ] No regressions in existing demo scripts (none exist yet; `testdata/scripts` serve as the baseline)

## Timeline & Estimates

- M1: ~8 h · M2: ~16 h · M3: ~20 h · M4: ~6 h · M5: ~24 h
- **Total: ~74 h** of focused work. This assumes the spikes pass first time; each spike fallback adds ~2–4 h.

## Open Questions

- [ ] None blocking. The spike outcomes (S1–S4) may change ADR-46/47/49 as described in their fallback branches.
