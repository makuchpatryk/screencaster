# Render logs, demo-folder output and intro/outro cards — Implementation Plan

## Summary

Three changes to the render. (1) The render says what it is doing at the start, per phase and at the end, with timings. (2) Paths of a demo resolve against the folder of its YAML: MP4s go to `<demo dir>/output/` by default. (3) Every video gets a 3 s intro and a 3 s outro card. The default cards are built in (title, description, closing line). A client replaces a card with its own picture by naming a path in the YAML, or turns it off with `false`. This makes the output look finished without an editing step.

## Success Criteria

- `screencaster render demos/x.yaml` prints, on stderr, a start summary (script, languages, voices, step count, output dir), one line per phase per language with its elapsed time, and an end summary (output paths, video duration, total time). A failed render prints an end line with the elapsed time and the error is still printed once by the CLI.
- With no `outputDir`, the MP4 lands in `demos/output/` for `demos/x.yaml`. An explicit `outputDir` resolves against the demo's folder and must stay inside the working directory.
- A script with no `intro`/`outro` renders `3 s card + recording + 3 s card`. ffprobe: h264, 1920×1080, 30 fps, aac, duration ≈ recording + 6 s. Narration still lines up (drift check, marker offset shifted by the intro length).
- `intro: {image: assets/logo.png}` shows that picture full-frame for 3 s (scaled to fit on a dark background). `intro: false` and `outro: false` give no card. A missing, non-image or outside-work-dir image fails validation before any browser or TTS starts, with the pointer `/intro/image`.
- Polish text on the built-in cards renders with the right glyphs in the runtime image.
- `make test vet lint e2e` and `make e2e-runtime` green.

## Scope & Constraints

- **In scope:**
  - `Request.Log` and the renderer's log lines, CLI and MCP wiring
  - path base change for `outputDir`, default `<demo dir>/output`
  - schema `intro` / `outro` (`false` or object), validation, image check
  - built-in card HTML, one Chromium screenshot call, ffmpeg concat and offset shift
  - tests, e2e, docs, `create_demo` prompt, `.gitignore`
- **Out of scope:**
  - narration on cards, transitions or fades, animated cards, video clips as cards
  - slides between steps (PRD §10.2 stays post-MVP; this is only a start card and an end card)
  - a `--quiet` flag, log levels, JSON logs
  - a separate "logo" field (one `image` field, per user)
  - a `storageState` or step-level path change (nothing else is a path)
- **Hard constraints:** BR-001 determinism (card HTML and image bytes are fixed inputs, no clock or random in the card), NFR-003 offline (the template embeds all CSS, no remote font or image), BR-004 (card build failure aborts, nothing published), FR-002 fail early (image checked in `Prepare`), BR-006 (never overwrite), MCP stdout stays protocol-only (logs go to `slog` on stderr).
- **Trade-offs:**
  - **Chromium for the built-in card over ffmpeg `drawtext`.** Chromium is already in the image, wraps long text and has Latin Extended glyphs; `drawtext` needs a font file path and does no wrapping. Cost: one extra browser launch per language (about 1 s).
  - **Default-on over opt-in** (user choice). Existing demos render 6 s longer and need `intro: false, outro: false` to stay as before. Pre-release, `demos/` has one file.
  - **One path rule (demo folder) over a work-dir rule for `outputDir`** (user choice). Reverses the "paths relative to the demo file" rejection in decision 58 for every path in a demo (`outputDir`, `image`). The inside-work-dir rule stays.

## Architecture & Design

### High-Level Flow

```
Render(ctx, d, req)
  log "render start: <script> name=… langs=… steps=… output=<dir>"
  Prepare -> Plan{…, DemoDir, OutputDir, Intro, Outro}   (image paths checked here)
  per language:
    log "[en] tts: n clips"                     (existing step, now timed)
    cards: for each of intro/outro that is on and has no image
           -> card.HTML(title, subtitle)  -> d.Cards.Screenshot(ctx, shots)  (one launch)
    log "[en] recording"                        (+ existing per-step Progress)
    log "[en] recorded <video len>"
    asm.Input{Webm, Clips, Intro:&Still{Path,Dur}, Outro:&Still{Path,Dur}, …}
    log "[en] assembling"  ->  log "[en] assembled <file> <dur>"
  publish
  log "render done in <elapsed>: <paths>"       (or "render failed after <elapsed>")
```

ffmpeg graph with both stills (inputs: 0 = webm, 1 = intro image, 2 = outro image, 3.. = clips):

```
[0:v]fps=30,scale=1920:1080,format=yuv420p,setsar=1[main]
[1:v]scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=#0f172a,fps=30,format=yuv420p,setsar=1[intro]
[2:v]…same…[outro]
[intro][main][outro]concat=n=3:v=1:a=0[v]
[k:a]adelay=<offset+introMs>:all=1[ak] … amix …[a]
```

Stills are `-loop 1 -framerate 30 -t <sec> -i <png>`. The audio mix is unchanged except every clip offset grows by the intro length. The audio may end before the video (silent outro, or the gap after the last clip); this is already true today and an AAC stream shorter than the video plays fine. No `apad` (ARCHITECTURE §5, found in M3). With no clips, `anullsrc` + `-shortest` as today (the concat output is finite).

### Key Changes

- **`core/script/script.schema.json`:**
  - New properties `intro` and `outro`, both `$ref: #/$defs/bookend`:
    ```json
    "bookend": {
      "description": "Start or end card. Omit for the built-in card, false for none.",
      "oneOf": [
        { "const": false },
        { "type": "object", "additionalProperties": false,
          "properties": {
            "image": { "description": "Path of a PNG or JPEG, relative to the demo file's folder. Replaces the built-in card.", "type": "string", "pattern": "(?i)\\.(png|jpe?g)$" },
            "title": { "type": "string", "maxLength": 120 },
            "subtitle": { "type": "string", "maxLength": 500 },
            "durationMs": { "type": "integer", "minimum": 500, "maximum": 10000 }
          },
          "not": { "required": ["image", "title"] } }
      ]
    }
    ```
    `image` with `title`/`subtitle` is rejected (the picture is the whole card); an `allOf` of two `not` clauses covers `subtitle`.
  - `outputDir` description: "relative to the demo file's folder, inside the working directory. Default `output`."
- **`core/script/script.go`:** `Script` gains `Intro, Outro *Bookend`. `Bookend{Off bool; Image, Title, Subtitle string; DurationMs int}` with `UnmarshalJSON` mapping `false` to `Off: true` (the only custom decoding; the schema already rejected `true`). Constant `DefaultCardMs = 3000`.
- **`core/card` (new, pure):** `//go:embed card.html`; `HTML(Card{Title, Subtitle string}) string` via `html/template` (escapes text). `Outro(lang) string` returns the closing line (`en`: "Thank you for watching", `pl`: "Dziękujemy za uwagę", any other language: English). Table in Go, one place; a test pins the languages the built-in voices cover. No I/O, no process.
- **`core/browser`:** new `Launcher.Screenshot(ctx, shots []Shot) error` (`Shot{HTML, Out string}`): one Chromium, 1920×1080 viewport, `SetContent`, wait for load, `Screenshot` PNG to `Out`. No video, no cursor. Same stdout hygiene (driver output discarded).
- **`core/assembler`:** `Input` gains `Intro, Outro *Still` (`Still{Path string; Duration time.Duration}`). `buildArgs` adds the inputs and graph above and adds `Intro.Duration` to every clip offset. The assembler stays ignorant of steps and languages. Golden-args tests extended.
- **`core/renderer`:**
  - `Request.Log func(msg string)`, called through a nil-safe helper. Every message is built in `renderer` (one place for the wording), callers only choose where it goes.
  - `Deps.Cards Cards`, `type Cards interface{ Screenshot(ctx, []browser.Shot) error }` declared by the consumer; the shot type lives in `renderer` to keep `renderer` free of a `browser` import: `type Shot struct{ HTML, Out string }` and `main` adapts, or the interface takes `[]struct{…}`. Decision at implementation: `renderer.Shot` and a 3-line adapter in `main`.
  - `Plan` gains `DemoDir` (absolute) and `Intro, Outro Card` resolved: `Card{Off bool; Image string (absolute or ""); Title, Subtitle string; Duration time.Duration}`.
  - `Prepare`: `demoDir := filepath.Dir(resolve(WorkDir, ScriptPath))`; `outputDir` and each `image` resolve against it with `within(WorkDir, …)` and the existing wording `outsideWorkDir`; image existence (`image not found: <abs>`) and regular-file check go to the same `errs` list, pointers `/intro/image`, `/outro/image`. Intro defaults: title = `meta.title` or `name`, subtitle = `meta.description`. Outro defaults: title = `card.Outro(lang)` at render time (language-dependent, so left empty in `Plan` and filled in `renderLanguage`), subtitle = `meta.title` or `name`.
  - `renderLanguage` builds the cards after TTS and before recording (fails early on a card error, before the slow recording), passes `Still`s to the assembler.
  - Logs as in the flow above; elapsed from `d.Now()`.
- **`cli/render.go`:** `Request.Log` prints `msg` to stderr, one line each. Existing `[lang] step i/n` lines stay.
- **`mcp/queue` worker / `mcp/main.go`:** the render closure sets `Request.Log` to `slog.Info(msg, "job", id)`. No stdout.
- **`mcp/server/server.go`, `create_demo.md`:** tool description comes from the embedded schema (automatic). The prompt step 4 mentions `intro`/`outro` and says images go next to the demo, e.g. `demos/assets/`. The `render_video` result text is unchanged.
- **`.gitignore`:** `/output/` becomes `output/` so `demos/output/` is ignored too.
- **Dependencies / Dockerfile:** none. Chromium, ffmpeg and fonts are already in the runtime image. Spike S5 confirms Polish glyphs.
- **Data model (SQLite):** none.

### Fit with Project Docs

- **ARCHITECTURE.md:**
  - §3 module list: add `card/`. §4: pipeline gets "build cards" after TTS; rule 1 adds the image check; `Request.Log`.
  - §5: the video is now `intro + recording + outro`; offsets are placed at `introMs + offset`; the "output duration is the video length" bullet is reworded.
  - §11: `output/` is next to the demo; example tree updated.
  - §12 stdio hygiene: logs via `Request.Log` to stderr.
  - §15: "Slides / overlays" extension point notes that start/end cards exist; interleaved slides are still post-MVP.
  - §16: new decisions 62 (all demo paths resolve against the demo's folder, `outputDir` default `<demo dir>/output`; supersedes the "relative to the demo file rejected" part of 58), 63 (intro/outro cards on by default, built-in card from an embedded HTML page screenshotted by Chromium, ffmpeg concat), 64 (render log lines come from `renderer` through `Request.Log`).
  - §17: new spike S5 (Polish glyphs and audio sync with stills).
- **CODE_QUALITY.md:** DRY table rows for "card text and closing line" (`core/card`) and "path base of a demo" (`renderer.Prepare`). Separation table: `core/card` row. Hard rule "Paths … come from the demo script via `renderer.Plan`" still holds. No new known debt expected; the third wiring copy in `cli/main.go` and `mcp/main.go` grows by one field (`Cards`), so the existing "extract on the third" debt line is updated.
- **PRD.md:** FR-001/FR-002 (`outputDir` base, `intro`/`outro`), FR-009 (stills, duration), FR-010 (output location), §10.2 (cards moved into MVP, slides stay out), decisions log 62–64, glossary.
- Deviation stated: this adds a feature PRD §10.2 deferred. It is limited to two cards, so `recorder` stays untouched.

### Alternative Approaches Considered

- **Cards via ffmpeg `drawtext`.** No extra browser launch, but needs a font file path, has no wrapping, and Polish glyph support depends on the font. Not chosen.
- **Go image rendering (`golang.org/x/image`) with an embedded font.** No browser, but a new dependency and hand-written text layout. Not chosen (KISS).
- **Cards recorded by the recorder as extra browser pages.** Gives the same code path as steps, but they go through the 30 fps VFR WebM, shifting offsets and adding the bimodal start risk (§17.1). Rejected.
- **Convention-only `assets/intro.png`.** Less schema, but implicit and not validated. Rejected by the user in favour of an explicit path.
- **Opt-in cards.** Existing output unchanged, but the default video looks plainer. Rejected by the user.
- **Logs through a `*slog.Logger` in `Deps`.** Structured, but the CLI would print `time=… level=INFO` on every line, which reads worse than the current progress lines. A `func(string)` per request keeps CLI output plain and lets the MCP side add the job id. Chosen.

## Implementation Steps

1. `core/script`: schema `bookend` + `intro`/`outro`, `outputDir` description; `Bookend` type and `UnmarshalJSON`; `DefaultCardMs`. Testdata: `valid/cards.yaml` (image, durationMs, `false`), `invalid/` samples for `intro: true`, image with title, bad extension, `durationMs: 100`. Script tests.
2. `core/card`: `card.html`, `HTML`, `Outro`. Tests: escaping (`<`, quotes), Polish text kept, unknown language falls back to English, built-in languages covered.
3. `core/browser`: `Launcher.Screenshot`. e2e test: PNG is 1920×1080 and not blank.
4. `core/assembler`: `Still`, `Input.Intro/Outro`, `buildArgs` graph and offset shift. Golden tests: no stills (unchanged output), intro only, both, with and without clips.
5. `core/renderer`: `Plan`/`Prepare` changes (demo dir, output dir, image checks, defaults), `Request.Log`, `Deps.Cards`, card building in `renderLanguage`, log lines, end line on success and failure. Tests with fakes (see Test Strategy).
6. `cli/main.go`, `cli/render.go`, `mcp/main.go`, `mcp/queue` render closure: wire `Cards` and `Log`. CLI test: log lines on stderr, none on stdout.
7. `.gitignore`, `create_demo.md`, `README` (output location, intro/outro, assets example), existing demo and `example.yaml` check.
8. E2E: existing scripts get `intro: false`/`outro: false` where they measure drift or exact duration; new tests for cards (duration ≈ recording + 6 s, first frame is the card, custom image, `false`), and for output under the demo folder. Run `make e2e` and `make e2e-runtime`.
9. Docs: ARCHITECTURE, CODE_QUALITY, PRD as listed in Fit with Project Docs.

## Checkpoints (Todo List)

- [ ] CP1 — Logs: step 5 (log part only: `Request.Log`, lines, end line), step 6 (log wiring). `go test -race ./...` in `core`, `cli`, `mcp` green; CLI test shows start, phase and end lines on stderr only.
- [ ] CP2 — Demo-folder paths: step 5 (Prepare path changes). Unit tests: default `<demo dir>/output`, explicit `outputDir` against demo dir, `..` escape rejected, MCP script in `demos/` publishes into `demos/output`; `go test -race` green.
- [x] CP3 — Script format and card text: steps 1–2. `go test ./script/... ./card/...` in `core` green; schema accepts `cards.yaml`, rejects the new invalid samples with exact pointers. — script + card tests green
- [x] CP4 — Assembly and screenshot: steps 3–4. Golden-args tests green; `Screenshot` e2e passes in the dev image; manual ffmpeg run with both stills gives the expected duration and a 30 fps aac mp4. — golden tests green, `TestScreenshot_*` e2e pass, manual run: 3+5+3 s = 11 s, h264 1920×1080 30 fps + aac
- [ ] CP5 — Renderer integration: step 5 (cards), 6 (`Cards` wiring), 7. Fake-based tests green (cards built before recording, image checked in `Prepare`, `false` skips, offsets passed unshifted to the assembler, failure leaves no output); `make vet lint` clean in every module.
- [ ] CP6 — E2E + docs: steps 8–9. `make e2e` and `make e2e-runtime` green (card duration, drift with cards on, Polish glyph render checked by eye or pixel test); docs updated; `grep -rn "relative to the working directory" docs README.md core` shows no stale outputDir text.

### Risks & Mitigations

- **Risk:** audio drift after adding the intro (offset arithmetic, AAC priming, concat timing).
  - Mitigation: offsets shift by the exact intro length in whole ms; stills are made with `-t` and `fps=30` so 3 s is 90 frames. The drift e2e keeps running with the intro on and expects the marker at `introMs + offset`, ±150 ms.
- **Risk:** `concat` with a `-loop 1` still hangs or never ends.
  - Mitigation: every still has `-t`, so all concat inputs are finite. Spike S5 runs it in the image first; fall back to `-loop 1 -t` + `-vframes` if needed.
- **Risk:** Polish glyphs missing in the runtime image's fonts (square boxes).
  - Mitigation: spike S5 renders "Dziękujemy za uwagę — żółć" and checks it; if missing, add a font package to the Dockerfile and update `image-check`.
- **Risk:** the extra Chromium launch per language slows renders, or a stray driver process leaks.
  - Mitigation: one `Screenshot` call per language builds both cards; it takes the same ctx watcher and `pw.Stop()` path as `Launch`. NFR-001 (≤ 2× duration) gets easier because the video is 6 s longer.
- **Risk:** existing users' videos change (+6 s) and paths move (`demos/output/`).
  - Mitigation: documented in README and decisions 62–63; pre-release, `demos/` has one file; `intro: false, outro: false` restores the old look.
- **Risk:** image path escapes the work dir or reads a non-image (LLM-written script).
  - Mitigation: `within(WorkDir, …)`, regular-file check and the extension pattern in the schema, all in `Prepare` before any work.

## Test Strategy

- **Unit (fakes for `Recorder`, `Synthesizer`, `Assembler`, `Cards`):**
  - renderer: `TestRender_logsStartPhasesAndEnd` (message order with fixed `Now`), `TestRender_logsEndLineOnFailure`, `TestRender_nilLogIsFine`, `TestPrepare_outputDirDefaultsToDemoOutput`, `TestPrepare_outputDirRelativeToDemoDir`, `TestPrepare_imageOutsideWorkDirIsValidationError`, `TestPrepare_imageMissingIsValidationError`, `TestPrepare_cardOffSkipsCard`, `TestRender_cardsBuiltBeforeRecording`, `TestRender_customImageSkipsScreenshot`, `TestRender_outroTitleFollowsLanguage`.
  - script: schema and `Bookend` decode (`false`, object, absent).
  - card: escaping, fallback language.
  - assembler: golden `buildArgs`.
  - cli: stderr lines, stdout has only paths.
- **Integration:** MCP in-memory transport: `render_video` for `demos/x.yaml` with a fake renderer, error for an image outside the work dir comes back before a job is created.
- **E2E (dev and runtime image):** card render (ffprobe codec, size, fps, duration within 150 ms of recording + 6 s), first-frame check (not the page), custom image check (pixel colour of the first frame), `false` check (duration ≈ recording), existing drift, NFR-001 and explore tests still green, output lands in the demo's folder.
- **Manual:** render `demos/portfolio-projects.yaml` (en + pl) and watch both cards; add `demos/assets/logo.png` and point `intro.image` at it; read the stderr log top to bottom.

## Success Checklist

- [ ] All success criteria met (with evidence)
- [ ] `golangci-lint`, `go vet`, `go test -race` clean in `core`, `cli`, `mcp`, `tests/e2e`
- [ ] E2E green in the dev image and the runtime image
- [ ] Code review approved (`screencaster-review`)
- [ ] README, PRD, ARCHITECTURE, CODE_QUALITY updated (decisions 62–64, spike S5)
- [ ] Existing demo renders (en, pl) with cards and logs; `intro: false, outro: false` restores the old length

## Timeline & Estimates

- Phase 1 (implementation, steps 1–7): ~6 h
- Phase 2 (tests, e2e, spike S5, steps 8): ~4 h
- Phase 3 (docs, review, polish, step 9): ~2 h
- **Total:** ~12 h, plus buffer

## Open Questions

- [ ] Built-in card look: dark slate background (`#0f172a`), white title, grey subtitle. Fine as a first default, or does the user want a brand colour?
- [ ] Image path base was read as "the demo file's folder" (so `image: assets/logo.png`). If the user meant "inside a fixed `assets/` folder next to the demo, name only", step 5's path rule changes.
