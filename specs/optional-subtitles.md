# Optional subtitles (issue #5) — Implementation Plan

## Summary
Add an opt-in soft subtitle track to rendered MP4s. Each MP4 gets one `mov_text` stream for its own language, tagged with an ISO 639-2/T language code and flagged `default`, so a player that supports embedded tracks shows it without the viewer doing anything. One cue per narrated step, timed on the same offset the narration clip already uses, so subtitles never drift from audio or the intro card. Style (font, size, colour, background) is written into an ASS file whose `Default` style ffmpeg's own `mov_text` converter carries into the track; an unset field keeps ffmpeg's own SRT→`mov_text` defaults, so plain `subtitles: true` looks like a bare SRT. The script field mirrors `intro`/`outro` (off unless set); a step can hide its own cue; a CLI flag and an MCP param override the script's on/off, like `--lang`/`languages`.

Base: branch `feature/security-hardening`, clean tree at `a0d88d3`. No uncommitted work and no unmerged dependency branch this plan relies on.

## Success Criteria
- A script without `subtitles` produces the exact same ffmpeg argv as today (`TestBuildArgs_golden` unchanged) and writes no ASS file.
- `subtitles: true` or `{}` gives one `mov_text` stream per language's MP4: `ffprobe` reports `codec_name=mov_text`, `tag:language` set per the mapping, `disposition:default=1`.
- A styled object (`font`, `size`, `color`, `background`) reaches the `tx3g` sample description, as verified by the spike against this repo's ffmpeg (5.1.9).
- A step with `subtitles: false` gets no cue; its narration audio is unaffected. A step with empty narration in that language gets no cue either.
- A language that ends up with zero cues (subtitles on) gets no subtitle stream and no error — one stderr note instead.
- `--subtitles`/`--subtitles=false` (CLI) and `subtitles?: boolean` (MCP `render_video`) override the script's on/off, the same precedence as `--lang`/`languages` (BR-002). The resolved value is what the queued job stores and what a server-restart re-render replays.
- A `type: screenshots` script refuses every form (script key, step key, flag, param) with a JSON-pointer validation error, like `--lang`.
- `make lint`, `make vet` and `go test` of every touched package are green (checkpoint evidence only — no e2e, no image build).

## Scope & Constraints
- **In scope:** new FR-021 (subtitle track rules); FR-001/FR-002 additions (`subtitles` top-level field, step `subtitles: false`); FR-009 item 7 (the assembler adds the stream); FR-011/FR-012 additions (`--subtitles`, MCP `subtitles?`); BR-002-pattern override; one resolved-boolean job column (PRD §13).
- **Out of scope** (PRD §10.3 checked): burn-in (no libass, no `drawtext`, no font added to the image or native setup), sidecar `.srt`/`.ass` files, cue position/outline/bold, per-step text override (hide only), splitting one step's narration into multiple cues, automatic translation, surfacing the track in `get_render_status` output, changes to the `create_demo` MCP prompt.
- **Hard constraints:** BR-001 determinism (ASS text is a pure function of the validated script and recorded offsets, no clock/randomness in it); NFR-003 offline (no network, no font download — `mov_text` needs neither libass nor a system font); BR-004 (the ASS file lives under the run's temp dir and is removed with it on every path, including failure); BR-006 (outputs never overwritten — the ASS write uses `Files.CreateExcl` like other temp writes); BR-010 (style lives in the script, no sidecar config); FR-002 (every check runs in `renderer.Prepare`, before any browser or TTS work); backward compatibility (no `subtitles` key ⇒ no new code path runs, byte-identical output); NFR-001 (one extra small file write and one extra ffmpeg input per language — no extra process, no measurable render-time cost).
- **Trade-offs:** only four style fields are exposed because `tx3g` cannot carry position, outline or alignment (confirmed by spike) — issue #5's "position (bottom/top)" and "outline" are explicitly dropped, not silently ignored; one track per MP4 (each MP4 is already one language, so this is not a limitation); one cue per step (issue #5 asked for narration-driven cues, not sentence splitting).

## Architecture & Design

### High-Level Flow
```
script YAML ──schema──▶ script.Parse ──▶ Script.Subtitles *Subtitles, Step.NoSubtitles
CLI --subtitles / MCP job.Subtitles(bool, resolved) ──▶ renderer.Request.Subtitles *bool
renderer.Prepare: resolveSubtitles(override, script.Subtitles) ──▶ Plan.Subtitles *subtitle.Style (nil = off)
                  screenshots script + any form present ⇒ ValidationError at /subtitles
renderLanguage (per language, after Record):
  if Plan.Subtitles != nil:
     cues := subtitleCues(steps, clipPaths, durations, rec.Offsets, introLead, lang)
     len(cues)==0 → log note, no track
     else        → subtitle.ASS(cues, style) ──▶ Files.CreateExcl(<rundir>/<lang>/subs.ass)
                 → AssembleRequest.Subs = &SubsTrack{Path, Lang: subtitle.Lang(lang)}
assembler.buildArgs: ... (clips) ... "-i subs.ass" (last input, only when len(Clips)>0 and Subs != nil)
                     "-map N:s -c:s mov_text -metadata:s:s:0 language=<tag> -disposition:s:0 default"
```

### Key Changes

**New package `internal/domain/subtitle`** (pure; no `os/exec`, no I/O — same purity rule as the rest of `domain`):
```go
// Cue is one narrated step's text, shown from Start to End in the output video
// (after the intro, same timeline as the narration mix).
type Cue struct {
    Start, End time.Duration
    Text       string
}

// Style is the validated per-script style. A zero field keeps ffmpeg's own
// SRT→mov_text default for that attribute.
type Style struct {
    Font       string // e.g. "Arial"; "" = ffmpeg default
    Size       int    // px at 1920x1080; 0 = ffmpeg default
    Color      string // "#RRGGBB"; "" = ffmpeg default (white)
    Background string // "#RRGGBB" or "#RRGGBBAA"; "" = ffmpeg default
}

// ASS renders the ASS document ffmpeg converts to mov_text: one "Default"
// style built from st (PlayResX 1920, PlayResY 1080), one Dialogue line per
// cue, text escaped so narration cannot inject ASS override tags.
func ASS(cues []Cue, st Style) []byte

// Lang maps an ISO 639-1 code (as used by script.languages and Piper voice
// names) to the ISO 639-2/T tag ffmpeg keeps in the mov_text stream, via
// golang.org/x/text/language. An unparseable code gives "und".
func Lang(iso1 string) string
```
- `golang.org/x/text/language` is already an indirect dependency (`go.mod:31`); this promotes it to direct. Rejected alternative: a hand-written ISO-639-1→639-2 table — more code, and it drifts from whatever language a Piper voice file implies (`adapters/tts/piper.Defaults` already derives the language from the voice name, so a second hardcoded list would be a second source of the same knowledge — CODE_QUALITY DRY rule).
- Colour conversion: `#RRGGBB` → ASS `&H00BBGGRR`; a background's trailing `AA` → `&H<255-AA>BBGGRR` (ASS alpha is inverted, `00` = opaque) — exact values pinned by the spike (step 1).
- Time format: `H:MM:SS.cc` (centiseconds, rounded from `time.Duration`).
- Escaping: the spike (step 1) fixes the exact rule against this repo's ffmpeg (candidate: `\`→ dropped or passed through unescaped, `{`/`}`→`(`/`)`, `\n`→`\N`); pinned in `subtitle_test.go`, not guessed here.

**`internal/domain/script`** (mirrors `Bookend` end-to-end, `script.go`):
- `Script.Subtitles *Subtitles` (new field next to `Intro *Bookend`/`Outro *Bookend` at `script.go:48-49`).
- `type Subtitles struct{ Off bool "json:\"-\""; Font, Color, Background string; Size int }` with `UnmarshalJSON` following the exact `Bookend` pattern at `script.go:84-95` (`false`→`Off:true`, `true`→zero value, object→`type plain Subtitles` alias).
- `checkType` (`script.go:232-270`, table at `:248-260`): add `{"subtitles", s.Subtitles != nil}` to the screenshots-forbidden list (pointer `/subtitles`, message `subtitles is not allowed in a screenshots script`); add a per-step check next to the existing narration one (`:261-265`) for `Step.NoSubtitles` (pointer `/steps/<i>/subtitles`).
- `step.go`: `Step.NoSubtitles bool`. `UnmarshalJSON` (`:116-142`) treats key `subtitles` as a second recognized non-action key, same branch shape as `narration`; `MarshalJSON` (`:204-248`) writes `"subtitles": false` when set. `ParseSteps` (used by `explore_page`) accepts and ignores the key, same as it already does for `narration`.
- `script.schema.json`: top-level `subtitles` property `$ref: #/$defs/subtitlesOpt`, a new `$defs/subtitlesOpt` shaped exactly like `$defs/bookend` (`:64-85`) — `if {type: object} then {additionalProperties:false, properties: font (string, 1-64 chars), size (integer 8-120), color (`^#[0-9A-Fa-f]{6}$`), background (`^#[0-9A-Fa-f]{6}([0-9A-Fa-f]{2})?$`)} else {type: boolean}`. Step property `subtitles: {const: false, description: "false hides this step's subtitle cue; narration is still spoken"}`. The step's property-count rule (`$defs/step`, currently 2-of-2-with-narration-else-1, `script.go:337,353-354` `oneAction`) extends to a third optional key: both narration and subtitles present → 3 properties; exactly one → 2; neither → 1 (three `if`/`then` pairs over the existing `minProperties`/`maxProperties` leaves, `schemaErrors` keeps mapping them to the `oneAction` message, unchanged for the existing two cases).

**`internal/app/renderer`**:
- `plan.go`: `Request.Subtitles *bool` (nil = script decides, matching `LangOverride`'s nil-means-nothing-to-override shape); `Plan.Subtitles *subtitle.Style` (nil = off). `resolveSubtitles(override *bool, s *script.Subtitles) *subtitle.Style`: override `false` ⇒ nil; override `true` with no script block ⇒ default `Style{}`; no override ⇒ nil unless script sets it (and not `Off`), in which case its fields copy across. `Prepare` (`:79-150`): for a screenshots script, a non-nil `req.Subtitles` adds `/subtitles` "subtitles are not used by a screenshots script" (same shape as the existing `/languages` check at `:100-103`).
- `render.go`: `AssembleRequest.Subs *SubsTrack{Path, Lang string}`. `subtitleCues(steps []script.Step, lang string, clipPaths map[int]string, durs map[int]time.Duration, offsets []time.Duration, lead time.Duration) []subtitle.Cue`: for each step with a clip (`clipPaths[i]` present), not `NoSubtitles`, `durs[i] > 0`: `Start = lead + offsets[i]`, `End = Start + durs[i]`, `Text = steps[i].Narration[lang]`; `lead` is the intro `Still.Duration` or `0`, exactly the value `buildArgs`'s `adelay` already uses (`assembler.go:219-222`) — so cues can never drift from the mix. `renderLanguage` (`:279-352`): after `d.Rec.Record`, when `plan.Subtitles != nil`, build cues; zero cues logs `[<lang>] subtitles: no narration in this language, no subtitle track` and leaves `asm.Subs` nil; otherwise write `subs.ass` via `d.Files.CreateExcl` into `<rundir>/<lang>/subs.ass` and set `asm.Subs`, logging `[<lang>] subtitles: <n> cues (<tag>)`. No change to log wording when subtitles are off.

**`internal/adapters/assembler/assembler.go`**:
- `Input.Subs *Subs{Path, Lang string}`.
- `buildArgs` (`:188-247`): only when `len(in.Clips) > 0 && in.Subs != nil`, append `-i <Subs.Path>` as the last input (index `next+len(in.Clips)`), and after the existing `-c:a aac -b:a 160k` line add `-map <idx>:s -c:s mov_text -metadata:s:s:0 language=<Subs.Lang> -disposition:s:0 default`, before the `-metadata title`/`comment` lines. No change to the marker scan, the cut point, or the no-clips (silent source) branch — cue ends never exceed the recorder's own narration-end wait, so the timeline is untouched.

**`internal/app/wire/adapters.go`**: `assembleInput` (`:56-91`) maps `renderer.SubsTrack` onto `assembler.Subs`, field by field, same pattern as the rest of that function.

**Persistence (`internal/app/jobs`, `internal/adapters/sqlite`)**: store the **resolved** boolean, following the `languages` precedent (Explore finding: `jobs.languages` already stores the resolved list, not the raw CLI/MCP override) — not a nullable tri-state.
- `jobs.NewJob`/`jobs.Job` get `Subtitles bool`.
- `sqlite/store.go`: new column `subtitles INTEGER NOT NULL DEFAULT 0`; `addSubtitlesColumn` at open, same shape as `addScriptColumn` (`:85-106`), using `ALTER TABLE jobs ADD COLUMN subtitles INTEGER NOT NULL DEFAULT 0` — pre-existing rows get `false`, correct since they predate the feature. Add to `columns`, `INSERT`, and the row scan.
- `cmd/screencaster-mcp/main.go` (`renderJob`, around `:115-127`): pass `Subtitles: &job.Subtitles` into `renderer.Request` — the stored resolved value becomes the override on a worker re-render, exactly as `job.Languages` already does for `LangOverride`.

**MCP (`internal/adapters/mcpserver/server.go`)**:
- `renderIn.Subtitles *bool` with a `jsonschema` tag describing the override; `shotsIn` unchanged.
- `enqueue` (`:156-197`) takes the resolved `*subtitle.Style` from `Prepare` and stores `plan.Subtitles != nil` as `NewJob.Subtitles`; `takeScreenshots` keeps passing no override.
- `render_video`'s tool description is built from the embedded schema (`renderDescription`, `:93-104`) and updates on its own; its validating example script needs no subtitles (existing golden/example tests catch any accidental break).

**CLI (`cmd/screencaster/render.go`)**: `var subs bool`; `cmd.Flags().BoolVar(&subs, "subtitles", false, ...)` with `cmd.Flags().Lookup("subtitles").NoOptDefVal = "true"` (same `--flag`/`--flag=false` shape requested); in `RunE`, `var on *bool; if cmd.Flags().Changed("subtitles") { on = &subs }`, passed as `Request.Subtitles`.

**Dependencies:** `golang.org/x/text` moves from `// indirect` to a direct `go.mod` require (already vendored/resolved, no new download, no version bump needed — same version stays). No Dockerfile, `Makefile`, font, or pin change.

### Fit with Project Docs
- **PRD:** FR-001 gains the top-level `subtitles` field; FR-002 gains the schema shape and the step `subtitles: false` key; FR-009 gains item 7 (the `mov_text` stream: language tag, default disposition, cue timing) plus an `ffprobe`-based acceptance criterion; FR-011/FR-012 gain `--subtitles`/`subtitles?`; a new **FR-021 "Subtitles"** (`Implements: BR-002, BR-004, BR-006, BR-010`) holds the override precedence, the per-step hide, the screenshots refusal and the zero-cue behaviour in one place, so the other FRs just point at it; §10.1 gains one MVP bullet; §10.3 gains "burn-in, sidecar subtitle files, subtitle position/outline/bold, per-step subtitle text"; §13's jobs table gains a `subtitles` row (`BOOLEAN`, resolved); the Decisions Log gains row 82 (soft `mov_text` track from an ASS file with four style fields, not burned in — alternatives burn-in/SRT-sidecar/per-step-text, rationale: plays everywhere without new native/image dependencies, matches the issue's "ship as soft track" option).
- **ARCHITECTURE.md:** §3 module view gains `domain/subtitle/` (ASS document + language tag, FR-021) and the assembler's new `Subs` input in its one-line package table; §4's sequence diagram note gains "cues built from the recorder's own offsets, after Record"; §5 timing model gains one sentence that cue start/end reuse the clip offset and intro lead, so no second timing source exists; §10 gains the job column note; §16 gains decision 82 (same text as the PRD row, per the doc's existing convention of decisions shared between both logs).
- **CODE_QUALITY.md:** DRY table gains two rows — "subtitle language tag (ISO 639-1 → 639-2/T): `domain/subtitle.Lang` (over `golang.org/x/text`, not a second hand-written table)" and "ASS document and style: `domain/subtitle.ASS`, used by nothing else"; the separation-of-concerns listing notes `domain/subtitle` does no I/O, matching the existing `domain/card` pattern it mirrors. No new "Known debt" entry expected.

### Alternative Approaches Considered
- **Burn-in (`subtitles=`/`drawtext` ffmpeg filters):** rejected per the grilled requirement — needs libass or a bundled font in the image and native setup, and always shows (can't be toggled by the viewer). The issue itself offers burn-in-or-soft as an open choice; soft was chosen.
- **Sidecar `.srt`/`.ass` file next to the MP4:** rejected — adds a second output file, a second BR-006 naming concern, and still needs a player that reads sidecars; a soft track inside the MP4 needs nothing extra from the viewer.
- **Plain SRT muxed straight to `mov_text`:** carries no style at all. The ASS route uses ffmpeg's own converter to carry style, verified against this repo's ffmpeg build.
- **Hand-written ISO 639-1→639-2 table:** rejected in favor of `golang.org/x/text/language` (already an indirect dependency) — less code, no second place that encodes "which language has a voice."
- **Storing the raw CLI/MCP override on the job (nullable tri-state):** rejected — the codebase's own `languages` column already stores the *resolved* value, not the raw override; matching that keeps one storage pattern and needs no NULL handling on read.
- **Cue timing from WAV duration + step start, independent of `rec.Offsets`:** rejected — would be a second source of the same timing the narration mix already uses (BR-003), risking drift between what's heard and what's read.

## Implementation Steps
1. [x] Spike (no product code, dev image): build one ASS file with the sketch style line, convert with `ffmpeg -c:s mov_text`, inspect the `tx3g` box (`ffprobe -show_streams`, hexdump) to pin: the exact colour/alpha encoding, the SRT→`mov_text` defaults (font, size, white-on-black alpha) ffmpeg uses when a field is unset, and the escaping rule for `{`, `}`, `\`, and newline in narration text. Record every pinned value as constants/fixtures for step 3.
2. [x] Add `go.mod` require for `golang.org/x/text` (direct, same version already resolved).
3. [x] Create `internal/domain/subtitle/subtitle.go`: `Cue`, `Style`, `ASS` (colour conversion, time formatting, escaping from step 1), `Lang` (via `golang.org/x/text/language`).
4. [x] Create `internal/domain/subtitle/subtitle_test.go`: table tests for colour conversion (`#RRGGBB`, `#RRGGBBAA`), time rounding, escaping, exact ASS bytes for one cue, zero-`Style` defaults matching the spike, `Lang` for a mapped and an unmapped code.
5. [x] `internal/domain/script/script.go`: `Subtitles` type + `UnmarshalJSON` (mirrors `Bookend`), `Script.Subtitles` field, `checkType` additions for screenshots scripts (top-level and per-step).
6. [x] `internal/domain/script/step.go`: decode/encode the `subtitles` key in `Step.UnmarshalJSON`/`MarshalJSON`; `ParseSteps` ignores it.
7. [x] `internal/domain/script/script.schema.json`: top-level `subtitles` ref, `$defs/subtitlesOpt`, step `subtitles` property, the three-way step property-count rule.
8. [x] `internal/domain/script/script_test.go` + `testdata/scripts/`: valid samples (`valid/subtitles-on.yaml`, `valid/subtitles-styled.yaml`, `valid/subtitles-step-off.yaml`) and invalid samples (`invalid/subtitles-string.yaml`, `invalid/subtitles-bad-color.yaml`, `invalid/screenshots-subtitles.yaml`, `invalid/subtitles-step-true.yaml`); extend `TestParse_decodesBookends`-style and `TestParse_oneRootCausePerBrokenStep` coverage; confirm existing messages for the narration-only cases are unchanged.
9. [x] `internal/app/renderer/plan.go`: `Request.Subtitles`, `Plan.Subtitles`, `resolveSubtitles`, the screenshots-override refusal in `Prepare`. Tests in `plan_test.go`: override beats script both directions, bare `true` override gives defaults, screenshots refusal pointer `/subtitles`.
10. [x] `internal/app/renderer/render.go`: `AssembleRequest.Subs`/`SubsTrack`, `subtitleCues` (pure; unit-tested for intro-shifted offsets, skipped/hidden/un-narrated steps, and a no-overlap invariant against `rec.Offsets`), the `renderLanguage` write-and-log branch. Tests in `render_test.go` using the existing fakes: off makes no `CreateExcl` call; on produces the expected cues and `Subs.Lang`; zero cues logs the note and sets no `Subs`; a write failure fails the render and the temp dir is still removed.
11. [x] `internal/adapters/assembler/assembler.go`: `Input.Subs`, the `buildArgs` branch. Golden tests in `assembler_test.go`: no-`Subs` goldens unchanged; with `Subs`, argv contains the new input and the `-map/-c:s/-metadata/-disposition` sequence in order. Optional: an ffmpeg-gated test muxing a real 1-cue ASS, skipped without ffmpeg (like `TestMarkerEnd_generatedClip`).
12. [x] `internal/app/wire/adapters.go`: map `SubsTrack`→`assembler.Subs` in `assembleInput`; extend `adapters_test.go`.
13. [x] `internal/app/jobs/jobs.go` + `internal/adapters/sqlite/store.go`: `Subtitles bool` on `NewJob`/`Job`; `subtitles INTEGER NOT NULL DEFAULT 0` column + `addSubtitlesColumn` migration. Tests in `store_test.go`: round trip true/false, a pre-migration `jobs.db` gets the column with existing rows reading `false`.
14. [x] `cmd/screencaster-mcp/main.go`: pass `Subtitles: &job.Subtitles` into the render request.
15. [x] `internal/adapters/mcpserver/server.go`: `renderIn.Subtitles`, `enqueue` stores the resolved value, `take_screenshots` unaffected. Tests in `server_test.go` covering acceptance and storage.
16. [x] `cmd/screencaster/render.go`: `--subtitles` flag with `NoOptDefVal`, `Changed`-gated pointer. Tests in `render_test.go`: bare flag → `&true`, `=false` → `&false`, absent → `nil`.
17. [x] Docs: `docs/PRD.md` (FR-001, FR-002, FR-009, FR-011, FR-012, new FR-021, §10, §13, decision 82), `docs/ARCHITECTURE.md` (§3, §4, §5, §10, §16 decision 82), `docs/CODE_QUALITY.md` (DRY + separation rows), schema descriptions.
18. [x] Final light verification: `make lint`, `make vet`, `go test` of every touched package, in the dev image.

## Checkpoints (Todo List)
- [x] CP1 — Spike + domain: steps 1–4; `go test ./internal/domain/subtitle/...` green, `make vet`/`make lint` clean for the package, spike values recorded in test fixtures. — spike values pinned and recorded; lint/vet/test not run (user selected no checks)
- [x] CP2 — Script + schema: steps 5–8; `go test ./internal/domain/script/...` green, existing error messages for narration-only cases unchanged. — test not run (user selected no checks); reviewed by hand for the narration-only cases
- [x] CP3 — Renderer, assembler, wiring: steps 9–12; `go test ./internal/app/renderer/... ./internal/adapters/assembler/... ./internal/app/wire/...` green, no-subtitles goldens unchanged. — test not run (user selected no checks); existing golden cases left byte-identical by hand
- [x] CP4 — Jobs, MCP, CLI: steps 13–16; `go test ./internal/app/jobs/... ./internal/adapters/sqlite/... ./internal/adapters/mcpserver/... ./cmd/...` green. — test not run (user selected no checks)
- [x] CP5 — Docs + final light verification: steps 17–18; `make lint`, `make vet` clean, docs diff reviewed for consistency across the three files (no e2e, no image build — `screencaster-review`/`screencaster-e2e` only). — lint/vet not run (user selected no checks); docs diff reviewed by hand

### Risks & Mitigations
- Risk: players render tx3g style loosely or ignore it (Chrome's own `<video>` doesn't show embedded MP4 subtitle tracks at all, confirmed in grilling).
  - Mitigation: scoped expectations in FR-021's wording ("as the player allows"); tests check the bytes ffmpeg writes, not a specific player's rendering.
- Risk: the spike's assumed ASS→`mov_text` behaviour (background box, alpha) doesn't hold once real narration text and multi-line wrapping are involved.
  - Mitigation: step 1 is a gate — nothing in steps 3+ proceeds past placeholder constants until the spike's values are pinned in test fixtures.
- Risk: narration containing `{`, `}`, `\`, or a newline corrupts the ASS line or injects an override tag.
  - Mitigation: `subtitle.ASS` escapes before writing; unit-tested; rule itself confirmed against ffmpeg in the spike, not assumed.
- Risk: the step property-count schema rewrite silently changes an existing error message or pointer for narration-only steps.
  - Mitigation: `TestParse_oneRootCausePerBrokenStep` and the existing `oneAction` tests must still pass unchanged; the new rule only adds a third counted case.
- Risk: a pre-migration `jobs.db` (no `subtitles` column) is opened by a build that expects it.
  - Mitigation: `addSubtitlesColumn` with `NOT NULL DEFAULT 0`, tested against a database created before the column existed.
- Risk: cue end times overrun the clip mix or overlap a neighboring cue when steps are very close together.
  - Mitigation: unit test asserting cues derive from the same offsets/durations the `adelay` mix already uses, so they can't exceed what the audio already bounds.

## Test Strategy
**Light** (this feature, run during implementation per the user's selection): unit tests with the existing renderer fakes (`Files`, `Synthesizer`, `Recorder`, `Assembler`); table tests for `domain/subtitle` and the schema; golden-argv tests for `assembler`; sqlite store tests for the new column; CLI/MCP tests for the wiring. One optional ffmpeg-gated mux test in `assembler`, skipped without ffmpeg — no browser, no Piper, no network.

**Heavy** (not part of this plan): a Docker image build and `make image-check` are unnecessary here (no Dockerfile/pin change) — `screencaster-review` decides if they're worth running anyway.

**For `/screencaster-e2e`:** render the fixture app with `subtitles: true` in EN and PL; verify with `ffprobe` one `mov_text` stream per output, correct language tag and default disposition, and cue start/end times matching the clip offsets within the existing ±100 ms drift bound.

## Success Checklist
- [ ] All Success Criteria above met, with evidence (test names / ffprobe output).
- [ ] `make lint`, `make vet`, `go test` of touched packages clean (implementation).
- [ ] Heavy checks: none required by this change; review may still run them.
- [ ] Code review approved (`screencaster-review`).
- [ ] PRD / ARCHITECTURE / CODE_QUALITY sections listed in "Fit with Project Docs" updated and consistent with each other.
- [ ] No regression in existing demo scripts (no-subtitles goldens unchanged).

## Timeline & Estimates
- Spike + domain (CP1): ~0.5 day.
- Script/schema (CP2): ~0.5 day.
- Renderer/assembler/wiring (CP3): ~0.5 day.
- Jobs/MCP/CLI (CP4): ~0.5 day.
- Docs + verification (CP5): ~2–3 hours.
- **Total:** ~2–2.5 working days, plus review. Rough estimate.

## Open Questions
None — all resolved during grilling or by spike/code precedent (see "Alternative Approaches Considered" for the ones that had a design choice rather than a single right answer).

## Implementation Log
- Decision: workspace: worktree
- Decision: checks: none
- Step 1 — started
- Step 1 — done — files: none (spike only, no product code) — ran ffmpeg 5.1.9 against hand-built .ass files (dev image); pinned tx3g default-style byte layout (Fontname/Fontsize copied verbatim, PrimaryColour/BackColour via alpha-inverted RGBA), ffmpeg's own bare-SRT defaults (Arial/16/white/opaque-black) to match on a zero Style, and the escaping rule ({→(, }→), backslash dropped, our own newline → literal \N). Notes kept at /tmp/claude-1000/-home-patryk-projects-screencaster/4244396f-bb10-4765-92f7-43c7165c6045/scratchpad/subspike/SPIKE_RESULTS.md (scratchpad, not part of the repo)
- Step 2 — started
- Step 2 — done — files: go.mod — moved golang.org/x/text from the indirect block to the direct require block, same v0.14.0, no go.sum change
- Step 3 — started
- Step 3 — done — files: internal/domain/subtitle/subtitle.go — Cue, Style, ASS (template Default style, assColor, assTime, escape), Lang via golang.org/x/text/language
- Step 4 — started
- Step 4 — done — files: internal/domain/subtitle/subtitle_test.go — table tests for assColor, assTime, escape, ASS defaults/overrides/cue order, Lang (mapped + unmapped)
- Step 5 — started
- Step 5 — done — files: internal/domain/script/script.go — Script.Subtitles field, Subtitles type + UnmarshalJSON (mirrors Bookend), checkType top-level and per-step `subtitles` screenshots-forbidden rules
- Step 6 — started
- Step 6 — done — files: internal/domain/script/step.go — Step.NoSubtitles field, decode/encode of the `subtitles` key in Step UnmarshalJSON/MarshalJSON
- Step 7 — started
- Step 7 — done — files: internal/domain/script/script.schema.json — top-level `subtitles` ref, `$defs/subtitlesOpt` (mirrors `$defs/bookend`), step `subtitles` const:false property, three-way step property-count rule (nested if/then/else: narration+subtitles=3, either one=2, neither=1)
- Step 8 — started
- Step 8 — done — files: internal/domain/script/script_test.go, testdata/scripts/valid/{subtitles-on,subtitles-styled,subtitles-step-off}.yaml, testdata/scripts/invalid/{subtitles-string,subtitles-bad-color,screenshots-subtitles,subtitles-step-true}.yaml — valid/invalid samples, TestParse_decodesSubtitles, TestParse_decodesStepSubtitles, TestStep_subtitlesJSONRoundTrip, extended TestParse_invalidSamples/TestParse_oneRootCausePerBrokenStep/TestCheckType_rules/TestParseSteps_appliesTheScriptRules; narration-only cases untouched
- Step 9 — started
- Step 9 — done — files: internal/app/renderer/plan.go, internal/app/renderer/plan_test.go, internal/app/renderer/shots_test.go — Request.Subtitles, Plan.Subtitles, resolveSubtitles (override/script precedence, BR-002 pattern), screenshots-override refusal in Prepare at /subtitles; TestPrepare_subtitles (8 cases), TestPrepare_screenshotsRejectASubtitlesOverride
- Step 10 — started
- Step 10 — done — files: internal/app/renderer/render.go, internal/app/renderer/render_test.go — AssembleRequest.Subs/SubsTrack, subtitleCues (pure, offsets/lead-based), renderLanguage write-and-log branch, writeFile helper (CreateExcl, BR-006); TestRender_subtitlesOff_producesNoTrack, TestRender_subtitlesOnProducesCuesAndLangTag, TestRender_subtitlesZeroCuesLogsAndSkipsTrack, TestRender_subtitlesWriteFailureAbortsAndCleansUp
- Step 11 — started
- Step 11 — done — files: internal/adapters/assembler/assembler.go, internal/adapters/assembler/assembler_test.go — Input.Subs/Subs type, buildArgs subs-input + mov_text map/codec/metadata/disposition branch (only when clips>0 and Subs set); golden tests for with-intro index shift and the no-clips-ignores-Subs case; no-Subs goldens unchanged. Optional ffmpeg-gated mux test skipped (optional per spec; spike already verified the real byte layout by hand)
- Step 12 — started
- Step 12 — done — files: internal/app/wire/adapters.go, internal/app/wire/adapters_test.go — assembleInput maps renderer.SubsTrack→assembler.Subs (subs helper, mirrors still()); TestAssembleInput_everyFieldCrosses extended, TestAssembleInput_noSubsStaysNil
- Step 13 — started
- Step 13 — done — files: internal/app/jobs/jobs.go, internal/adapters/sqlite/store.go, internal/adapters/sqlite/store_test.go — NewJob/Job.Subtitles bool; subtitles INTEGER NOT NULL DEFAULT 0 column + addSubtitlesColumn migration (mirrors addScriptColumn); TestQueue_subtitlesRoundTrips, TestOpen_addsSubtitlesColumnToAnOldDatabase
- Step 14 — started
- Step 14 — done — files: cmd/screencaster-mcp/main.go — renderJob passes Subtitles: &job.Subtitles into renderer.Request (no test file for this main package; covered indirectly by e2e)
- Step 15 — started
- Step 15 — done — files: internal/adapters/mcpserver/server.go, internal/adapters/mcpserver/server_test.go — renderIn.Subtitles *bool; enqueue takes subtitles override, Prepare gets it, NewJob.Subtitles stores plan.Subtitles != nil; take_screenshots keeps passing nil; renderDescription sentence; TestRenderVideo_subtitlesOverrideIsResolvedAndStored, TestRenderVideo_subtitlesDefaultOffWhenNeitherSetsIt
- Step 16 — started
- Step 16 — done — files: cmd/screencaster/render.go, cmd/screencaster/render_test.go — `--subtitles` flag with NoOptDefVal "true", Changed-gated *bool into Request.Subtitles; TestRun_subtitlesFlag (bare → &true, =false → &false, =true → &true, absent → nil)
- Step 17 — started
- Step 17 — done — files: docs/PRD.md (FR-001, FR-002, FR-009 item 7 + AC, FR-011, FR-012, new FR-021, §10.1, §10.3, §13 jobs table, decision 82), docs/ARCHITECTURE.md (§3 domain/subtitle + assembler row, §4 sequence note, §5 timing sentence, §10 subtitles column note, §16 decision 82 + numbering note), docs/CODE_QUALITY.md (DRY table: Lang/ASS rows; separation-of-concerns: domain/subtitle row)
- Step 18 — started
- Step 18 — done — make lint/make vet/go test not run (user selected no checks throughout); verified instead by hand: full re-read of every touched file, brace/paren balance swept across all 20 changed/added Go files (one pre-existing, unrelated imbalance found in a shell-fixture raw string in assembler_test.go, not touched by this change), schema.json re-validated as JSON after every edit
- Handoff — mode: worktree — branch: feature/optional-subtitles — base: a0d88d3 — checks: none — changed: go.mod, internal/domain/subtitle/subtitle.go, internal/domain/subtitle/subtitle_test.go, internal/domain/script/script.go, internal/domain/script/step.go, internal/domain/script/script.schema.json, internal/domain/script/script_test.go, testdata/scripts/valid/subtitles-on.yaml, testdata/scripts/valid/subtitles-styled.yaml, testdata/scripts/valid/subtitles-step-off.yaml, testdata/scripts/invalid/subtitles-string.yaml, testdata/scripts/invalid/subtitles-bad-color.yaml, testdata/scripts/invalid/screenshots-subtitles.yaml, testdata/scripts/invalid/subtitles-step-true.yaml, internal/app/renderer/plan.go, internal/app/renderer/plan_test.go, internal/app/renderer/shots_test.go, internal/app/renderer/render.go, internal/app/renderer/render_test.go, internal/adapters/assembler/assembler.go, internal/adapters/assembler/assembler_test.go, internal/app/wire/adapters.go, internal/app/wire/adapters_test.go, internal/app/jobs/jobs.go, internal/adapters/sqlite/store.go, internal/adapters/sqlite/store_test.go, cmd/screencaster-mcp/main.go, internal/adapters/mcpserver/server.go, internal/adapters/mcpserver/server_test.go, cmd/screencaster/render.go, cmd/screencaster/render_test.go, docs/PRD.md, docs/ARCHITECTURE.md, docs/CODE_QUALITY.md — preexisting: none — checkpoints: 5/5

## Review
Reviewed 2026-11-06 — checks run: lint, vet, test
No findings.
