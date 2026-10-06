# Architecture Roadmap (15 review items) — Implementation Plan

## Summary
A design review produced 15 items: port boundaries in `core/renderer`, duplicated wiring in both mains, a stringly-typed step model, queue/DTO leaks, no real layering, a guessed lead-in (`LeadInCompensation`, wrong in ~1/3 of recordings), no drift check in CI, serial TTS. This roadmap orders all 15 so every checkpoint leaves the build green: quick doc items first, then the Group A refactor in the current paths, then a purely mechanical move to one module with `internal/` layering, then the sync marker + CI drift gate, then the TTS pool.

Groups: **A** architecture (items 1–10), **B** Playwright (11), **C** sync marker + CI drift (12–13), **D** README + TTS pool (14–15).

## Success Criteria
- `core/renderer` imports none of `core/recorder`, `core/assembler`, `core/browser`, `core/provider/*` (later: `internal/app/renderer` imports nothing under `internal/adapters`), enforced by depguard.
- `cli` and `mcp` mains contain no `browser.Launcher`, `assembler.FFmpeg`, `recorder.New` or `cards{}`; both call `wire.NewDeps`. The "two copies" known-debt entry is deleted.
- One `go.mod` (`module screencaster`), no `go.work`; layout `cmd/`, `internal/{domain,app,adapters}`; `make lint` fails if a domain package imports an adapter (proved by a throwaway violating import during review).
- Demo YAML uses the keyed step form; executor has no `*s.Y`/`*s.Ms` derefs and no `executor.Target` string switch; an old-form script gets one clear hint.
- MCP jobs render the script snapshot stored at submit time; `get_render_status` and `jobs.error_json` JSON is byte-for-byte unchanged (golden tests).
- `LeadInCompensation` is gone; every recording is aligned on a detected marker; e2e drift ≤ ±40 ms, run in a CI job on every push/PR.
- Narration for one language synthesizes on `min(4, NumCPU)` workers; clip paths, durations and the chosen failure are identical to sequential (fake-TTS test); log line shows the speedup.

## Scope & Constraints
- **In scope:** all 15 items; schema break for steps (item 6); SQLite column add (item 8); new CI job (item 13); ARCHITECTURE/CODE_QUALITY/PRD/README updates.
- **Out of scope:** new TTS providers, new step actions, explore/render browser semaphore (Decision 44), `go install` distribution (`specs/release-distribution.md`), converter tool for old YAML.
- **Hard constraints:** BR-001 determinism (script + voices → same steps, offsets, clip placement), NFR-001 render-time ratio (marker adds ~2 s per language — must stay within the ratio), NFR-003 offline, BR-008 one render at a time, BR-004 abort semantics, MCP stdout hygiene.
- **Trade-offs:**
  - Ports + mapping adapters (item 1) cost some type duplication (`renderer.RecordRequest` vs `recorder.Input`) — accepted for a renderer that names no tool package.
  - Breaking YAML (item 6) over compatibility — one developer, few demos; repo demos/testdata migrated, users get a hint.
  - 2 s marker hold (item 12) costs render time to make detection robust to the bimodal start.
  - Real e2e in CI (item 13) costs CI minutes (~5–10 min) to actually guard drift.

## Architecture & Design

### High-Level Flow
Final layout (after CP5):
```
cmd/screencaster/           CLI main + render.go (cobra)         → app/wire, app/renderer
cmd/screencaster-mcp/       MCP main                              → app/wire, app/jobs, adapters/mcpserver, adapters/sqlite
internal/domain/            pure rules, no I/O tools
  script/ voices/ failure/ card/ executor/ recorder/
internal/app/               use cases, depend on ports only
  renderer/                 Prepare (plan.go), Render (render.go), publish (publish.go); ports: Synthesizer, Recorder, Assembler, Cards, Files
  explorer/
  jobs/                     Worker, Job, Status, Store port (was mcp/queue worker half)
  wire/                     composition root: NewDeps, TTS factory, port→adapter mapping (only app pkg allowed to import adapters)
internal/adapters/
  browser/ assembler/ lock/ osfs/ sqlite/ mcpserver/
  tts/ (Engine) tts/piper/ tts/wav/
tests/e2e/                  same module, //go:build e2e
```
Render with sync marker (CP6):
```
recorder: t0 → sess.Start() [page shows magenta marker over start image]
          hold 2 s → sess.HideMarker() (remove + double rAF) → tOff = now()
          offset_i = start_i − tOff                    (no LeadInCompensation)
assembler: probe first 4 s of webm → vOff = pts of first non-marker frame
          no marker frame / no end → ErrNoMarker → failure.SyncMarker(lang)
          trim webm at vOff, CFR transcode, place clips at offset_i (+ intro)
```

### Key Changes

**Item 14 — README** (`README.md:117`): "Deterministic: no LLM at render time (BR-001)" → "Deterministic script: the same YAML and voices give the same steps, timing and narration placement; no LLM at render time (BR-001). Pixels and audio bytes may differ between runs (browser rendering, encoder, TTS build)." Mirror the scope in PRD BR-001 wording if it claims more.

**Item 11 — Playwright (verified, no migration).** Go proxy: `github.com/mxschmitt/playwright-go` and `github.com/playwright-community/playwright-go` both serve `v0.6201.1` from the same commit (`b4b4642`), and from v0.6100 the upstream `go.mod` declares `module github.com/mxschmitt/playwright-go`. Requiring the playwright-community path at v0.6201.1 fails with "module declares its path as …". The project moved *back* to mxschmitt; our import is already the canonical one. Action: add Decision 66 + keep the Dockerfile comment; re-check on every playwright-go bump (known-debt note).

**Item 4 — split `core/renderer/renderer.go`** (pure move, no logic change):
- `plan.go`: `Request`, `Plan`, `Card`, `Prepare`, `resolveCard`, `sniffImage`, `resolve`, `within`, `outsideWorkDir`, `ScriptPath`.
- `publish.go`: `publish`, `move`, `copyThenRename`, `OutputName`, `timestampLayout`.
- `render.go`: ports, `Deps`, `Output`, `Render`, `renderLanguage`, `buildCards`, `reporter`, `voiceNames`.
- Tests split the same way (`plan_test.go`, `publish_test.go`, `render_test.go` already exists).

**Items 2 + 3 — composition package `core/app`** (renamed `internal/app/wire` in CP5):
```go
package app

// TTS builds the provider named by SCREENCASTER_TTS (was provider.FromEnv).
func TTS(getenv func(string) string, workDir string) (provider.Engine, error)

// NewDeps wires the real tools around eng. Catalog is read here, per call,
// so a voice added to /work/voices is picked up per render/job.
func NewDeps(eng provider.Engine, runID func() string) (renderer.Deps, error)

// Explorer returns the explore_page launcher (was mcp/main.go launchExplorer).
func Explorer() explorer.Explorer
```
- `NewDeps` holds the one launch closure, the `cards` adapter, `assembler.FFmpeg{Bin: "ffmpeg", Probe: "ffprobe"}`, `time.Now`.
- `core/provider` keeps only `Engine`; `FromEnv`'s switch moves to `app.TTS`, so `provider` no longer imports `provider/piper`. `tests/e2e` `assertDrift` calls `app.TTS`.
- Signature differs from the review's `NewDeps(cfg)`: the MCP server needs the engine at startup (`server.Deps.Voices`) while the CLI builds it per render, so the engine is built separately and passed in.

**Item 1 — renderer ports own their types** (`core/renderer/render.go`):
```go
type RecordRequest struct {
    Steps        []script.Step
    Clips        map[int]time.Duration
    Lang, Dir    string
    BaseURL      string
    StorageState *script.StorageState
    StartImage   string
    OnStep       func(i int)
}
type Recording struct { Video string; Offsets []time.Duration }
type Clip struct { Path string; Offset time.Duration }
type Still struct { Path string; Duration time.Duration }
type AssembleRequest struct {
    Video          string
    Clips          []Clip
    Intro, Outro   *Still
    Title, Comment string
    Out            string
}
type Recorder  interface { Record(ctx context.Context, r RecordRequest) (Recording, error) }
type Assembler interface { Assemble(ctx context.Context, r AssembleRequest) (int64, error) }
```
`core/app/adapters.go` holds `recorderPort{recorder.Recorder}`, `assemblerPort{assembler.FFmpeg}`, `cardsPort{}` with field-by-field mapping. Renderer imports drop `core/assembler` and `core/recorder`.

**Item 5 — filesystem port** (declared by the consumer, `core/renderer/files.go`):
```go
// Files is the file system the plan and publish steps touch (test seam).
type Files interface {
    ReadFile(name string) ([]byte, error)
    Stat(name string) (fs.FileInfo, error)
    Lstat(name string) (fs.FileInfo, error)
    Open(name string) (io.ReadCloser, error)              // sniffImage, copy
    CreateExcl(name string) (io.WriteCloser, error)       // O_EXCL, BR-006
    MkdirAll(name string, perm fs.FileMode) error
    Rename(oldname, newname string) error                 // may return syscall.EXDEV
    Remove(name string) error
    RemoveAll(name string) error
}
```
- `Prepare(req Request, catalog voices.Catalog, files Files)`; `Deps.Files Files`; `publish` and the temp-dir `MkdirAll`/`RemoveAll` in `Render`/`renderLanguage` go through it.
- OS adapter: `core/osfs` (`osfs.FS{}`), wired by `app.NewDeps`; MCP server gets it in `server.Deps` for its `Prepare` call.
- Test fake `renderer/filestest_test.go`: an `fstest.MapFS` (keys = path without leading `/`) mutated for writes, plus a hook to make `Rename` return `syscall.EXDEV` → the copy-then-rename path gets unit coverage without a second filesystem.

**Item 6 — typed actions, keyed YAML** (breaking):
```yaml
steps:
  - goto: /login
  - fill: { selector: "#email", value: demo@example.com }
    narration: { en: "Sign in with the demo account." }
  - click: "button[type=submit]"
  - select: { selector: "#lang", value: pl }
  - press: Enter                       # or { key: Enter, selector: "#q" }
  - hover: ".card"
  - scroll: { y: 400 }                 # or scroll: ".footer"
  - wait: 500                          # ms, integer; or wait: ".toast"
```
`wait` takes an integer (ms) rather than the `500ms` string from the review sketch: JSON type alone tells a duration from a selector, with no pattern rule. Change it to `500ms` during review if preferred.
```go
// core/script/step.go
type Step struct { Action Action; Narration map[string]string }
type Action interface { Name() string; Target() string } // Target: URL or selector, "" if none
type Goto struct{ URL string }
type Click struct{ Selector string }
type Hover struct{ Selector string }
type Fill struct{ Selector, Value string }
type Select struct{ Selector, Value string }
type Press struct{ Key, Selector string }
type ScrollTo struct{ Y int }
type ScrollInto struct{ Selector string }
type WaitFor struct{ Selector string }
type Pause struct{ D time.Duration }
```
- Schema: `$defs/step` = object with optional `narration` plus `oneOf` of ten single-key action shapes (`minProperties`/`maxProperties` keep exactly one action key). The JSON Schema stays the format's one source of truth.
- `Step.UnmarshalJSON` runs after schema validation and switches on the action key. `Step.MarshalJSON` keeps the round trip for tests.
- `executor.dispatch` becomes a type switch; `executor.Target` is deleted; `renderer` progress uses `step.Action.Name()`/`Target()`; `failure.Step` takes them the same way.
- Old-form hint: in `script.Parse`, when a step object has an `action` key, add `{Pointer: "/steps/<i>", Message: "old step form: use the action as the key, e.g. `- click: \"#id\"` (see README)"}` once, ahead of the schema errors.
- `explore_page`: `exploreIn.Actions` becomes `[]json.RawMessage`. The tool input schema's `actions.items` is set from the embedded `$defs/step` (no struct inference over an interface), and the handler calls a new `script.ParseSteps(raw)`. That replaces `ValidateSteps` and fixes the "empty `fill` value dropped" known debt.
- Migrate: `demos/*.yaml`, `demos/cli-demo/demo.yaml` (+ `demo.rc` if it prints YAML), `testdata/scripts/**` (rename `scroll-selector-and-y`, `wait-ms-zero` to the new invalid cases), `core/script/example.yaml`, YAML literals in `tests/e2e/*_test.go`, README, PRD FR-001/FR-005 examples.

**Item 7 — worker** (`mcp/queue/worker.go`): `NewWorker(store *Store, render RenderFunc, acquire AcquireFunc) *Worker`; `Run(ctx)`, `drain(ctx)`, `runOne(ctx, job)` read the fields. `mcp/main.go` builds it once.

**Item 9 — DTOs:**
- `failure.Failure` loses its JSON tags (pure domain type).
- `mcp/queue` adds private `failureRecord` / `outputRecord` with today's tags and field names, plus `toRecord`/`fromRecord`. `error_json`/`outputs_json` stay byte-identical.
- `queue.Output` is deleted; `Job.Outputs []renderer.Output`; the render func returns `[]renderer.Output`, so `renderJob`'s copy loop disappears.
- `mcp/server` adds `outputOut` and `errorOut` with the current JSON names for `statusOut`. Golden-JSON tests pin both the stored and the tool shapes.
- Chosen canonical result: `renderer.Output`, because the pipeline produces it and both consumers only reshape it.

**Item 8 — job snapshot:**
- `renderer.Request` gains `Script []byte`. When it is set, `Prepare` parses those bytes instead of reading `ScriptPath`; `ScriptPath` still anchors the demo folder and names the script in messages.
- `render_video` reads the file once through `Files`, calls `Prepare` with the bytes, and stores them in `NewJob.Script`. The worker passes `job.Script`.
- DDL: `script TEXT` (nullable). `Open` adds it when `PRAGMA table_info(jobs)` lacks it (`ALTER TABLE jobs ADD COLUMN script TEXT`).
- No backfill: `Recover` already fails every queued/running job at start, so no live job has a NULL. A NULL at run time fails the job with "job has no script snapshot".
- Documented limit: card images and `storageState` files stay on disk and are re-read at run time. Only the YAML is snapshotted.

**Item 10 — one module + `internal/` + depguard** (last in Group A, mechanical apart from splitting `mcp/queue`):
- Root `go.mod` `module screencaster`, `go 1.25`, union of the four modules' requirements; delete `go.work`, `go.work.sum`, and the 4 sub-`go.mod`/`go.sum` files.
- `git mv` per the layout above; `mcp/queue` splits into `internal/app/jobs` (Worker, Job, Status, a 3-method store port `next/start/finish`) and `internal/adapters/sqlite` (Store, schema, records, `RemoveStaleTemp`).
- Dockerfile `build` stage: `go build ./cmd/...` from the root, `go build -o /out/playwright github.com/mxschmitt/playwright-go/cmd/playwright` from the root. `PLAYWRIGHT_GO_VERSION` is now read from the root `go.mod`.
- Makefile: drop per-module loops. CI `check` job: one job, no matrix.
- `.golangci.yml` rules (file globs):
  - `domain-pure`: `**/internal/domain/**` must not import `screencaster/internal/app`, `screencaster/internal/adapters`, `screencaster/cmd`, `os/exec`, playwright-go, sqlite or the MCP SDK.
  - `app-ports-only`: `**/internal/app/**`, excluding `**/internal/app/wire/**`, must not import `screencaster/internal/adapters`, `os/exec`, playwright-go, sqlite or the MCP SDK.
  - `exec-only-in-wrappers`: only `adapters/tts/piper`, `adapters/assembler` and `adapters/browser` may use `os/exec`.
  - `playwright-only-in-browser`.
  - `sqlite-only-in-adapter`: only `adapters/sqlite`.
  - `mcp-sdk-only-in-server`: only `adapters/mcpserver` and `cmd/screencaster-mcp`.
  - `cli-no-mcp`: `cmd/screencaster` must not import `adapters/mcpserver`, `adapters/sqlite` or the MCP SDK.
- `tests/e2e` joins the root module and keeps `//go:build e2e`.

**Item 12 — sync marker:**
- `adapters/browser`: `Options.Marker bool`. When set, the recorded page's start document gets a full-viewport `#FF00FF` div above the start image. `Session.HideMarker() error` removes it and awaits a double `requestAnimationFrame`, so the call returns after the paint without the marker. The explorer never sets `Marker`.
- `domain/recorder`: `Session` gains `HideMarker`. `MarkerHold = 2 * time.Second` (covers the 0.5–1.5 s late start, §17.1) is spent via the injected `Sleep`, so fake-clock tests stay instant. Then `tOff := r.Now()`, `offsets[i] = max(start_i − tOff, 0)`. Delete `LeadInCompensation` and the t0 lead-in comment.
- `adapters/assembler`: `markerEnd(ctx, webm) (time.Duration, error)` runs `ffmpeg -t 4 -i webm -vf scale=1:1:flags=area,showinfo -f rawvideo -pix_fmt rgb24 pipe:1`. It reads one RGB triple per frame from stdout and the `pts_time` values from stderr, and returns the pts of the first non-marker frame after at least one marker frame (marker = each channel within ±40 of FF/00/FF). Otherwise it returns `ErrNoMarker`. `Assemble` trims the webm input at that time (`-ss` on the input) before the CFR transcode.
- `domain/failure`: `SyncMarker(lang string) *Failure` with the message `sync marker not found in recording (<lang>)`. `renderer` maps `assembler.ErrNoMarker` (surfaced through the adapter as a sentinel the port declares: `renderer.ErrNoMarker`) to it. There is no fallback.
- `Recording` stays `{Video, Offsets}`, because the offsets are already marker-relative.

**Item 13 — CI drift gate:**
- `tests/e2e/render_test.go`: `maxDrift` 150 ms → 40 ms. Delete the "wider than PRD" comment.
- `.github/workflows/ci.yml`: new `e2e` job on push/PR that runs `make dev-image PROVIDER=piper`, then `make e2e`. It uses the Docker layer cache (`docker/setup-buildx-action` + `cache-from: type=gha`).
- This supersedes Decision 54's "local only" (new Decision 68).

**Item 15 — TTS pool** (`internal/app/renderer/render.go`, private `synthesizeAll`):
- `sem := make(chan struct{}, min(4, runtime.NumCPU()))`, a `sync.WaitGroup`, and `ctx, cancel := context.WithCancel(ctx)`.
- Each worker writes `durations[i]`, `paths[i]` or `errs[i]` into slices pre-sized by step index, so no lock is needed. The first real error calls `cancel()`.
- After `Wait`: when the parent ctx was cancelled, return `ctx.Err()`. Otherwise return `failure.TTS` for the **lowest** step index with a non-cancellation error, the same failure the sequential loop would report.
- Clip paths stay `clips/<i+1>.wav`. Standard library only, no `errgroup` (CODE_QUALITY KISS).

### Fit with Project Docs
- **ARCHITECTURE.md:**
  - §3 rewritten for the new layout and dependency rules.
  - §4 diagram: participant names, plus the parallel TTS line.
  - §5 timing: marker replaces t0 lead-in; drift target ±40 ms (tighter than FR-007's ±100 ms).
  - §6.2: TTS goroutines are short-lived and inside one render, an exception to "one long-lived goroutine" that stays true.
  - §9: `SyncMarker` kind added to the table.
  - §10: `script` column, snapshot semantics.
  - §12: build from the root.
  - §13: CI now runs e2e.
  - §15: provider path `internal/adapters/tts/<name>`, factory `wire.TTS`.
  - §16: new decisions 66–70:
    - 66: stay on mxschmitt path.
    - 67: one module, `internal/` layering, supersedes 51.
    - 68: e2e drift in CI, supersedes 54 in part.
    - 69: marker alignment, supersedes 46.
    - 70: keyed step form.
  - §17.1 closed.
- **CODE_QUALITY.md:**
  - DRY table:
    - Step format → `$defs/step`.
    - Failure shape → domain type + records in sqlite/mcpserver.
    - Provider choice → `wire.TTS`.
  - Separation table: rewritten per package.
  - YAGNI test-seam list: add `Files`.
  - Working agreements "API surface": `internal/` replaces "core packages are exported".
  - Known debt, remove:
    - two copies of wiring
    - lead-in 90 ms
    - ±150 ms drift bound
    - empty `fill` value
    - `PLAYWRIGHT_GO_VERSION` "core/go.mod"
    - sqlite pin "mcp/go.mod"
  - Known debt, add:
    - card images not snapshotted
    - re-check the Playwright module path on bumps
- **KISS/YAGNI:**
  - `Files` and the port types are test seams, which CODE_QUALITY allows.
  - The pool uses plain stdlib.
  - No converter tool.
  - Bent rule: item 1 adds mapping duplication (DRY in letter), justified by the boundary. Note it in §3.
- **PRD:** §13 jobs DDL, FR-001/FR-005 YAML examples, BR-001 wording.

### Alternative Approaches Considered
- **Item 2 `NewDeps(cfg)` building the engine inside** — rejected: the CLI must serve `--help` without a provider and the MCP server needs the catalog at startup; separate `TTS()` + `NewDeps(eng, …)` covers both.
- **Item 5 `fs.FS` read + separate writer / afero** — rejected (user): `fs.FS` cannot write; afero is a dependency for one seam. Own interface chosen.
- **Files port living in `core/app`** — rejected: `app` imports `renderer`, so `renderer` importing `app` is a cycle; ports are declared by the consumer (CODE_QUALITY ISP).
- **Item 6 keep flat `action:` + discriminator** — rejected (user chose keyed form). **`wait: 500ms` string** — possible, needs a pattern to tell it from a selector; integer chosen.
- **Item 8 document re-read / hash check** — rejected (user): snapshot gives predictable jobs.
- **Item 10 per-module `internal/` or depguard-only** — rejected (user): one module gives real `internal/` enforcement.
- **Item 10 composition = all of `internal/app`** — rejected (user): `app` holds use cases, `app/wire` holds the composition root.
- **Item 11 migrate to playwright-community** — not possible (module path verified above).
- **Item 12 short flash / adaptive hold** — rejected: a short flash is missed in the late mode; an adaptive hold needs frame events Playwright does not expose. 2 s fixed hold (user).
- **Item 12 detect via `signalstats`/`blackdetect`** — weaker: they measure luma/saturation, not a specific colour; the 1×1 RGB downscale is exact and cheap.
- **Item 13 keep local / nightly** — rejected (user): gate on every push.
- **Item 15 `errgroup.SetLimit`** — cleaner, but adds a direct dependency; ~25 lines of stdlib suffice.

## Implementation Steps
1. [x] **README + PRD wording** (item 14).
2. [x] **Playwright decision** (item 11): Decision 66 in ARCHITECTURE §16, known-debt line; no code.
3. [x] **Split renderer.go** (item 4): move code into `plan.go`/`publish.go`/`render.go`; split tests; zero diff in behaviour.
4. [x] **`core/app` + TTS factory** (items 2, 3): add `app.TTS` (move `FromEnv` + its tests), `app.NewDeps`, `app.Explorer`; `core/provider` keeps `Engine` only; rewrite `cli/main.go` `renderWith` and `mcp/main.go` `renderJob`/`launchExplorer`; e2e uses `app.TTS`; remove `provider/piper` from the `provider` imports; add `core/app` to `.golangci.yml` exec/playwright allow-lists only if needed (it shouldn't: it calls wrappers).
5. [x] **Renderer ports** (item 1): add port types to `render.go`, switch `Recorder`/`Assembler` signatures, add `core/app/adapters.go` mapping; renderer fakes in tests use the new types.
6. [x] **Files port** (item 5): `renderer.Files`, `core/osfs`, `Deps.Files`, `Prepare(…, files)`; MCP server `Deps.Files`; MapFS fake + EXDEV test for `copyThenRename`; publish rollback test.
7. [x] **Typed actions** (item 6): schema `$defs/step`; `script/step.go` types + (un)marshal; `ParseSteps`; old-form hint; executor type switch, delete `Target`; recorder/renderer/explorer/failure call sites; explore_page raw actions + schema from `$defs/step`; migrate demos, testdata, example, e2e literals, docs.
8. [x] **Worker** (item 7): `NewWorker(store, render, acquire)`, `Run(ctx)`; tests.
9. [x] **DTOs** (item 9): golden JSON tests first (capture today's `error_json`, `outputs_json`, `get_render_status` output); then strip tags from `failure.Failure`, add records/out types, delete `queue.Output`; goldens unchanged.
10. [x] **Job snapshot** (item 8): `Request.Script`; `Prepare` bytes path; `script` column + `table_info` migration; `NewJob.Script`/`Job.Script`; server stores bytes; worker passes them; tests: edit file after enqueue → job renders the original; NULL snapshot → failed job.
11. [x] **One module + layering** (item 10): root `go.mod`; `git mv` into `cmd/`, `internal/…`; split `mcp/queue` into `app/jobs` + `adapters/sqlite`; rename `core/app` → `internal/app/wire`, `core/osfs` → `internal/adapters/osfs`, `core/provider` → `internal/adapters/tts`, `core/support/wav` → `internal/adapters/tts/wav`; rewrite imports (`gofmt -r` / `sed` + `goimports`); new depguard rules; Dockerfile, Makefile, CI, compose paths; delete `go.work*` and sub-modules.
12. [ ] **Sync marker** (item 12): browser `Marker` + `HideMarker`; recorder hold + marker-relative offsets, delete `LeadInCompensation` and `TestRecord_leadInIsWithinTolerance`; assembler `markerEnd` + trim + `ErrNoMarker`; `failure.SyncMarker`; renderer mapping; unit tests with fake session/clock; assembler test over a generated clip (`ffmpeg -f lavfi color=magenta…,color=white…`) in the dev image; spike-calibrate on 10 fixture renders (log `vOff`, drift) before step 13.
13. [x] **CI drift gate** (item 13): `maxDrift = 40 * time.Millisecond`; `e2e` CI job with layer cache.
14. [x] **TTS pool** (item 15): `synthesizeAll`; tests: fake TTS with random per-call sleeps → same durations/paths as sequential; two failing indexes → lowest reported; parent cancel → `ctx.Err()`; max in-flight ≤ pool size (atomic counter in fake).
15. [x] **Docs sweep:** every section listed in "Fit with Project Docs"; update `docs/demo.gif` only if the visible output changed (it shouldn't).

## Checkpoints (Todo List)
- [x] CP0 — Doc-only items: steps 1–2; README line changed, Decision 66 present, `go build ./...` untouched. — baseline lint/vet/test green
- [x] CP1 — Renderer split, composition, ports: steps 3–5; `go list -deps ./core/renderer | grep -E 'core/(recorder|assembler|browser|provider)'` empty; `grep -n piper core/provider/provider.go` empty; `grep -nE 'browser\.Launcher|assembler\.FFmpeg|recorder\.New' cli mcp -r` empty; `make lint vet test` green. — greps empty, lint/vet/test green; mapping tests in `core/app/adapters_test.go`
- [x] CP2 — Files port: step 6; `grep -nE '\bos\.' core/renderer/{plan,publish,render}.go` empty; EXDEV + rollback tests green; `make test` green. — renderer tests run on an in-memory fake; lint/test green
- [x] CP3 — Typed actions: step 7; `grep -rn 'action:' demos testdata core/script/example.yaml tests` empty; `grep -n '\*s\.\(Y\|Ms\)\|func Target' core/executor` empty; old-form hint test green; explore_page e2e with typed actions green (`make e2e`). — greps empty; explore + CLI e2e green; only `TestRecord_leadInIsWithinTolerance` failed (109 ms vs 100 ms, the known bimodal lead-in, removed in CP6)
- [x] CP4 — Queue: steps 8–10; golden JSON tests unchanged across the change; snapshot test (file edited after enqueue) green; `grep -n 'type Output' mcp/queue` empty; `make test` green with `-race`. — goldens captured first (SDK text uses sorted keys), unchanged after; old-DB migration test green; lint/vet/test green
- [x] CP5 — One module + layering: step 11; `ls go.work core cli mcp` all absent; `head -1 go.mod` = `module screencaster`; `make lint vet test image image-check e2e e2e-runtime` green; a throwaway `import _ "screencaster/internal/adapters/browser"` in `internal/domain/script` makes `make lint` fail (then reverted). — lint/vet/test, image, image-check (after path fix), e2e-runtime green; e2e green except `TestRecord_leadInIsWithinTolerance` (178 ms, known lead-in, deleted in CP6). depguard proof done in `internal/domain/voices` (in `domain/script` the import is a cycle error, not depguard)
- [ ] CP6 — Sync marker + CI drift: steps 12–13; `grep -rn LeadInCompensation .` empty; 10 consecutive local fixture renders all within ±40 ms; NFR-001 ratio assertion still green; CI `e2e` job green on the PR. — IN PROGRESS: step 12 code done, calibration 5/10 (drift 28, 33, 32, 31 ms, then 64 ms: outside ±40 ms)
- [ ] CP7 — TTS pool + docs + final: steps 14–15 (both done; pool speedup only ~1.07×, `ttsWorkers = 1`); pool tests green under `-race`; log shows narration time drop on the EN+PL e2e; all "Fit with Project Docs" sections updated; `screencaster-review` pass.

### Risks & Mitigations
- **Marker alignment residual > 40 ms** (Playwright WebM is ~25 fps VFR, so a frame boundary alone is up to 40 ms; plus compositor latency).
  - Mitigation: double-rAF before stamping `tOff`. Calibrate on 10 renders (step 12) and correct any constant bias, measured, as a named constant with its evidence.
  - Mitigation: if the spread itself exceeds ±40 ms, stop and bring the numbers to the user. Options: gate at FR-007's ±100 ms, or raise the recording fps.
- **Marker missed** (video starts > 2 s late, or a page paints over the overlay before removal).
  - Mitigation: the overlay is top-layer (`z-index` max, `position: fixed`) on the start document, before any `goto`.
  - Mitigation: the render fails loudly with `SyncMarker` (user choice). Log `vOff` on every render to spot drift toward the 2 s edge.
- **Item 6 breaks every existing script and the explore_page tool contract.**
  - Mitigation: one hint message; migrate all repo YAML in the same step; the `render_video` description is derived from the embedded schema + example (existing drift test).
  - Mitigation: the explore_page input schema is built from `$defs/step` with a test that the example's steps validate against it.
- **Module merge breaks the image or CI in non-obvious places** (Dockerfile `cd core`, golangci path globs, `cache-dependency-path`, `PLAYWRIGHT_GO_VERSION` read from `core/go.mod`).
  - Mitigation: CP5 requires `make image image-check e2e-runtime`; grep for `core/`, `cli/`, `mcp/` paths in Dockerfile/Makefile/CI/compose before closing.
- **DTO change silently alters MCP/stored JSON.**
  - Mitigation: goldens written *before* the change (step 9). An old `jobs.db` row must still decode (fixture row in test).
- **Parallel Piper: RAM/CPU contention** (each process loads its `.onnx`; onnxruntime is already multi-threaded, so the speedup may be small).
  - Mitigation: measure in CP7. If the speedup is under 1.3× on the e2e script, report it and let the user decide whether to keep the pool. Pool size is a constant, easy to set to 1.
- **CI e2e flakiness/cost.**
  - Mitigation: layer cache; the drift test logs all measured values; a flake becomes data for the calibration, not a retry (no retries, CODE_QUALITY).

## Test Strategy
- **Unit:**
  - plan/publish against the MapFS fake: EXDEV copy path, rollback, BR-006 existing target.
  - Port mapping in `wire` adapters (each field crosses).
  - Step parsing: one valid and one invalid case per action; old-form hint; `fill` with empty value accepted.
  - Executor type switch with a fake `Page`.
  - Recorder: marker hold + marker-relative offsets on a fake clock; `HideMarker` error → abort.
  - Worker with injected funcs; snapshot rendering; NULL snapshot.
  - Golden JSON for `error_json`, `outputs_json`, `get_render_status`.
  - TTS pool: order, lowest-index failure, cancellation, in-flight bound.
  - Assembler `markerEnd` parser over canned rgb/stderr bytes.
- **Integration:** SQLite `table_info` migration on a pre-change DB file; MCP in-memory transport: explore_page with raw typed actions, render_video storing the snapshot.
- **E2E (dev image, now also CI):** EN and EN+PL fixture renders; ffprobe format checks; drift ≤ ±40 ms; NFR-001 ratio; explore selector reused in render; generated magenta→white clip detects at the expected pts; `make e2e-runtime` after CP5.
- **Manual:**
  - Render `demos/portfolio-projects.yaml` via CLI and MCP; watch the first second for any magenta frame.
  - Run an old-form script and read the hint.
  - Edit a demo after `render_video` while the job is queued; the output follows the snapshot.

## Success Checklist
- [ ] All success criteria met, with the evidence listed in each checkpoint
- [ ] `golangci-lint`, `go vet`, `go test -race ./...` clean (single module)
- [ ] E2E green locally and in the CI `e2e` job; `make e2e-runtime` green
- [ ] Code review approved (`screencaster-review`)
- [ ] ARCHITECTURE.md, CODE_QUALITY.md, PRD, README updated per "Fit with Project Docs"
- [ ] All repo demos render; no regressions in render-time ratio (NFR-001)

## Timeline & Estimates
- CP0: ~1 h
- CP1: ~5 h
- CP2: ~4 h
- CP3: ~10 h (schema + parser + explore schema + migration)
- CP4: ~6 h
- CP5: ~8 h (moves, queue split, Docker/Make/CI)
- CP6: ~10 h (browser/recorder/assembler + calibration + CI job)
- CP7: ~5 h
- **Total**: ~49 h, plus ~25% buffer → ~60 h. Estimates are rough; adjust to your pace.

## Resume Point (2026-10-06)
Stopped after CP5. Nothing committed; all changes are in the working tree.

**Done:** CP0–CP5. Docs already updated for CP1–CP5: ARCHITECTURE §3, §10, §12 (build line), §15, §16 (decisions 66, 67, 70; 51 marked superseded); CODE_QUALITY (DRY, separation, YAGNI seams, hard rules, API surface, known debt); PRD BR-001, FR-002, FR-005, §13 `script` column, repo structure; README.

**Deviations so far (tell reviewer):**
- `$defs/step` uses one property per action plus an if/then on `narration` for min/max property count, not a `oneOf` of shapes; `schemaErrors` rewrites the count message to "a step needs exactly one action: …" and drops it when an unknown key is the root cause.
- Old-form fixture is inline in `script_test.go`, so `grep action: testdata` stays empty.
- `get_render_status` golden uses sorted keys (the SDK re-marshals structured content).
- Card outro-coverage test moved from `domain/card` to `app/wire` (domain tests may not import adapters).

**Next: CP6 (step 12–13), design notes for resuming:**
- `adapters/browser`: `Options.Marker`; start page gets `<div id=screencaster-marker style="position:fixed;inset:0;z-index:2147483647;background:#FF00FF">`; `Session.HideMarker()` = `page.Evaluate` removing it, then double `requestAnimationFrame`.
- `wire.launchRecording` sets `Marker: true`; `domain/recorder.Session` gains `HideMarker`; after `Start`: `Sleep(MarkerHold=2s)`, `HideMarker`, `tOff := Now()`, offsets `max(start_i−tOff,0)`; delete `LeadInCompensation`, `TestRecord_leadInIsWithinTolerance`; adapt e2e `TestRecord_startsDark` (first frames are now magenta).
- `adapters/assembler`: `markerEnd` = `ffmpeg -hide_banner -nostdin -t 4 -i webm -vf scale=1:1:flags=area,showinfo -fps_mode passthrough -f rawvideo -pix_fmt rgb24 pipe:1`, pair stdout RGB triples with stderr `pts_time`; `-ss vOff` before `-i webm` in `buildArgs` (pass trim as arg, keep it pure); `ErrNoMarker` → wire maps to `renderer.ErrNoMarker` → `failure.SyncMarker(lang)` ("sync marker not found in recording (<lang>)"). Consider returning vOff from Assemble so the renderer can log it.
- CI `e2e` job: buildx needs `FROM screencaster-dev-base` visible across Dockerfiles → use a `registry:2` service + `driver-opts: network=host`, push base to `localhost:5000`, pass `DEV_BASE`, `cache-from/to: type=gha`; then `make e2e`. Add decisions 68, 69; §5 timing, §13 CI, §17.1 closed; README "E2E runs locally only" line.
- Calibrate: run `TestRender_cli` 10× (`-count=10`), log drift; `maxDrift` 150→40 ms.

**Then CP7:** `synthesizeAll` pool in `app/renderer/render.go` (min(4,NumCPU), lowest-index failure, ctx cancel), tests; ARCHITECTURE §4 diagram, §6.2; CODE_QUALITY KISS goroutine note; final `screencaster-review`.

## Open Questions
- [ ] `wait: 500` (integer ms) vs `wait: 500ms` (string) — plan uses the integer; flip during review if preferred.
- [ ] If the CP6 calibration shows a spread wider than ±40 ms, keep the gate at FR-007's ±100 ms or raise the recording fps? — gate set to ±100 ms for now (user, after drift 28/33/32/31/64 ms in 5 renders); fps option and the ~30 ms bias constant still open

## Implementation Log
- Steps 1–11 — done in earlier sessions (CP0–CP5, see Resume Point)
- Step 12 — progress — code + unit tests done (failure.SyncMarker; recorder MarkerHold/HideMarker, LeadInCompensation deleted; browser Marker/HideMarker; assembler markerEnd/findMarkerEnd/-ss cut/ErrNoMarker, Assemble returns Output{DurationMs,MarkerEnd}; renderer Assembly port + ErrNoMarker → SyncMarker, logs marker end; wire mapping; e2e lead-in test deleted); left: 10× calibration
- Step 12 — stopped — code and unit tests done; calibration incomplete: 5 of 10 renders (drift 28, 33, 32, 31 ms, then 64 ms; marker end 1.92–1.96 s). The 64 ms run is outside ±40 ms, so the bias constant and the gate value are undecided. `TestRender_cli` cut to EN only (~45 s); EN+PL NFR-001 check moved to `TestRender_cli_enPl`. ARCHITECTURE/CODE_QUALITY not updated yet (step 15)
- Step 13 — started
- Step 13 — done — files: .github/workflows/ci.yml, tests/e2e/render_test.go, README.md — new `e2e` job (`make dev-image PROVIDER=piper`, `make e2e PROVIDER=piper`, 30 min timeout); `maxDrift` 150 → 100 ms. Deviations: (1) gate is 100 ms (FR-007), not 40 ms, by user choice after one 64 ms run in 5; (2) no Docker layer cache: buildx `type=gha` needs the docker-container driver, which cannot see the locally built `screencaster-dev-base`, so it would also need a registry service; not added because the workflow cannot be run here. The job is untested on GitHub: CP6's "CI e2e job green on the PR" is still open. README "E2E runs locally only" line fixed; ARCHITECTURE §13 and decision 54 still say local only (step 15)
- Step 14 — started
- Step 14 — started
- Step 14 — done — files: internal/app/renderer/render.go (`synthesizeAll`, `ttsWorkers`, `lowerTo`, narration log line), internal/app/renderer/narration_test.go (new, 7 tests), internal/app/renderer/render_test.go, internal/app/wire/adapters_test.go, tests/e2e/render_test.go — pool of `min(4, NumCPU)`; tests green under `-race -count=10`, lint 0 issues. Deviations: (1) no shared cancel on the first error: only later steps are not started, so the lowest failing step is always the one reported (a cancel could cut off a lower step and report a higher one); (2) log line is `[en] narration: N clips in T (W at a time, S of synthesis)`; (3) render tests set `ttsWorkers = 1` because their fakes share an unlocked clock and call log. **Measured speedup is about 1.07×, under the 1.3× bar in Risks** (3 clips, 8 CPUs: pool 4.4 s and 3.7 s, sequential 4.7 s, 4.0 s, 4.6 s, 3.3 s, 3.3 s). The "synthesis" sum under contention reads 2.9×, which overstates it. Piper is already multi-threaded. User choice: set `ttsWorkers = 1` (sequential, less code). Pool code kept for future if a longer script shows 1.3×+ speedup.
- Side fixes found by lint/tests while doing step 14: `internal/app/wire/adapters_test.go` imported `os/exec` (depguard) since step 12, now a fake script file; `TestRender_cli` had lost its FR-004 AC2 default-language run and `TestRender_cli_enPl` the FR-010 timestamp/no-overwrite checks in my earlier speed-up, both restored; `select` in the pool could start a clip after cancel, now checks `ctx.Err()` first
- Calibration data (all runs so far, drift ms): 28, 33, 32, 31, 64, 66, 26, 59, 66, **132**. The 132 ms run fails even the ±100 ms gate. Drift is quantized by the ~33-66 ms frame step. CI `e2e` job will be flaky at 100 ms; ±40 ms is not reachable with the current alignment
- Step 15 — started
- Step 15 — done — files: docs/ARCHITECTURE.md (§4 diagram + rule 3, §5 marker replaces lead-in, §6.2 TTS goroutines, §9 SyncMarker, §13 CI e2e, §16 decisions 68, 69, 71 and 46/54 marked superseded, §17.1 rewritten), docs/CODE_QUALITY.md (comment example, enforcement row, known debt: marker drift, no CI layer cache, unused pool machinery), docs/PRD.md (CI/testing rows, decision log notes on 29, 46, 51, 54, 55), README.md (make lines). Deviations: §17.1 is not "closed" as planned: it records the measured drift (26–132 ms, one run over the ±100 ms gate) as an open item; decision 71 (pool kept, `ttsWorkers = 1`) added beyond the planned 66–70. Left alone: `docs/demo.gif` (visible output unchanged; the marker is cut from the video). README line "same ... timing and narration placement" (item 14) overstates determinism given the measured tens-of-ms jitter
- Finish pass — started: calibration 10x, then full make e2e, e2e-runtime, screencaster-review
