# screencaster — Architecture

**Version:** 0.2 | **Date:** 2026-10-05 | **Based on:** [PRD v1.3](PRD.md) | **Code rules:** [CODE_QUALITY.md](CODE_QUALITY.md) | **Status:** Draft

This document holds the structural rules: how the code is divided, what may depend on what, and the invariants the render pipeline keeps. *What* the product does is in the PRD; constants, formats and measurements live in the code and its tests. Requirement IDs (BR-, FR-, NFR-, BP-) link back to the PRD.

## 1. Context and design drivers

| Driver | Source | Consequence |
|--------|--------|-------------|
| Re-render is deterministic, no LLM at render time | BR-001 | The render engine is a pure function of (script, voices, target app). Claude Code only authors YAML. |
| Narration and action start together; the next step waits for both | BR-003, FR-007 | Audio is never played live. Clips are placed on a timeline by recorded offsets and mixed offline. |
| Any step failure aborts everything, no partial output | BR-004, FR-008, FR-010 | Work in a temp dir, publish only after every language succeeded. |
| Offline, $0 | NFR-003 | No HTTP clients in code. Chromium is the only network user. |
| One developer, few videos | PRD §9 | Few moving parts: one process, one worker, one SQLite file. No scale design. |
| Same selectors in explore and render | Decision 23 | One step executor serves both. |

Non-goals: multi-tenancy, parallel rendering, auth, any UI (PRD §10.3).

## 2. System context

```mermaid
flowchart LR
    Dev([Developer])
    CC[Claude Code<br/>MCP client]
    subgraph Docker["Docker container (/work = mounted project)"]
        MCP[screencaster-mcp]
        CLI[screencaster CLI]
        Core[[internal packages]]
        Chromium[(Chromium)]
        TTS[TTS provider<br/>Piper + voices]
        FF[ffmpeg / ffprobe]
    end
    App[Target app<br/>host:3000]

    Dev -- chat --> CC
    Dev -- docker run ... render --> CLI
    CC -- stdio JSON-RPC --> MCP
    MCP --> Core
    CLI --> Core
    Core --> Chromium
    Core --> TTS
    Core --> FF
    Chromium -- host.docker.internal --> App
```

Two entry points are thin shells over one module's `internal/` packages. They share the pipeline and its wiring (`app/wire`).

## 3. Module view

One Go module (`module screencaster`, Decision 67), layered with `internal/`:

```
cmd/
  screencaster/            CLI: parse args, print progress, exit code        FR-011
  screencaster-mcp/        MCP main: store, worker, server, stdio             FR-012…FR-019
internal/
  domain/                  pure rules: no tool, no process, no SQL
    script/                types, embedded JSON Schema, cross-field rules     FR-001, FR-002
    voices/                resolve a voice per language over a catalog        BR-011, FR-018
    failure/               error types (no JSON tags)                         §9
    card/                  built-in start/end card page                       FR-009
    executor/              step execution (two modes)                         FR-005, FR-006
    recorder/              one language: steps -> video + offsets             FR-004, FR-007
    shooter/               one screenshots run: steps -> PNGs                 FR-020
  app/                     use cases; reach tools only through ports
    renderer/              Prepare (plan.go), Render (render.go), publish (publish.go)  FR-008, FR-010, FR-020
    explorer/              explore_page logic                                 FR-017
    jobs/                  Job, statuses, the single worker, Store port       FR-014, FR-015
    wire/                  composition root: TTS factory, NewDeps, port -> adapter mapping
  adapters/                one external tool or protocol each
    browser/               playwright-go: launch, context, screenshots, overlay.js  FR-004, FR-020
    assembler/             ffmpeg: mix, cards, transcode, mux, tags           FR-009
    tts/                   Engine contract; piper/ (the only Piper code), wav/ (clip length)  FR-003
    sqlite/                jobs table, stored JSON, startup clean-up          FR-014, FR-015
    mcpserver/             MCP tools and prompt                               FR-012, 013, 017, 018, 019, 020
    lock/                  flock-based render lock                            §6.3
    osfs/                  the real file system behind renderer.Files
tests/e2e/                 same module, //go:build e2e
testdata/                  fixture app and sample scripts
providers/<name>/          one TTS provider's image layer: Dockerfile (dev + runtime stages), image-check.sh
```

The JSON Schema sits next to the code that embeds it (`internal/domain/script`), because `go:embed` cannot reach a parent directory (Decision 55).

**Dependency rules**

- `domain` imports nothing from `app`, `adapters` or `cmd`, and no tool library (`os/exec`, playwright-go, SQLite, MCP SDK).
- `app` reaches tools only through ports it declares (`Synthesizer`, `Recorder`, `Shooter`, `Assembler`, `Cards`, `Files` in `renderer`; `Store` in `jobs`). `app/wire` is the one exception: the composition root that maps the ports onto the adapters.
- Only `adapters/tts/piper`, `adapters/assembler` and `adapters/browser` start subprocesses; only `adapters/browser` calls playwright-go; only `adapters/sqlite` opens SQLite; only `adapters/mcpserver` and `cmd/screencaster-mcp` use the MCP SDK. `cmd/screencaster` imports neither the MCP server nor the job store.
- Both mains get the pipeline from `wire.TTS` and `wire.NewDeps`; neither builds a tool itself.
- The renderer's port types (`RecordRequest`, `ShootRequest`, `AssembleRequest`, ...) repeat the wrappers' input shapes on purpose, and `wire` maps them field by field. That is duplication in letter, accepted so `renderer` names no tool package. Unit tests use fakes; only e2e uses the real tools.
- Enforced by `depguard` in `.golangci.yml` (file globs over these paths, test files included).

## 4. Render pipeline

`renderer.Render(ctx, Deps, Request)` is the single code path for CLI and MCP (BR-001, FR-011); the caller is the only difference. This section is the video pipeline; a `type: screenshots` script takes the branch at the end of it. `Deps` holds the tools, the voices, the clock and the run-ID source. `Request.Progress` and `Request.Log` report progress; the wording lives in `renderer`, the caller decides where it goes (stderr, `slog`; Decision 64).

```mermaid
sequenceDiagram
    participant C as Caller (CLI / worker)
    participant R as renderer
    participant V as script+voices
    participant T as tts.Engine (Piper)
    participant B as recorder + executor (Chromium)
    participant A as assembler (ffmpeg)
    participant FS as outputDir

    C->>R: Render(script, langOverride)
    R->>V: parse+validate script, resolve langs & voices, check outputDir
    V-->>R: plan (or ValidationErrors, nothing started)
    loop each language, in order
        R->>T: synthesize clip per narrated step (ttsWorkers at a time)
        T-->>R: wav + durationMs
        R->>B: screenshot built-in cards (one launch)
        R->>B: record(steps, clips)
        B-->>R: webm + stepOffsets
        R->>A: assemble(webm, clips@offsets, intro, outro, meta): cut at the sync marker
        A-->>R: tmp/<lang>.mp4
    end
    R->>FS: move all tmp mp4s -> <name>.<lang>.<ts>.mp4
    R-->>C: []Output
```

Rules:

1. **Validate first.** Everything checkable without a browser or TTS is checked before either starts: schema, `baseUrl`, narration per language, installed voices, paths inside the working directory (BR-011, FR-001, FR-002).
2. **Languages run sequentially**, each from a fresh browser context (FR-004).
3. **TTS before browser**, because clip durations decide step timing (FR-003). `synthesizeAll` makes the clips of one language, at most `ttsWorkers` at a time (1 today, Decision 71); the failure reported is always the lowest failing step. Cards come right after TTS, so a card problem fails before the slow part.
4. **Abort path.** The first step error cancels the context, closes the browser, deletes the temp dir and returns a `Failure`. Nothing reaches `outputDir`; a finished `en` is discarded when `pl` fails (BR-004).
5. **Publish last.** After all languages succeed, files move into `outputDir` with one shared timestamp (FR-010, BR-006). Every target is checked for existence first, existing files are never opened for writing, a cross-filesystem move goes through `<name>.part` then rename, and a failed move removes what the job already moved. Screenshots are the one exception (below).

### Screenshots runs (decisions 72–75)

After `Prepare`, `Render` branches on `Plan.Type`. A screenshots script has no languages, voices, TTS, cards, recording or assembly:

1. **Validate first.** The same rules, plus the type rules in `domain/script` (`checkType`, run by `Parse`, so `explore_page` actions are covered too). `Prepare` skips voices and cards and rejects a language override.
2. **Shoot.** `renderShots` calls the `Shooter` port, which is `domain/shooter`: a fresh, unrecorded browser (no cursor overlay), the ordinary steps through the executor, and `Session.Capture` at each `screenshot` step, writing `NN.png` (`shooter.ShotName`) under `<tmp>/<runId>/shots/`. Capture is in `adapters/browser`: it draws the annotation overlay (`overlay.js`, document coordinates) when asked, takes the shot with animations stopped and the caret hidden, and removes the overlay again.
3. **Publish last, with overwrite.** `publishShots` moves the PNGs into `<outputDir>/<name>/screenshots/`, replacing the previous run's files, then removes the `NN.png` files this run did not write. Other files and the folder itself are never removed, and no existing file is opened for writing (a cross-filesystem move still goes through `<dst>.part`). This is the deliberate exception to rule 5 and BR-006 (decision 73): the paths stay stable for docs that embed the images. A failure while publishing leaves a mix of new and old shots.
4. **Abort path** as for videos: the first failing step aborts the browser, the temp dir goes and nothing reaches `outputDir`.

The queue, worker and render lock (§6) are unchanged: the worker calls `Render`, which branches on the script.

## 5. Timing and synchronization model

The part most likely to go wrong, so it is defined precisely.

```
t0 (end of the sync marker) ─────────────────────────────────► recording time
 │   step1 start        step2 start               step3 start
 │   │ action ▓▓▓       │ action ▓▓                │ action ▓
 │   │ clip   ░░░░░░░░░░│ (no narration)          │ clip ░░░░
 │   └─ next starts at max(action end, start+clip)  └─ next starts at action end
```

- **t0** is a monotonic timestamp taken right after the sync marker is removed from the recorded page (Decision 69). A step's offset is `now − t0`, clamped at 0. The assembler cuts the recording at the same moment, found in the video itself.
- Per step: record the offset, start the action, then wait until `max(actionEnd, offset + clipDuration)` if narrated, else continue at `actionEnd` (BR-003, FR-007).
- Narration is not played while recording. The assembler places each clip at its offset and mixes offline, so sync does not depend on real-time playback.
- The transcode forces a constant frame rate, so video time equals wall time and offsets stay valid (Decision 50).
- The video is `intro card + recording + outro card`. The recorder knows nothing of the cards: its offsets are relative to the recording and the assembler adds the intro length. Every concat input is finite.
- Output length is the video length (FR-009.4). The last narrated step waits for its clip, so the video always covers the audio; the assembler mixes clips without padding.
- Drift target ±100 ms (FR-007), checked by the e2e test with the same bound. Measured over 20 local renders: 25–66 ms in 19, 132 ms in one (§17.1).

**Sync marker (Decision 69):** the video starts a variable time after page creation (bimodal, §17.1), so no constant aligns it. The recorded page therefore opens on a full-viewport magenta (`#FF00FF`) div over the start picture. The recorder holds it for `MarkerHold` (2 s, longer than the latest video start seen, 1.5 s), removes it, waits for two animation frames and takes t0. The assembler reads the first 4 s of the WebM as one averaged RGB pixel per frame, finds the first non-marker frame after a marker frame and cuts the recording there, so video time 0 is the recorder's t0. A recording without the marker fails with the `SyncMarker` failure; there is no fallback. The cut time is logged on every render (`sync marker ends at …`): a value near 2 s warns of a late video start. The marker costs about 2 s per language.

**Start page:** a new page is white until the first `goto` paints, which would flash white after the start card. The recorded page therefore shows the intro picture on the card background first (`recorder.Input.StartImage`), so the card carries on until the site appears; with no intro it is the plain card colour. The sync marker covers it until t0, so the picture is on screen from time 0 of the cut video.

## 6. Concurrency model

### 6.1 Processes

- `screencaster-mcp`: long-lived, one per Claude Code session.
- `screencaster`: short-lived, one per CLI render.
- Both may run at once on the same `/work`.

### 6.2 Inside `screencaster-mcp`

| Goroutine | Role |
|-----------|------|
| MCP server loop | Serves stdio. `render_video` and `take_screenshots` validate, insert a job and wake the worker. `get_render_status` reads SQLite. |
| Queue worker (exactly one) | Takes the oldest `queued` job, acquires the render lock (§6.3), sets `running`, calls `renderer.Render`, stores the result (BR-008, FR-014). |
| `explore_page` handlers | Run on the request goroutine, serialized by a mutex. They do not wait for the worker. |

Inside one render, `synthesizeAll` runs each narration clip in a short-lived goroutine and joins them before it returns; with `ttsWorkers = 1` (Decision 71) one runs at a time. It is the only fan-out in a render and no exception to "exactly one worker" for the queue.

**Explore may overlap a render** (Decision 44). Render timing can jitter, but offsets come from recorded timestamps, so narration stays correct. If it becomes a problem, add a shared browser semaphore; no interface changes.

### 6.3 Render lock

The CLI bypasses the queue (BP-003), so a lock file keeps a CLI render and an MCP job apart (Decision 45).

- `<work>/.screencaster/render.lock`, `flock(LOCK_EX)`; the kernel releases it on exit, so a crash leaves nothing stale. It works across containers sharing the bind mount.
- **CLI:** one non-blocking attempt; if held, print `another render is running` and exit 1.
- **Worker:** blocking acquire before the job becomes `running`; a waiting job stays `queued`.
- `explore_page` does not take the lock.
- MCP startup tries the lock without waiting before it clears `tmp/` (§11); if a CLI render holds it, the next start cleans up.

### 6.4 Shutdown

EOF on stdin or a signal cancels the root context: the browser closes, the temp dir goes, the process exits. The job row stays `running` and recovery marks it `interrupted` at next start (BR-009). Run containers with `--init` so signals are forwarded.

## 7. Step executor (shared)

One implementation of FR-005 semantics, two modes:

| | `render` | `explore` |
|---|---|---|
| Cursor overlay, mouse glide, typing delay | yes | no |
| Records step offsets | yes | no |
| Timeout per action | 30 s | 30 s |

Same selectors, same URL resolution against `baseUrl` (BR-010), same error shape. That is the guarantee behind FR-017 AC3: a selector returned by `explore_page` works in a render.

**Screenshot steps.** `screenshot` is a step action the executor does not run. `domain/shooter` intercepts it, because a capture needs an output path and a counter, which the executor (one step, no files) does not have; validation keeps it out of recordings and `explore_page`, and the executor's default case would return `unknown action` if one slipped through (decision 75). Every other step of a screenshots script runs through the executor in the mode `explore` uses (no visuals).

**Selector strictness.** A selector matching several elements fails instead of clicking the first. The executor uses strict locators, so `explore_page` emits only selectors that match one element and disambiguates with `>> nth=N`.

## 8. `explore_page` design

- Runs in a fresh, non-recorded context with the caller's `storageState`. The `url` is also the `baseURL`, so relative `goto`s in `actions` resolve against the explored page (Decision 59).
- Replays `actions` through the executor in `explore` mode, then captures the page as an ARIA snapshot (Decision 47). Each named interactive line gets a ready-to-use selector, checked for uniqueness against the live page.
- Output is capped (`truncated: true`).
- On an action failure the tool returns `{step, action, target, message}` plus the snapshot at that moment (FR-017). The initial navigation is step 0.

## 9. Error model

One type travels everywhere (`internal/domain/failure`):

```go
type Failure struct {
    Step    *int   // 1-based; nil for non-step failures
    Lang    string
    Action  string
    Target  string // selector or URL
    Message string
}
```

| Kind | Produced by | CLI | MCP |
|------|-------------|-----|-----|
| Validation (list of `{pointer, message}`) | script, voices, renderer | print, exit 1 | tool error, no job created |
| Step | executor, shooter (a capture error is a step failure for the `screenshot` step) | print, exit 1 | `jobs.error_json` |
| TTS, Assembly, Cards, SyncMarker | tts adapter, assembler, renderer | print, exit 1 | `error_json.message` |
| Interrupted | startup recovery | n/a | `error_json` |

Step, TTS, assembly, card and sync-marker failures become a `Failure` before they leave `app`. A missing sync marker comes from the assembler as a sentinel (`ErrNoMarker`), which `renderer` turns into `failure.SyncMarker(lang)`: `sync marker not found in recording (<lang>)`. Messages name the step or phase and the language, and include the tool's stderr tail where there is one.

## 10. Job queue and persistence (MCP only)

SQLite (`modernc.org/sqlite`), file `<work>/.screencaster/jobs.db`; schema in PRD §13. Only the MCP process opens it. The worker and the job shape are a use case (`app/jobs`); the table is an adapter (`adapters/sqlite`) behind its `Store` port.

- FIFO by `(created_at, rowid)`. Timestamps use a fixed-width UTC layout so text order equals time order (Decision 57). A job's `position` counts the `queued` jobs before it plus one; a running job does not count. There is no job-kind column: a screenshots job is an ordinary job with an empty `languages` list, and the renderer tells the kind from the script (decision 75).
- WAL mode with a busy timeout. The worker is woken by a buffered channel on insert, no polling. Recovery runs first at start (FR-015).
- **Script snapshot.** `render_video` and `take_screenshots` read the YAML once, validates those bytes and stores them in `jobs.script`; the worker renders the stored bytes, so editing the file while a job waits changes nothing. `ScriptPath` still anchors the demo folder. Only the YAML is snapshotted: card images are read again at run time. A `jobs.db` from before the column gets it on open (`ALTER TABLE`); its rows keep `NULL`, and a job with no snapshot fails with `job has no script snapshot` (none can be queued: recovery has already failed them).
- **Stored JSON.** `outputs_json` and `error_json` keep their field names; `adapters/sqlite` maps the untagged domain types (`renderer.Output`, `failure.Failure`) onto private records, and golden tests pin the bytes. `get_render_status` has its own output types in `adapters/mcpserver`, also pinned by a golden test.

```mermaid
stateDiagram-v2
    [*] --> queued: render_video / take_screenshots
    queued --> running: worker picks + lock held
    running --> succeeded
    running --> failed: step/tts/assembly error
    queued --> failed: server restart (interrupted)
    running --> failed: server restart (interrupted)
    succeeded --> [*]
    failed --> [*]
```

## 11. File layout

```
/work                          mounted project (rw)
├── demos/*.yaml               scripts: baseUrl, optional storageState and outputDir (FR-001)
├── demos/assets/*             card pictures named by intro.image / outro.image
├── demos/output/              <name>.<lang>.<ts>.mp4, never overwritten
│   └── <name>/screenshots/    NN.png of a screenshots script, overwritten on rerun (decision 73)
├── voices/*                   extra voices for the active TTS provider (Piper: *.onnx + .json, FR-016)
└── .screencaster/
    ├── jobs.db                MCP only
    ├── render.lock
    └── tmp/<runId>/<lang>/    clips, video, cards, out.mp4
```

- Every path in a demo resolves against the demo file's folder and must stay inside the working directory (Decision 62).
- `runId` is the job UUID (MCP) or a random ID (CLI). The temp dir is removed on success and on abort.
- MCP startup removes stale `tmp/*`, only while it holds the render lock (§6.3).
- `.screencaster/` belongs in the project's `.gitignore`.

## 12. Docker image

- **Base plus provider layer (Decision 65).** The root `Dockerfile` names no provider: `dev-base` (toolchain for `make`), `build` (static binaries, `go build ./cmd/...` from the root module) and `runtime-base` (both binaries, Chromium, ffmpeg, no TTS). Each provider is a folder `providers/<name>/` with its own `Dockerfile` whose `dev` and `runtime` stages build `FROM` those bases (`DEV_BASE`, `RUNTIME_BASE` build args) and set the provider ENV, plus an `image-check.sh`. `providers/piper` also holds the `piper` fetch stage (Piper + voices, checksum-pinned), shared by its `dev` and `runtime`.
- **Build order.** `make dev-image` builds `screencaster-dev-base`, then `screencaster-dev` (tags `:<provider>` and latest). `make image` builds `screencaster-base`, then `screencaster:<provider>` and `screencaster` (latest). `PROVIDER` defaults to `piper`. `compose.yaml` only runs the built image, since compose cannot chain the two builds.
- **Provider selection.** The image sets `SCREENCASTER_TTS` (and the provider's own variables: `SCREENCASTER_PIPER_BIN`, `SCREENCASTER_PIPER_VOICES`). `screencaster-mcp` calls `wire.TTS` at startup; the CLI calls it when a render starts, so `--help` and `--version` work without a provider. Either way it fails with a clear message, before any render work, when the name is unset or unknown or the Piper binary is missing. No image path lives in Go code.
- Voice discovery belongs to the provider. The Piper adapter scans the image's voice folder and `/work/voices` on every call; the language is the voice name up to the first `_` (FR-018).
- `--add-host=host.docker.internal:host-gateway` lets a `baseUrl` reach the host app on Linux (Decision 27). The container runs as root, so output files are root-owned.
- **Stdio hygiene.** MCP uses stdout for protocol frames, so all logging goes to stderr and subprocess output (Piper, ffmpeg, the Playwright driver) is captured, never inherited. A stray byte on stdout corrupts the session.
- **Release (Decision 76).** The `release` workflow is started by hand with a version `X.Y.Z`. It cross-compiles `screencaster`, `screencaster-mcp` and the `playwright` CLI for linux amd64 and arm64 into two tarballs plus `SHA256SUMS`, runs `make image VERSION=X.Y.Z` and `make image-check`, pushes `ghcr.io/makuchpatryk/screencaster:X.Y.Z` and `:latest`, and creates the GitHub Release last, so a failed build leaves no half-published release. The version reaches both mains through one `-X main.version` flag (`var version = "dev"` in each); the `VERSION` build arg and `make` variable carry it into the `build` stage. The image is amd64 only (Piper is pinned to `piper_linux_x86_64` in `providers/piper/Dockerfile`); the native binaries need the provider variables, ffmpeg and Chromium installed by the user (README, Install).
- Go tooling runs in the dev image (Decision 52); `make image-check` runs the provider's own `image-check.sh` (for Piper: the built-in voices are in the image).

## 13. Testing strategy

| Level | Scope | Tools |
|-------|-------|-------|
| Unit | schema and cross-field rules, voice resolution, offset math, the wait rule, error formatting, queue ordering, recovery | `go test`, fakes for `Recorder`, `Session`, `Synthesizer`, `Assembler`, `Cards` |
| Integration | SQLite store, lock semantics, MCP wiring over an in-memory transport | `go test` |
| E2E | fixture app → real render (EN, EN+PL), output format, drift, duration ratio (NFR-001), an `explore_page` selector used in a render; a screenshots run (sizes per capture area, annotation pixels, no overlay leak, overwrite and stale removal) | `make e2e` in the dev image; `make e2e-runtime` runs the CLI tests against the runtime image (Decisions 54, 56, 68) |

CI runs lint, vet and `go test -race` (one `check` job), an image build (`image`) and the e2e suite in the dev image (`e2e`, Decision 68). The e2e job has no Docker layer cache, so every run builds the images. The manual `release` workflow (§12) runs `make image-check` but not lint, tests or e2e: dispatch it from a green `main`.

## 14. Security notes

Local single-user tool (PRD §14), so the model is minimal:

- An inline `storageState` holds live session cookies: keep such a demo out of git.
- No outbound network except Chromium to `baseUrl` and absolute `goto` URLs (NFR-003).
- Scripts are data. YAML parses into typed structs, the schema rejects unknown fields, and selectors go only to Playwright. Paths in scripts and tool input are not trusted: they are checked against the working directory (Decision 58).

## 15. Extension points (post-MVP)

- **Slides / overlays** (PRD §10.2): only start and end cards exist for videos (Decision 63). Interleaved slides would change the recorder output and the assembler. Overlays exist for screenshots only (Decision 74: `overlay.js`, drawn by `Session.Capture`); in a video they would hook in as an executor init script, like the cursor.
- **New languages:** no code change, add the voice files under `/work/voices`.
- **Other TTS engines:** one adapter package `internal/adapters/tts/<name>` that satisfies `tts.Engine` (`Catalog`, and `Synthesize` into a PCM WAV), one case in `wire.TTS`, and an image layer: a folder `providers/<name>/` (Dockerfile with `dev` and `runtime` stages on the bases, plus `image-check.sh`; local binary: fetch stage + COPY + ENV; cloud or sidecar: ENV only). No existing file changes except the `wire.TTS` case. Script voice IDs are opaque per provider, so a switch may need `voices:` edits. A network provider must revisit NFR-003 and BR-001 first. Piper is the only adapter today.

## 16. Architecture decisions

Numbering continues the PRD Decisions Log (last: 43). 44–55, 58–60, 62–64 and 72–75 are also in the PRD log; 56, 57, 61, 65–71 and 76 are only here.

| # | Decision | Why |
|---|----------|-----|
| 44 | `explore_page` may overlap a render; no shared browser lock | Offsets come from timestamps, so correctness holds. Revisit if jitter shows. |
| 45 | `flock` file guards CLI against the MCP worker; CLI fails fast, worker waits | Kernel releases on crash; keeps the rule that the CLI has no queue or job record. |
| 46 | `t0` = just before page creation; fixed lead-in compensation, no trimming. Superseded by 69 | Fewer moving parts (§17.1). |
| 47 | `explore_page` = ARIA snapshot plus derived, uniqueness-checked selectors | Supported API; guarantees "selector works in render". |
| 48 | Logs to stderr only, subprocess output captured | Required by MCP stdio. |
| 49 | Clip duration read from the WAV header | No subprocess per clip; exact for PCM WAV. |
| 50 | Constant frame rate forced in the transcode | Video time equals wall time for the offsets. |
| 51 | `go.work` and `go.work.sum` are committed. Superseded by 67 | One source of truth for CI, the image and contributors. |
| 52 | Dev image from the start; every `make` target runs in it | The host needs only Docker; one toolchain everywhere. |
| 53 | `cobra` for the CLI | User choice; a documented deviation from KISS. |
| 54 | e2e in its own module, local only. Superseded by 67 (one module) and 68 (CI) | Keeps CI fast; the pipeline was not verified in CI. |
| 55 | Schema next to the package that embeds it (now `internal/domain/script/script.schema.json`) | `go:embed` cannot reach a parent directory. |
| 56 | `make e2e-runtime`: test binary built in the dev image, run inside the runtime image | Covers the image's binary, Chromium, Piper and ffmpeg with no network or docker-in-docker. |
| 57 | Job timestamps use a fixed-width UTC layout | `RFC3339Nano` trims zeros and breaks text ordering. |
| 58 | Each demo is one self-contained YAML (`baseUrl` required; `storageState`, `outputDir` optional); no project config file. Supersedes PRD decision 15 | No hidden project state. Script and tool-input paths are LLM-written, so `renderer` confines them to the working directory. |
| 59 | `explore_page` takes an absolute `url` and an inline `storageState`; the url is the `BaseURL` | Nothing to read from disk; executor and explorer stay unchanged. |
| 60 | No project-level voice defaults: script, then built-in | Follows from 58. |
| 61 | `storageState` is an inline object in Playwright's shape, not a file path | One self-contained file; no existence or path checks. |
| 62 | Demo paths resolve against the demo's folder and must stay inside the working directory. Supersedes the rejection in 58 | A demo, its videos and its pictures move together. |
| 63 | Every video gets a start and end card unless `false`; built-in cards are an embedded HTML page screenshotted by Chromium, a custom `image` is used as is; the assembler joins them with `concat` and shifts clip offsets by the intro | Chromium wraps text and has the Polish glyphs. Recorded cards would meet the bimodal start (§17.1). |
| 64 | Render log lines come from `renderer` through `Request.Log`; the caller chooses stderr or `slog` | One place for the wording; nothing on stdout. |
| 65 | The TTS provider sits behind `tts.Engine`, chosen by `SCREENCASTER_TTS` at startup; the image is `runtime-base` plus one provider layer, and each layer lives in its own `providers/<name>/` folder, not in the root `Dockerfile` | A new provider is one adapter, one `wire.TTS` case and one folder. Piper details stay in `internal/adapters/tts/piper` and `providers/piper`; the image owns the provider and its paths. |
| 66 | Stay on `github.com/mxschmitt/playwright-go`; no move to `playwright-community` | Both paths serve v0.6201.1 from the same commit, and from v0.6100 the upstream `go.mod` declares the mxschmitt path, so requiring the community path fails with "module declares its path as …". Re-check on every playwright-go bump. |
| 67 | One module (`module screencaster`) with `cmd/` and `internal/{domain,app,adapters}`; no `go.work`. Supersedes 51 | `internal/` gives real import enforcement on top of depguard, and the image, CI and `make` build from one root. |
| 68 | e2e runs in CI on every push and PR (`e2e` job: dev image, then `make e2e`). Supersedes 54 in part | The drift check guards nothing if it never runs. No retries: a flaky run is data for the drift bound. Costs CI minutes; no layer cache yet. |
| 69 | Sync marker: the recorded page opens on a magenta marker held 2 s; the recorder starts t0 when it is removed, the assembler finds that frame in the WebM, cuts there and fails with `SyncMarker` if it is missing. Supersedes 46 | The video start is bimodal (0.5–1.5 s late), so a constant is wrong in some runs; a marker is measured per recording. A short flash would be missed in the late mode; an adaptive hold needs frame events Playwright does not give. Costs about 2 s per language. |
| 70 | Steps use the keyed form: the action is the step's key (`- click: "#id"`), its value a string, number or small object; Go has one type per action shape | The schema's shape alone decides each action, so the executor needs no field checks. A breaking change: repo YAML migrated, an old-form script gets one hint. |
| 71 | `synthesizeAll` can synthesize a language's clips in parallel, but `ttsWorkers` is 1 | Measured about 1.07× with 4 workers on the 3-clip e2e script (Piper is already multi-threaded), under the 1.3× bar. The pool stays as a tested, one-constant switch. Failure reporting is by lowest step, so results match the sequential loop. |
| 72 | `type: video\|screenshots` on the script; keyed `screenshot: true \| {selector\|fullPage\|clip, annotate}` steps only in screenshots scripts; screenshots scripts forbid narration, intro, outro, languages and voices | `--screenshots-only` flag; screenshots as a side output of a video | User choice. One script, one output kind; the keyed object keeps one action key per step (Decision 70). |
| 73 | Screenshots publish to `<outputDir>/<name>/screenshots/NN.png`, overwritten on rerun; stale `NN.png` removed, other files kept. Exception to BR-006 and §4 rule 5 | Timestamped run folder | User choice. Stable paths for docs and READMEs that embed the images. |
| 74 | Annotations (box, arrow, label, dim) are a DOM overlay in document coordinates, injected before the capture and removed after; opt-in per step | Go image post-processing | User choice. Chromium already renders the text and glyphs; no new dependency. |
| 75 | `domain/shooter` runs a screenshots run and intercepts `screenshot` steps; the executor is unchanged; the renderer reaches it through a `Shooter` port wired in `app/wire`. `take_screenshots` reuses the queue, worker, lock and `get_render_status`; no job-kind column | `Page.Screenshot` plus an executor case; the capture loop in `adapters/browser`; a separate queue | The executor stays file-agnostic and its fakes untouched; step semantics stay in one place; the renderer branches on the script type, so the worker needs no change. |
| 76 | Releases are cut by hand: the `release` workflow takes a version, builds linux amd64 and arm64 tarballs (`screencaster`, `screencaster-mcp`, `playwright`) with plain `go build`, pushes an amd64-only image to GHCR and creates the GitHub Release last. The version is injected once by `-ldflags -X main.version`; native binaries need Piper, ffmpeg and Chromium installed by the user | GoReleaser; tag-push or per-commit triggers; a multi-arch image; extracting binaries from the Docker `build` stage; a `version` subcommand | User choice for the manual trigger, the amd64 image, `--version` and the shipped `playwright` CLI. Two binaries and one OS do not need a release tool; Piper is pinned to x86_64, so an arm64 image would be untested by `make e2e-runtime`; the binaries cannot fetch their driver and Chromium, and a user without Go cannot build the CLI. |

## 17. Open items for spikes

1. **Sync marker residual (Decision 69).** The marker removes the bimodal start (before it, in about a third of recordings video time 0 was about 0.5 s after `t0`). What is left is frame quantization (the WebM frame step is 33–66 ms) and compositor latency. Over 20 local renders the drift was 25–33 ms in 13, 59–66 ms in 6 and 132 ms in one. The values sit on the 33 ms frame grid of the 30 fps output: the flash is detected on the first frame after the click paints, so most of the usual ~30 ms is measurement, not placement, and no bias constant is applied. The ±100 ms bound (FR-007) holds in 19 of 20; the 132 ms run (four frames) breaks it, so the CI `e2e` job can flake about 1 run in 20. The plan's ±40 ms is not reached. Open: the cause of the 132 ms run; a higher recording frame rate would shrink the grid. Guarded by `TestRender_cli` (drift), `TestMarkerEnd_generatedClip` and the recorder's marker tests.
2. **Strict locators.** playwright-go locators are strict by default; an ambiguous selector fails fast with the candidates, so the executor needs no pre-check. Guarded by `TestBrowser_ambiguousSelectorFailsFast`.
3. **Piper (Decision 49).** `piper --model <voice>.onnx --output_file <out.wav>` with the text on stdin; the output is PCM s16le mono and the header duration matches ffprobe. Piper prints the output path on stdout, so stdout must not be inherited. Guarded by `TestParse_durationFromHeader` in `internal/adapters/tts/wav`.
4. **ARIA snapshot (Decision 47).** `Locator("body").AriaSnapshot()` gives one `- role "name" [attrs]: value` line per node, indented by nesting; a regexp over the leading role and name is enough to derive selectors. The `role=` selector matches the whole name, so uniqueness checks and `>> nth=` are only needed for repeated names. Guarded by the explore e2e tests and the mapper unit tests.
5. **Cards (Decision 63).** The built-in card renders the Polish letters (`TestScreenshot_polishGlyphsAreDistinct`), and looped stills joined with `concat` end at the sum of the parts with audio still in sync.
