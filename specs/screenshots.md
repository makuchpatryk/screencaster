# Screenshots — Implementation Plan

## Summary

A demo script can now set `type: screenshots`. It then produces PNG screenshots of a website instead of a narrated MP4. The steps are the same (`goto`, `click`, `scroll`, …), and a new keyed `screenshot` step marks each capture point. A capture is the viewport by default, or the full page, one element or a clip region. Optional annotations (highlight box, arrow, label, dim) can be drawn on top. The developer runs it with the same `screencaster render` command, and Claude Code runs it through a new `take_screenshots` MCP tool. No TTS, recording or ffmpeg is involved, so a run takes seconds.

This revision maps the earlier plan onto the current tree (one module, `internal/{domain,app,adapters}`, `cmd/`, Decision 67) and the keyed step form (Decision 70). Product decisions are unchanged.

## Success Criteria

- Every existing demo YAML (no `type`) still validates and renders exactly as before: `make test` and `make e2e` are green with no change to existing scripts or tests.
- A `type: screenshots` fixture script with 5 `screenshot` steps (viewport, `fullPage`, `selector`, `clip`, annotated) renders to `<outputDir>/<name>/screenshots/01.png … 05.png`:
  - the viewport shot is 1920×1080;
  - the full-page shot is taller than 1080;
  - the element shot equals the element's bounding box;
  - the clip shot equals the clip size.
- An annotated shot shows each marker in the pixels. The next shot without `annotate` has no overlay left (pixel probes in e2e).
- Invalid combinations are rejected before any browser starts, each with a JSON-pointer error:
  - `screenshot` in a video script;
  - narration/intro/outro/languages/voices in a screenshots script;
  - no `screenshot` step;
  - more than one capture area;
  - `annotate` without a marker;
  - `screenshot` in `explore_page` actions;
  - `--lang` / `languages` override on a screenshots render.
- Speed: the fixture screenshots run takes ≤ 15 s wall time in the dev image.

## Scope & Constraints

- **In scope:**
  - Top-level script field `type: video | screenshots`.
  - Keyed step `screenshot: true | {selector? | fullPage? | clip?, annotate?}`.
  - An `annotate` block (nested in `screenshot`) with `box`, `arrow`, `label`, `dim`.
  - New package `internal/domain/shooter`.
  - `browser.Session.Capture` with an embedded `overlay.js`.
  - A screenshots branch in `renderer.Render`, its publish, and a `Shooter` port wired in `app/wire`.
  - CLI progress without a language.
  - MCP tool `take_screenshots`; `get_render_status` returning PNG paths.
  - Tests, plus PRD/ARCHITECTURE/CODE_QUALITY/README updates.
- **Out of scope:**
  - Per-language screenshots.
  - Annotations in videos.
  - Several annotations per shot (one `annotate` block, one element).
  - Formats other than PNG.
  - Viewport sizes other than 1920×1080.
  - Device scale factor > 1.
  - A job-kind column in SQLite.
  - Running screenshots without a TTS provider configured (see Risks).
- **Hard constraints:**
  - Determinism (BR-001): no randomness in file names; animations and caret are frozen at capture.
  - Offline (NFR-003).
  - One render at a time (BR-008): same lock and queue.
  - Abort leaves `outputDir` untouched (BR-004).
  - Backward compatibility of existing YAML.
  - MCP stdout hygiene.
  - Import boundaries (depguard): `domain` imports no adapter; `app` (except `wire`) imports no adapter.
- **Trade-offs:**
  - **Output is overwritten** in a stable folder on rerun (user choice) rather than BR-006's never-overwrite. A deliberate, scoped exception (Decision 73).
  - **Overlays are now in scope** (PRD §10.2), but only for screenshots, drawn in the page DOM.

## Architecture & Design

### High-Level Flow

```
cmd/screencaster render  /  MCP worker (jobs)
        │
renderer.Render ──► Prepare (script.Parse [type rules] → [video: langs+voices+cards] → outputDir)
        │
        ├─ plan.Type == video        → renderVideo (existing per-language loop, moved unchanged)
        │
        └─ plan.Type == screenshots  → renderShots
             d.Shots.Shoot(ctx, ShootRequest{Steps, Dir: runDir/shots, BaseURL, StorageState, OnStep})
                 └─ app/wire shooterPort → domain/shooter.Shooter.Shoot
                      Launch (adapters/browser, no video, no visuals) → Start
                      for each step i:
                        script.Screenshot → sess.Capture(shot, Dir/ShotName(k,n))
                              ├─ annotate? Locator(sel).Evaluate(overlay.js) → #__sc_annot
                              ├─ page/locator Screenshot (Animations: disabled, Caret: hide)
                              └─ defer remove #__sc_annot
                        other            → executor.Run(ctx, i, step)  (Mode{Visuals:false})
                      Abort
             publishShots(files, <outputDir>/<name>/screenshots, pngs)
                 move each NN.png in (rename replaces) → remove stale NN.png not in the new set
        ▼
[]Output{Path} ──► CLI: paths on stdout   MCP: outputs_json → get_render_status
```

### Key Changes

**Script schema (`internal/domain/script/script.schema.json`)**

- Top level:

```json
"type": { "enum": ["video", "screenshots"], "default": "video",
          "description": "video renders narrated MP4s; screenshots writes PNGs at each screenshot step to <outputDir>/<name>/screenshots/" }
```

- `$defs.step.properties.screenshot` (keyed, same if/then/else idiom as `press` and `scroll`):

```json
"screenshot": {
  "description": "Capture a PNG (screenshots scripts only): true for the viewport, or { selector | fullPage | clip, annotate }",
  "if": { "type": "boolean" },
  "then": { "const": true },
  "else": {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "selector": { "$ref": "#/$defs/selector" },
      "fullPage": { "const": true },
      "clip": { "$ref": "#/$defs/clip" },
      "annotate": { "$ref": "#/$defs/annotate" }
    },
    "not": { "anyOf": [
      { "required": ["selector", "fullPage"] },
      { "required": ["selector", "clip"] },
      { "required": ["fullPage", "clip"] } ] }
  }
}
```

- New `$defs`:

```json
"clip": { "type": "object", "additionalProperties": false,
          "required": ["x", "y", "width", "height"],
          "properties": { "x": {"type":"integer","minimum":0}, "y": {"type":"integer","minimum":0},
                          "width": {"type":"integer","minimum":1}, "height": {"type":"integer","minimum":1} } },
"annotate": { "type": "object", "additionalProperties": false, "required": ["selector"],
  "anyOf": [ {"required":["box"]}, {"required":["arrow"]}, {"required":["label"]}, {"required":["dim"]} ],
  "properties": {
    "selector": { "$ref": "#/$defs/selector" },
    "box":   { "const": true },
    "arrow": { "const": true },
    "dim":   { "const": true },
    "label": { "type": "string", "minLength": 1, "maxLength": 120 } } }
```

- Update the step `description` and the `oneAction` message in `script.go` to list `screenshot`.
- The schema allows `screenshot` in any script shape-wise. The *type* coupling is a cross-field rule in Go (DRY table: cross-field rules live in `domain/script`).

**Go types (`internal/domain/script`)**

`script.go`:

```go
// Script
Type string `json:"type"` // "" means video

const (
    TypeVideo       = "video"
    TypeScreenshots = "screenshots"
)

// Kind returns the script type with the default applied.
func (s Script) Kind() string { return cmp.Or(s.Type, TypeVideo) }
```

`step.go`, one more action type (Decision 70: one type per action shape):

```go
// Screenshot captures a PNG. At most one of Selector, FullPage and Clip is set
// (schema); none means the viewport. Annotate, when set, is drawn first.
Screenshot struct {
    Selector string
    FullPage bool
    Clip     *Clip
    Annotate *Annotate
}

type Clip struct{ X, Y, Width, Height int }        // json tags x, y, width, height
type Annotate struct {
    Selector string `json:"selector"`
    Box      bool   `json:"box,omitempty"`
    Arrow    bool   `json:"arrow,omitempty"`
    Dim      bool   `json:"dim,omitempty"`
    Label    string `json:"label,omitempty"`
}

func (Screenshot) Name() string { return "screenshot" }
// Target is the element shot's selector, else the annotated element's, else "".
func (a Screenshot) Target() string
```

- `decodeAction` gets a `"screenshot"` case: the value `true` gives `Screenshot{}`; an object is decoded into a private `screenshotValue` struct with JSON tags (`selector`, `fullPage`, `clip`, `annotate`) and converted.
- `MarshalJSON` writes `true` for the zero `Screenshot` and the object otherwise, so it stays the inverse of `UnmarshalJSON`.

New `checkType(s Script) failure.ValidationErrors` is called from `Parse` after the `AbsoluteHTTP` check. It runs in `Parse`, not `Validate`, because `ParseSteps` (`explore_page`) only calls `Parse` and must reject `screenshot` too. Its placeholder script has no `type`, so it is a video script.

| Condition | Pointer | Message |
|---|---|---|
| video + `screenshot` step | `/steps/<i>/screenshot` | `screenshot steps need type: screenshots` |
| screenshots + step narration | `/steps/<i>/narration` | `narration is not allowed in a screenshots script` |
| screenshots + `intro` / `outro` / `languages` / `voices` set | `/intro` etc. | `<field> is not allowed in a screenshots script` |
| screenshots + no `screenshot` step | `/steps` | `a screenshots script needs at least one screenshot step` |

There is a new `internal/domain/script/example-screenshots.yaml`, embedded and exposed as `ExampleScreenshotsYAML()`. It is used in the `take_screenshots` description, and a test keeps it valid.

**Executor (`internal/domain/executor`)**

- No change. `script.Screenshot` never reaches `dispatch`: `domain/shooter` intercepts it.
- Validation stops it from reaching the executor in a recording or in `explore_page`. If one did reach it, the existing default case would return `unknown action`.
- This is a stated deviation from "new step action = one executor case" (CODE_QUALITY, SOLID). A capture needs an output path and a counter, and the executor runs one step without files.

**Browser (`internal/adapters/browser`)**

- `overlay.js`, embedded like `cursor.js`, is a function `(el, opts) => void`. It is called through `page.Locator(a.Selector).Evaluate(overlayJS, opts)`.
  - The locator is strict, so a selector with several matches fails like in the executor.
  - It reads `el.getBoundingClientRect()` plus `scrollX/Y`.
  - It appends one `div#__sc_annot` to `document.body`: `position:absolute`, document coordinates, `pointer-events:none`, `z-index:2147483647`.
  - That div holds the markers below.

  | Marker | Drawn as |
  |---|---|
  | `box` | 4 px `#ff2d55` outline at the rect, 6 px padding |
  | `dim` | one div at the rect with `box-shadow: 0 0 0 100000px rgba(0,0,0,.55)` |
  | `arrow` | inline SVG pointing at the rect's top-left corner from 120 px up-left, flipped right/below when there is no room at the document edge |
  | `label` | pill (white text on `#ff2d55`, 20 px system font) at the arrow's tail, or above the box when there is no arrow |

  Placement is a pure function of the rect and the document size, so it is deterministic. Document coordinates keep it right for `fullPage` shots too.
- `func (s *Session) Capture(shot script.Screenshot, path string) error`:
  1. If `shot.Annotate != nil`, inject the overlay as above. Register a `defer` that runs `page.Evaluate("document.getElementById('__sc_annot')?.remove()")`, so the overlay never leaks into the next shot.
  2. Take the shot:
     - `Selector` set: `page.Locator(sel).Screenshot({Path, Animations: disabled, Caret: hide})`;
     - otherwise: `page.Screenshot({Path, FullPage, Clip, Animations: disabled, Caret: hide})`. For the enum constants, check `playwright.ScreenshotAnimationsDisabled` / `ScreenshotCaretHide` in v0.6201.1.
  3. The timeout is the page default, `executor.ActionTimeout` (30 s).
- Taking `script.Screenshot` keeps the domain free of adapter types: the adapter already imports `domain/script`, never the reverse.
- `Launch` with no `VideoDir` and `Visuals: false` already gives an unrecorded session with no cursor (the explorer pattern).

**New package `internal/domain/shooter`** (sibling of `recorder`: runs one screenshots run)

```go
// Session is the browser the shooter drives. adapters/browser implements it.
type Session interface {
    executor.Page
    Start() error
    Capture(shot script.Screenshot, path string) error
    Abort()
}
type LaunchOptions struct {
    BaseURL      string
    StorageState *script.StorageState
}
type Input struct {
    Steps        []script.Step
    Dir          string // PNGs are written here, named by ShotName
    BaseURL      string
    StorageState *script.StorageState
    OnStep       func(i int) // 0-based, right before each step (CLI progress)
}
type Shooter struct {
    Launch func(ctx context.Context, o LaunchOptions) (Session, error)
}

// Shoot runs the steps and returns the PNG paths in step order.
func (s Shooter) Shoot(ctx context.Context, in Input) ([]string, error)

// ShotName is the one place that names a PNG: k-th shot (1-based) of n,
// zero-padded to max(2, digits(n)): 01.png, or 001.png when n > 99.
func ShotName(k, n int) string
```

- The executor is built with `executor.New(sess, in.BaseURL, executor.Mode{})`. Non-screenshot steps go to `ex.Run(ctx, i+1, step)`.
- A capture error becomes `failure.Step(i+1, "", "screenshot", shot.Target(), err)`.
- A cancelled ctx returns `ctx.Err()`, as `recorder.stepError` does.
- The session is always `Abort`ed (no video to keep).
- `n` is the number of `screenshot` steps, counted before the loop.

**Renderer (`internal/app/renderer`)**

- `files.go`: the `Files` port gains `ReadDir(name string) ([]fs.DirEntry, error)`, for finding stale shots. Add it to `adapters/osfs` (`os.ReadDir`) and to the in-memory fake in `filestest_test.go`.
- `plan.go`:
  - `Plan` gets `Type string`, set from `s.Kind()`.
  - `Prepare` branches after `script.Parse`. For screenshots:
    - a non-empty `req.LangOverride` gives the ValidationError `/languages: languages are not used by a screenshots script`;
    - `Languages` / `Validate` / `voices.Resolve` / `resolveCard` are skipped; `Plan.Languages` and `Plan.Voices` stay nil and the cards stay zero;
    - `OutputDir` resolution and the inside-the-work-dir check are unchanged.
- `render.go`:

```go
// ShootRequest is one screenshots run (repeats domain/shooter.Input on purpose,
// ARCHITECTURE §3). OnStep is called with the 0-based index before each step.
type ShootRequest struct {
    Steps        []script.Step
    Dir          string
    BaseURL      string
    StorageState *script.StorageState
    OnStep       func(i int)
}

// Shooter runs a screenshots script and returns the PNG paths in step order.
// A step failure comes back as *failure.Failure; a cancelled ctx as ctx.Err().
type Shooter interface {
    Shoot(ctx context.Context, r ShootRequest) ([]string, error)
}
```

  - `Deps` gets `Shots Shooter`.
  - `Render`:
    - After `Prepare`, the start log for screenshots is `render start: <path> name=<n> type=screenshots steps=<k> output=<dir>`.
    - Then it creates `runDir` and its `defer RemoveAll` as today.
    - Then it branches: `if plan.Type == script.TypeScreenshots { outs, err = renderShots(...) } else { outs, err = renderVideo(...) }`.
  - `renderVideo` is the existing language loop, the `publish` call and the end log, **moved without logic edits**.
  - `renderShots(ctx, d, plan, runDir, rep)`:
    1. `MkdirAll(runDir/shots)`.
    2. `d.Shots.Shoot` with `OnStep` → `rep.progress("", i+1, n, name, target)`.
    3. Log `screenshots: N captured in X`.
    4. `publishShots(d.Files, filepath.Join(plan.OutputDir, plan.Script.Name, "screenshots"), pngs)`.
    5. Log `render done in X` plus one path per line.
    6. Return `[]Output{{Path: abs}}` with `Lang` empty and `DurationMs` 0.
- `publish.go`, `publishShots(files Files, dir string, src []string) ([]string, error)`:
  1. `MkdirAll(dir)`.
  2. For each PNG, call the existing `move(files, src, filepath.Join(dir, filepath.Base(src)))`. Both `os.Rename` and the cross-filesystem `.part` + rename replace an existing target, and the `.part` is still created with `CreateExcl`, so no existing file is opened for writing.
  3. `ReadDir(dir)`: remove every regular file whose name matches `^\d{2,3}\.png$` and is not in the new set.

  It never deletes other files and never removes the folder. If step 2 fails part-way, the folder holds a mix of new and old shots, and the error says so (`publish <path>: …`). That is acceptable for a rerunnable, overwrite-by-design output (Decision 73).

**Wire (`internal/app/wire`)**

- `adapters.go`: `shooterPort{sh shooter.Shooter}` implements `renderer.Shooter` through `shootInput(r renderer.ShootRequest) shooter.Input`, which maps field by field like `recordInput`.
- `app.go`:
  - `NewDeps` sets `Shots: shooterPort{sh: shooter.Shooter{Launch: launchShots}}`.
  - `launchShots` opens `browser.Launcher{}.Launch(ctx, browser.Options{BaseURL, StorageState})` with no video and no visuals. It keeps the nil-interface guard used by `launchExploration`.
- `adapters_test.go`: `TestShootInput_everyFieldCrosses` (with `assertNoZeroField`).

**CLI (`cmd/screencaster/render.go`)**

- The Progress printer prints `step i/n action target` when `lang == ""`, else `[lang] step …` as today.
- PNG paths already go to stdout through the existing `for _, o := range outs` loop.
- `--lang` on a screenshots script gives the ValidationError from `Prepare`.
- `main.go` is unchanged: `wire.NewDeps` wires `Shots`.

**MCP (`internal/adapters/mcpserver`)**

- New tool `take_screenshots`. Its input is `shotsIn{Script string}` (no languages); its output reuses `renderOut{JobID, Status, Position}`.
- The handler mirrors `renderVideo`:
  1. `ScriptPath` confinement.
  2. `Voices()`, `ReadScript`, `Prepare`.
  3. `plan.Type != script.TypeScreenshots` → tool error `script type is video; use render_video`.
  4. `Store.Insert(jobs.NewJob{…, Languages: []string{}})`, then `Notify`.
- `render_video` gains the mirror check: `script type is screenshots; use take_screenshots`.
- Description: one paragraph (what it does, output path, overwrite on rerun, poll `get_render_status`, "the full script schema is in render_video's description") plus `ExampleScreenshotsYAML()`.
- `get_render_status`:
  - `statusIn.JobID` description: "id returned by render_video or take_screenshots".
  - `outputOut.Lang` and `DurationMs` get `omitempty`. Video outputs always carry both, so existing goldens do not change.
- `adapters/sqlite` `outputRecord` is unchanged: its tags never change. A screenshots job stores `"lang":""` and `"durationMs":0`.
- `create_demo.md`: one line saying that a set of screenshots uses `type: screenshots` and `take_screenshots`.
- The worker (`app/jobs`) and `cmd/screencaster-mcp` are unchanged: they call `renderer.Render`, which branches on the script type.

**Data model:** no SQLite change. `languages` stores `[]`.

**Dependencies:** none new. No Dockerfile change: Chromium, Playwright `Screenshot` and `Locator.Evaluate` are already present.

### Fit with Project Docs

- **ARCHITECTURE.md:**
  - `domain/shooter` sits next to `domain/recorder` (runs one run's steps through the executor, behind a `Session` port).
  - Only `adapters/browser` touches playwright-go. The overlay JS lives there, like `cursor.js`.
  - `app/renderer` reaches the shooter only through its `Shooter` port, and `app/wire` maps it (§3). No new depguard rule is needed.
  - Pipeline rules kept:
    - Validate-first (§4.1): all type rules run in Parse/Prepare before launch.
    - Abort path (§4.4): temp only; `outputDir` is untouched until publish.
    - Lock and queue (§6): unchanged, so screenshot jobs serialize with videos.
    - Error model (§9): capture failures are ordinary Step failures.
  - **Deviations** (all go into the decision log):
    1. `screenshot` is a step action the executor's dispatch does not handle (the shooter intercepts it).
    2. Publish overwrites, against BR-006 and §4.5, scoped to `<outputDir>/<name>/screenshots/NN.png`.
    3. Overlays (PRD §10.2) and a non-MP4 output (PRD §10.3) are in scope, for screenshots only.
- **CODE_QUALITY.md:**
  - KISS: no new dependencies; one JS file, one package of about 80 lines, one publish helper reusing `move`.
  - YAGNI: one annotation per shot, fixed colours and placement, no styling options.
  - DRY:
    - the type rules live in one Go function (`checkType`);
    - capture-area exclusivity is in the schema;
    - `shooter.ShotName` is the only source of PNG names;
    - the video pipeline code is moved, not copied.
  - Separation:
    - the executor runs one step without files;
    - the shooter decides capture paths;
    - the browser draws and captures;
    - the renderer publishes;
    - wire maps.
  - SOLID: `executor.Page` is unchanged, so the executor, recorder and explorer fakes are untouched. `shooter.Session` and `renderer.Shooter` are new consumer-declared test seams. The `Files` port grows by one method (`ReadDir`), which publish needs.
  - Composition: `shooter.Session` embeds the `executor.Page` *interface*, as `recorder.Session` does.
- **Docs to update:**
  - **PRD:**
    - a new FR "Screenshots";
    - a BR-006 exception;
    - §10.2/§10.3 notes;
    - Decisions Log rows 72–75.
  - **ARCHITECTURE:**
    - §3 module tree (`domain/shooter/`);
    - §4: the screenshots branch and its rules (rule 5 exception);
    - §7: a note that `screenshot` is handled by the shooter;
    - §9 table: capture failures are Step failures;
    - §10: the script snapshot also covers `take_screenshots`;
    - §11 file layout: `<outputDir>/<name>/screenshots/NN.png`;
    - §13 e2e row;
    - §15: overlays are now partly in;
    - §16 decisions 72–75.
  - **CODE_QUALITY:**
    - DRY table: type rules, PNG naming `shooter.ShotName`, overlay JS in `adapters/browser`;
    - separation table row for `domain/shooter`;
    - YAGNI bullets: §10.2 overlays and §10.3 formats are now partly in;
    - SOLID open/closed: the screenshot exception;
    - Known debt: a partial overwrite on a failed publish; screenshots still need a TTS provider configured (the CLI calls `wire.TTS` at render start).
  - **README:** a `type: screenshots` example.

Decisions to add:

| # | Decision | Alternatives | Rationale |
|---|---|---|---|
| 72 | `type: video\|screenshots` on the script; keyed `screenshot: true \| {selector\|fullPage\|clip, annotate}` steps only in screenshots scripts; screenshots forbid narration/intro/outro/languages/voices | `--screenshots-only` flag; screenshots as a side output of a video | User choice. One script, one output kind; the keyed object keeps one action key per step (Decision 70) |
| 73 | Screenshots publish to `<outputDir>/<name>/screenshots/NN.png`, overwritten on rerun; stale `NN.png` removed, other files kept. Exception to BR-006 | Timestamped run folder | User choice. Stable paths for docs and READMEs that embed the images |
| 74 | Annotations (box, arrow, label, dim) are a DOM overlay in document coordinates, injected before capture and removed after; opt-in per step | Go image post-processing | User choice. Chromium already renders text and glyphs; no new deps |
| 75 | `domain/shooter` runs screenshots runs and intercepts `screenshot` steps; executor unchanged; renderer reaches it through a `Shooter` port wired in `app/wire`. MCP `take_screenshots` reuses the queue, worker, lock and `get_render_status`; no job-kind column | `Page.Screenshot` plus an executor case; the capture loop in `adapters/browser`; a separate queue | The executor stays file-agnostic and its fakes untouched; step semantics stay in one place; the renderer branches on script type, so the worker needs no change |

### Alternative Approaches Considered

- **Step shape (Decision 70 era):**
  - Keyed object with `annotate` nested (chosen, user choice). One action key per step, like `press` and `scroll`.
  - Keyed `screenshot: true` plus sibling step keys (`fullPage`, `clip`, `annotate`). This breaks the one-action-key rule and the step's property count.
  - A separate `annotate` step before `screenshot`. More state, since the overlay would persist across steps.
- **Where capture runs:**
  - (a) `Screenshot` on `executor.Page` plus a dispatch case. Needs an output path and counter in the executor and breaks every Page fake.
  - (b) The whole loop inside `adapters/browser` behind a renderer port. It re-implements step semantics outside the executor (DRY table), and the adapter would execute steps.
  - (c) `domain/shooter` behind `renderer.Shooter` (chosen). It mirrors `recorder`, and both fakes are trivial.
- **Where the type rules live:**
  - `Validate`/`Prepare`: `explore_page` (`ParseSteps` → `Parse` only) would then let `screenshot` through to the executor's `unknown action`.
  - `Parse` (chosen): one function, with `ParseSteps` covered.
- **Overwrite strategy:**
  - `RemoveAll` of the folder, then move. That deletes the user's other files, and a failure leaves nothing.
  - Move the new files over, then delete the stale `NN.png` (chosen). It only touches files the tool owns.
- **Determinism at capture:**
  - Injected CSS (`animation: none`).
  - Playwright's `Animations: disabled` / `Caret: hide` options (chosen): supported, scoped to the capture, no page mutation.
- **Overlay positioning:**
  - `position: fixed` in viewport coordinates: wrong on `fullPage` shots.
  - `position: absolute` in document coordinates (chosen).
- **MCP surface:**
  - Reuse `render_video` with a type switch. Fewer tools, but the user wants an explicit tool.
  - New `take_screenshots` (chosen), queued like videos.

## Implementation Steps

1. [x] Schema (`internal/domain/script/script.schema.json`):
   - top-level `type`;
   - `screenshot` under `$defs.step.properties` (boolean `true` or object, with exclusivity);
   - `$defs.clip` and `$defs.annotate`;
   - the step `description` lists `screenshot`.
2. [x] `internal/domain/script` types:
   - `Script.Type`, `Kind()` and the `TypeVideo` / `TypeScreenshots` constants;
   - `Screenshot`, `Clip`, `Annotate` types with `Name` and `Target`;
   - `decodeAction` and `MarshalJSON` cases;
   - the `oneAction` message lists `screenshot`.
3. [x] `internal/domain/script` rules and example:
   - `checkType` called from `Parse`;
   - `example-screenshots.yaml` and `ExampleScreenshotsYAML()`.
4. [x] Samples and tests:
   - `testdata/scripts/valid/screenshots-*.yaml`: each capture area, `true`, `{}`, annotate.
   - `testdata/scripts/invalid/`: each of the following fails at its expected pointer.
     - screenshot-in-video;
     - narration / intro / outro / languages / voices in screenshots;
     - no screenshot step;
     - selector+fullPage;
     - clip+fullPage;
     - `screenshot: false`;
     - annotate without a marker;
     - annotate without a selector;
     - `fullPage` on a click step.
   - Unit tests:
     - the `checkType` table;
     - `Kind()` default;
     - the round trip of the keyed `screenshot` in `UnmarshalJSON`/`MarshalJSON`;
     - `ParseSteps` rejects `screenshot`;
     - the example validates.
5. [x] `internal/adapters/browser`:
   - `overlay.js` (embedded);
   - `Session.Capture(shot script.Screenshot, path)`: Animations disabled, Caret hidden, overlay removed in a `defer`.
   - `browser_test.go` stays pure, with no Chromium, like its existing tests: add one test that maps a `script.Screenshot` to Playwright options (factor the mapping into a small `screenshotOptions` func). Capture itself is covered by e2e (step 12).
6. [x] `internal/domain/shooter`:
   - `Session`, `LaunchOptions`, `Input`, `Shooter.Shoot`, `ShotName`.
   - Unit tests with a fake Session, covering:
     - call order;
     - names `01.png…`, and `001.png` when n > 99;
     - executor steps vs capture steps;
     - the Step failure shape on a capture error;
     - Abort on every path;
     - ctx cancel;
     - OnStep indices.
7. [x] `internal/app/renderer` and `osfs`:
   - `Files.ReadDir` in the port, `adapters/osfs` and the test fake;
   - `Plan.Type` and the `Prepare` branch (skip langs/voices/cards, reject the lang override);
   - `ShootRequest` / `Shooter` port and `Deps.Shots`;
   - extract `renderVideo` (pure move);
   - `renderShots`;
   - `publishShots`;
   - the log wording.
8. [x] Renderer tests:
   - Prepare for screenshots: no voices needed; lang override rejected.
   - Render with a fake Shooter:
     - outputs and paths;
     - start and end log lines;
     - overwrite of an existing `01.png`;
     - stale `03.png` removed when the new run has 2;
     - a non-matching `notes.txt` and `cover.png` kept;
     - a Shoot failure leaves `outputDir` untouched and the temp dir removed.
   - All existing video renderer tests stay green unchanged.
9. [x] `internal/app/wire`:
   - `shooterPort`, `shootInput`, `launchShots`;
   - `NewDeps` sets `Shots`;
   - `TestShootInput_everyFieldCrosses`.
10. [x] CLI (`cmd/screencaster/render.go`):
    - Progress line without a lang;
    - a `render_test.go` case with a fake renderFunc printing PNG paths and lang-less progress.
11. [x] MCP (`internal/adapters/mcpserver`):
    - `take_screenshots` tool and handler;
    - the `render_video` mirror check;
    - `outputOut` `omitempty`;
    - `statusIn` description;
    - a `create_demo.md` line;
    - in-memory transport tests:
      - `take_screenshots` queues a job with `Languages: []`;
      - it rejects a video script;
      - `render_video` rejects a screenshots script;
      - the screenshots example validates;
      - the status of a finished fake job lists the PNG paths without `lang` / `durationMs`;
      - existing goldens unchanged.
12. [x] E2E (`tests/e2e/shots_test.go`; the `TestScreenshot_` prefix is taken by the card tests, so use `TestShots_`):
    - A fixture script `testdata/scripts/e2e-shots.yaml` (or next to the existing e2e scripts, matching their location) with 5 shots:
      - viewport;
      - `fullPage` on a fixture page taller than 1080 (add `testdata/fixture-app/long.html` if none is);
      - element;
      - clip;
      - annotated: box + arrow + label + dim on a button.
    - `TestShots_cli` asserts:
      - the dimensions;
      - the dim darkens a corner pixel vs the viewport shot of the same page;
      - the box colour `#ff2d55` at the outline;
      - the shot after the annotated one has no overlay;
      - the wall time is ≤ 15 s;
      - a rerun with fewer shots removes the stale PNG.
    - Add `TestShots_cli` to the `make e2e-runtime` `-test.run` pattern in `Makefile`.
13. [x] Docs: PRD, ARCHITECTURE, CODE_QUALITY and README, as listed in "Fit with Project Docs" (decisions 72–75).

## Checkpoints (Todo List)

- [x] CP1 — Script format: steps 1-4; `go test -race ./internal/domain/script/...` green; every existing sample still valid; new invalid samples fail at the expected pointers — race clean, full `go test ./...` green
- [x] CP2 — Capture + shooter: steps 5-6; `go test -race ./internal/domain/... ./internal/adapters/browser/...` green; `go build ./...` ok; existing executor/recorder/explorer tests untouched and green; `make lint` depguard clean — race clean, vet -tags e2e ok, golangci-lint 0 issues
- [x] CP3 — Renderer + wire: steps 7-9; `go test -race ./internal/app/... ./internal/adapters/osfs/...` green, video renderer tests unchanged; publish overwrite/stale/keep cases pass — race clean, only the memFS fake gained ReadDir
- [x] CP4 — Entry points: steps 10-11; `make test` and `make lint` green; MCP in-memory tests cover both tools' type checks and the status paths; goldens unchanged — go test -race ./... green, vet -tags e2e ok, golangci-lint 0 issues
- [ ] CP5 — E2E: step 12; `make e2e` green (including the existing video and drift tests), `make e2e-runtime` green with `TestShots_cli`; PNGs inspected by eye once
- [ ] CP6 — Docs + final: step 13; PRD/ARCHITECTURE/CODE_QUALITY/README updated (decisions 72–75, §3/§4/§7/§9/§10/§11/§13/§15, DRY + separation tables, known debt); `make lint vet test` clean

### Risks & Mitigations

- **Risk: the overlay sits in the wrong place.** Causes are sticky/fixed headers, transformed ancestors, or an element shot that crops away the arrow or label.
  - Mitigation: document coordinates from `getBoundingClientRect()` + scroll, attached to `document.body` at max z-index. The e2e checks pixels on the fixture.
  - Mitigation: document that markers outside the element can be cropped in a `selector` shot. Expanding the capture by the overlay bounds comes later, if needed.
- **Risk: an overlay leaks into the next shot** after a capture error.
  - Mitigation: the overlay is removed in a `defer` in `Capture`. A capture error aborts the run anyway (BR-004). The e2e asserts the shot after the annotated one is clean.
- **Risk: flaky pixels** from animations, the caret, late fonts or lazy images.
  - Mitigation: `Animations: disabled`, `Caret: hide`; Playwright waits for fonts before a screenshot.
  - Mitigation: scripts can add `wait` with a selector before `screenshot`. Determinism means the same pixels for a static page, not for live content.
- **Risk: a clip past the viewport or page edge.** The schema cannot check `x+width ≤ 1920`.
  - Mitigation: Playwright's error becomes an ordinary Step failure naming step `screenshot`. Document that clip coordinates are in the viewport for a plain shot. The e2e uses a clip inside the viewport.
- **Risk: overwrite deletes something the user cares about.**
  - Mitigation: only `^\d{2,3}\.png$` files in `<outputDir>/<name>/screenshots/` are touched. The folder is never removed and other files are kept (unit test). Abort never reaches publish.
- **Risk: moving the video pipeline into `renderVideo` changes its behaviour.**
  - Mitigation: a pure move with no logic edits. The existing renderer unit tests and the e2e drift/ffprobe tests run unchanged as the regression gate.
- **Risk: screenshots need a TTS provider configured.** `cmd/screencaster` calls `wire.TTS` before `NewDeps`, and the MCP server at startup.
  - Mitigation: both images always set a provider. Recorded as known debt; splitting `NewDeps` would be scope creep now.

## Test Strategy

- **Unit:**
  - `domain/script`: schema samples (valid/invalid), the `checkType` table, `Kind()` default, keyed `screenshot` round trip, `ParseSteps` rejecting `screenshot`, the example validating.
  - `domain/shooter` with a fake Session: order, names, failure shape, Abort, ctx, OnStep.
  - `app/renderer` with a fake Shooter and the in-memory Files: Prepare branch, Render branch, `publishShots` overwrite/stale/keep/abort, log lines.
  - `app/wire`: `shootInput` field crossing.
  - `cmd/screencaster` with a fake renderFunc: PNG paths on stdout, lang-less progress.
- **Integration:**
  - MCP over the in-memory transport: `take_screenshots` / `render_video` type checks, the job queued with `[]` languages, status outputs carrying PNG paths.
  - SQLite store and lock unchanged; the existing tests confirm it.
- **E2E (dev image `make e2e`; runtime image `make e2e-runtime`):** the fixture screenshots script through the CLI binary, checking:
  - dimensions per capture area;
  - annotation pixel probes;
  - no overlay leak;
  - stale removal on rerun;
  - wall time ≤ 15 s;
  - the existing video e2e unchanged.
- **Manual:**
  - Run a `type: screenshots` copy of `demos/portfolio-projects.yaml` against `makuchpatryk.com` and check the annotated shots by eye.
  - Run `take_screenshots` from Claude Code and poll the status.

## Success Checklist

- [ ] All success criteria met (with evidence)
- [ ] `golangci-lint`, `go vet`, `go test -race` clean in the module (incl. `-tags e2e` vet)
- [ ] E2E green in the Docker image (`make e2e`, `make e2e-runtime`)
- [ ] Code review approved (`screencaster-review`)
- [ ] Documentation updated (PRD, ARCHITECTURE §3/§4/§7/§9/§10/§11/§13/§15/§16, CODE_QUALITY DRY/separation/YAGNI/SOLID/known debt, README)
- [ ] No regressions in existing demo scripts (`demos/portfolio-projects.yaml` still validates and renders)

## Timeline & Estimates

- Phase 1 (implementation, CP1–CP4): ~6.5 h
- Phase 2 (e2e + fixture page, CP5): ~2 h
- Phase 3 (docs, review, polish, CP6): ~1.5 h
- **Total**: ~10 h, plus a buffer of ~2 h for overlay placement tweaks

## Open Questions

None. Checked against the code:
- depguard keeps `domain` from adapters, and `app` from adapters except `wire`. `domain/shooter` imports only `domain/executor`, `domain/script` and `domain/failure`, and `adapters/browser` already imports `domain/script`.
- `ParseSteps` calls only `Parse`.
- `Files` has no `ReadDir` yet (added in step 7).
- `move` replaces an existing target (`os.Rename`, `.part` + rename).

## Implementation Log
- Step 1 — started
- Step 1 — done — files: internal/domain/script/script.schema.json — top-level `type`, keyed `screenshot` (+exclusivity), `$defs.clip`, `$defs.annotate`, step description; existing script tests pass
- Step 2 — started
- Step 2 — done — files: internal/domain/script/script.go, internal/domain/script/step.go — Script.Type/Kind(), TypeVideo/TypeScreenshots, Screenshot/Clip/Annotate, decode/marshal cases, oneAction message; tests land in step 4
- Step 3 — started
- Step 3 — done — files: internal/domain/script/script.go, internal/domain/script/example-screenshots.yaml — checkType called from Parse (video+screenshot, screenshots+narration/intro/outro/languages/voices, no shot), ExampleScreenshotsYAML(); tests in step 4
- Step 4 — started
- Step 4 — done — files: testdata/scripts/valid/screenshots-{areas,annotate}.yaml, testdata/scripts/invalid/{screenshot-*,screenshots-*,fullpage-on-click}.yaml, internal/domain/script/script_test.go — 2 valid + 14 invalid samples, checkType table, Kind, round trip, ParseSteps, example; deviation: annotate-without-marker gives 4 anyOf errors at one pointer (like the cookie rule), so it is not in the one-root-cause list
- CP1 — ticked (script tests -race green, whole suite green)
- Step 5 — started
- Step 5 — done — files: internal/adapters/browser/overlay.js, internal/adapters/browser/browser.go, internal/adapters/browser/browser_test.go — Session.Capture + pageScreenshotOptions/locatorScreenshotOptions (two small funcs instead of one `screenshotOptions`), tests pure; overlay checked once in real Chromium via a throwaway test (deleted): box/arrow/label/dim in place, no overlay left, sizes 1920x1080 / full 1920x2110 / element 260x70 / clip 500x200
- Step 6 — started
- Step 6 — done — files: internal/domain/shooter/shooter.go, internal/domain/shooter/shooter_test.go — Session, LaunchOptions, Input, Shooter.Shoot, ShotName; 9 test funcs (order, names incl. 001.png, shot-vs-step numbering, Step failure shape, Abort every path, ctx cancel, launch error, OnStep)
- Step 7 — started
- CP2 — ticked (build, vet -tags e2e, race tests on domain + browser, golangci-lint: all clean)
- Step 7 — done — files: internal/app/renderer/{files,plan,render,publish}.go, internal/app/renderer/filestest_test.go, internal/adapters/osfs/{osfs,osfs_test}.go — Files.ReadDir (port, osfs, memFS), Plan.Type + Prepare branch, ShootRequest/Shooter/Deps.Shots, renderVideo (pure move), renderShots, publishShots; renderShots/renderVideo also take `start` (needed for the end log / timestamp)
- Step 8 — started
- Step 8 — done — files: internal/app/renderer/shots_test.go — 15 tests: Prepare (no voices/cards, lang override rejected, outputDir check kept, video Type), Render (publish paths, request fields, progress without lang, log lines, overwrite, stale removed + other files kept, failure leaves outputDir untouched / creates no folder, cancel), publishShots (cross-device, failed move); existing video tests unchanged and green
- Step 9 — started
- Step 9 — done — files: internal/app/wire/{adapters,app}.go, internal/app/wire/{adapters_test,app_test}.go — shooterPort, shootInput, launchShots, NewDeps sets Shots; TestShootInput_everyFieldCrosses; extra TestNewDeps_wiresEveryPort (nil Shots would panic at render time)
- CP3 — ticked (race tests on internal/app/... and osfs green)
- Step 10 — started
- Step 10 — done — files: cmd/screencaster/render.go, cmd/screencaster/render_test.go — progress prints `step i/n action target` when lang is empty, `[lang] step …` otherwise; new test for lang-less progress and PNG paths on stdout
- Step 11 — started
- Step 11 — done — files: internal/adapters/mcpserver/{server.go,create_demo.md,server_test.go,shots_test.go} — take_screenshots tool; render_video and take_screenshots share `enqueue` (one place for confinement, Prepare, type check, Insert); wrong-tool message `script type is X; use Y`; jobs store `[]` languages; outputOut lang/durationMs omitempty; statusIn description; create_demo line; 11 new tests, goldens unchanged
- Step 12 — started
- CP4 — ticked (go vet -tags e2e, go test -race ./..., golangci-lint run ./...: all clean; status goldens untouched)
- Step 12 — done — files: tests/e2e/shots_test.go, testdata/fixture-app/shots.html, Makefile — TestShots_cli (6 shots: viewport, fullPage 1920x2497, element 300x120, clip 400x150, annotated, clean viewport equal to the first; wall 1.7 s; stale removal, foreign files kept, --lang refused, failing step leaves folder alone); script templates are Go consts like the existing e2e scripts, not testdata/scripts files; fixture page is new (projects.html cannot give exact sizes) and passes eyeball check; TestShots_cli added to e2e-runtime -test.run
- Step 13 — started
- Step 13 — done — files: docs/PRD.md, docs/ARCHITECTURE.md, docs/CODE_QUALITY.md, README.md — PRD: FR-020, BR-006 exception, 10.1/10.2/10.3, FR-002/011/013 notes, §13 files, glossary, decisions 72-75; ARCHITECTURE: §3, §4 (screenshots runs + rule 5), §6.2, §7, §9, §10, §11, §13, §15, §16 (72-75); CODE_QUALITY: YAGNI, DRY (type rules, PNG names, overlay), separation row, SOLID exception, enforcement, known debt x2; README: screenshots section (example validated with script.Parse), layout, MCP tools
