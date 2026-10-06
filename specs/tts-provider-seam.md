# TTS Provider Seam — Implementation Plan

## Summary
Piper leaks across the codebase: `renderer.Synthesizer` takes an `.onnx`
path, `core/voices` hardcodes `.onnx`, Piper naming and the built-in voices,
both `main`s duplicate `/opt/piper/...` constants, `renderer.voiceNames` strips
`.onnx`, and the runtime image bakes Piper into its only stage. This change
puts every Piper detail behind one adapter package selected by an env var, and
splits the image into a provider-free base plus a Piper layer, so a future
provider (local binary, cloud API, sidecar HTTP) is one new adapter, one
factory case and one image layer. Piper stays the only adapter.

## Success Criteria
- `grep -rniE 'piper|\.onnx' core cli mcp --include=*.go` hits only
  `core/provider/piper/**` and `core/provider/provider.go` (the factory case).
- `SCREENCASTER_TTS` selects the provider in both CLI and MCP through one
  shared function; unset or unknown value, or a missing Piper binary, exits
  non-zero at startup with a clear message before any render/tool work.
- Code holds no image paths: Piper binary and voice dir come from
  `SCREENCASTER_PIPER_BIN` / `SCREENCASTER_PIPER_VOICES`, set by the image.
- Existing demo YAML renders unchanged; `get_options` output identical for the
  stock image (FR-018 tests unchanged in substance).
- `docker build .` and `make image` yield the Piper image tagged
  `screencaster:piper` and `screencaster` (latest); `--target runtime-base`
  builds an image with Chromium + ffmpeg + binaries and no TTS.
- `make test vet lint image image-check e2e e2e-runtime` all green.

## Scope & Constraints
- **In scope:** `core/provider` (contract + factory), `core/provider/piper`
  (adapter), `core/support/wav` (shared WAV duration), provider-neutral
  `core/voices`, renderer/server type changes, both `main`s, Dockerfile stages,
  Makefile, compose, depguard, docs.
- **Out of scope:** a second provider; script schema changes; CLI flag or YAML
  field for provider; PRD edits (BR-001, BR-011, NFR-003, FR-018 text stays);
  generic provider config syntax beyond per-provider env vars.
- **Hard constraints:** BR-001 determinism and NFR-003 offline stay strict
  (Piper satisfies both); stdout hygiene (ADR-48); validate-first (FR-002,
  BR-011); one render path (BR-001, FR-011); backward-compatible YAML and
  `docker run screencaster ...`.
- **Trade-offs:** an interface that exists for swapping, not only as a test
  seam — explicit bend of CODE_QUALITY §YAGNI, by user decision. Script voice
  IDs are opaque per provider: swapping provider may need script edits; the
  provider's defaults cover scripts without `voices`.

## Architecture & Design

### High-Level Flow
```
cli/main.go, mcp/main.go
   │  eng, err := provider.FromEnv(os.Getenv, workDir)   ← startup, fail fast
   ▼
core/provider  ── switch SCREENCASTER_TTS ──► core/provider/piper.New(bin, dirs)
   │ returns provider.Engine                       │ stats bin, keeps dirs
   ▼                                               │ Catalog(): scan *.onnx → voices.Catalog
renderer.Deps{ TTS: eng, Voices: catalog }         │ Synthesize(): exec piper → wav.Duration
   │ Prepare: voices.Resolve(langs, script.Voices, catalog) → map[lang]voiceName
   │ renderLanguage: d.TTS.Synthesize(ctx, voiceName, text, out.wav)
mcp/server: Deps.Voices = eng.Catalog  → voices.Options(catalog) → get_options
```
Discovery stays per call (MCP picks up new `/work/voices` files without a
restart, as today).

### Key Changes

**`core/voices` — provider-neutral data + rules (BR-011, FR-018)**
```go
// Voice is one installed voice as its provider names it.
type Voice struct {
    Name string // opaque ID passed back to the provider
    Lang string // language code, decided by the provider
}

// Catalog is what a provider offers: installed voices and its built-in
// default per language (BR-011).
type Catalog struct {
    Voices   []Voice
    Defaults map[string]string // lang -> voice name
}

func Resolve(langs []string, scriptVoices map[string]string, c Catalog) (map[string]string, failure.ValidationErrors)
func Options(c Catalog) []Option
```
- `Resolve` returns lang → **voice name** (was path). Same messages:
  `no voice for language: <lang>`, `voice not installed: <name>`.
- `Options` lists every `Defaults` key plus every voice language; default
  counts only when installed (unchanged behaviour).
- Removed: `Installed`, `Discover`, `Lang`, `modelExt`, `builtin`.

**`core/provider/provider.go` — contract + factory**
```go
// Engine is a TTS provider: it lists its voices and speaks text into a PCM
// WAV at outPath, returning the clip's duration (FR-003). Adapters that get
// another format convert it themselves.
type Engine interface {
    Catalog() (voices.Catalog, error)
    Synthesize(ctx context.Context, voice, text, outPath string) (time.Duration, error)
}

// FromEnv builds the provider named by SCREENCASTER_TTS. Unset or unknown
// names are an error, so the entry points fail before any work.
func FromEnv(getenv func(string) string, workDir string) (Engine, error) {
    switch name := getenv("SCREENCASTER_TTS"); name {
    case "piper":
        return piper.New(getenv("SCREENCASTER_PIPER_BIN"),
            getenv("SCREENCASTER_PIPER_VOICES"), filepath.Join(workDir, "voices"))
    case "":
        return nil, errors.New("no TTS provider: set SCREENCASTER_TTS (available: piper)")
    default:
        return nil, fmt.Errorf("unknown TTS provider %q (available: piper)", name)
    }
}
```
- `getenv` injected → table tests without `t.Setenv` globals.
- No import cycle: `provider` → `piper` → `voices`, `wav`; `piper` does not
  import `provider` (satisfies `Engine` structurally).

**`core/provider/piper` — the only Piper-aware code**
- `type Piper struct{ bin string; dirs []string }`; `New(bin, imageVoices,
  projectVoices string) (*Piper, error)`: errors if `bin` or `imageVoices`
  empty, or `os.Stat(bin)` fails (`piper binary not found: <path>`).
- `Defaults = map[string]string{"en": "en_US-ryan-high", "pl": "pl_PL-darkman-medium"}`
  (moved from `voices.builtin`; `make image-check` greps it here).
- `Catalog()`: scan dirs for `*.onnx` (later dir wins), `Lang` = name up to
  first `_`, names without `_` skipped from options as today; keeps
  name → path map internally.
- `Synthesize(ctx, voice, text, out)`: rescans/looks up the model path
  (`voice not installed: <name>` if gone), runs
  `piper --model <path> --output_file <out>`, stdout to null, stderr captured
  into `*piper.Error` (same `Error()`/`Unwrap()` as today's `tts.Error`), then
  `wav.Duration(out)`.
- Path lookup per call is one `ReadDir` per dir per clip — negligible next to
  Piper; avoids stale state between `Catalog` and `Synthesize`.

**`core/support/wav`** — `Duration(path) (time.Duration, error)` +
`parse(data)`: today's `wavDuration`/`parseWAV` moved verbatim (ADR-49).
Shared because every adapter must deliver WAV + duration. Lives in `core/support/`,
not under `core/provider/`: it is a helper, not a provider, and a sibling of
`piper` would read as one (moved from `core/provider/wav` during implementation).

**`core/renderer`**
- `Synthesizer.Synthesize(ctx, voice, text, outPath)` — param renamed, doc
  says "voice name, opaque, from the provider's catalog" (drop `core/tts` ref).
- `Deps.Voices voices.Catalog`; `Plan.Voices map[string]string // lang -> voice name`.
- `Prepare(req, catalog voices.Catalog)`.
- `voiceNames` prints `plan.Voices[lang]` directly (no `.onnx` strip).

**`mcp/server`** — `Deps.Voices func() (voices.Catalog, error)`; handler code
unchanged apart from the type. Tests build a `voices.Catalog` literal instead
of writing `.onnx` files.

**`cli/main.go`, `mcp/main.go`**
- Drop `piperBin`, `builtinVoices`, `projectVoices`; drop `core/tts` and
  `voices.Discover` imports.
- CLI: `provider.FromEnv(os.Getenv, wd)` in `main` before `run`; error →
  stderr, exit 1. `render` calls `eng.Catalog()` per render and passes
  `TTS: eng`.
- MCP: `FromEnv` at the top of `run()`, before opening the store; error
  returned → `slog.Error` + exit 1; `discover = eng.Catalog`.
- Partly pays CODE_QUALITY known debt (TTS wiring now one shared call);
  recorder/assembler/cards wiring stays duplicated (still two copies).

**`core/tts`** — deleted (contents moved to `piper` and `wav`).

**`.golangci.yml`** — `core-exec-only-in-wrappers`: `!**/core/tts/**` →
`!**/core/provider/piper/**`; desc updated. Narrower than `core/provider/**`
so factory and `wav` can't spawn processes.

**Dockerfile**
- `piper` stage unchanged (download + sha256 pin).
- `dev`: keep `COPY --from=piper`; add
  `ENV SCREENCASTER_TTS=piper SCREENCASTER_PIPER_BIN=/opt/piper/piper SCREENCASTER_PIPER_VOICES=/opt/piper/voices`.
- `runtime` → renamed `runtime-base`: Chromium (playwright install), ffmpeg,
  both binaries, `WORKDIR /work`; no Piper, no `SCREENCASTER_TTS`.
- New last stage `runtime-piper`: `FROM runtime-base`, `COPY --from=piper
  /opt/piper /opt/piper`, the three ENV lines. Last → `docker build .` yields it.
- Header comment: how to add a provider (local binary = stage + ENV; cloud or
  sidecar = `FROM runtime-base` + ENV only, NFR-003 caveat).

**Makefile**
- `IMAGE := screencaster`; `image`: `docker build --target runtime-piper -t
  $(IMAGE):piper -t $(IMAGE) .`; new `image-base`: `--target runtime-base -t
  $(IMAGE)-base`.
- `image-check`: sed source → `core/provider/piper/piper.go` (`/^var Defaults/`);
  voice dir from the image's own env: `docker run --rm $(IMAGE) sh -c 'ls
  "$$SCREENCASTER_PIPER_VOICES"'`, so path lives only in the Dockerfile.
- `e2e-runtime`: unchanged (image ENV supplies the provider).

**compose.yaml** — `target: runtime-piper`, `image: screencaster:piper`.

**tests/e2e/render_test.go:384** — build the engine with
`provider.FromEnv(os.Getenv, ...)` (dev/runtime image ENV), voice by name
`en_US-ryan-high`; no hardcoded paths.

**Script schema / MCP tools / CLI flags** — no change.

**Dependencies** — none new.

### Fit with Project Docs
- **ARCHITECTURE.md:** new packages live in `core` (dependency rules hold:
  `cli`/`mcp` → `core`, no reverse). Only `core/provider/piper`,
  `core/assembler`, `core/browser` spawn processes (depguard). Validate-first
  holds: `Prepare` resolves against the catalog before any synthesis.
  §1 driver "No HTTP clients in code" stays true; noted as a constraint any
  future cloud/sidecar adapter must revisit (with BR-001).
- **CODE_QUALITY.md:** bends §YAGNI lines "No second TTS engine…" (still true:
  no second engine) and "interfaces exist as test seams, not for swapping" —
  `Engine` is a deliberate swap seam (user decision). KISS: static switch, no
  registry/plugins/reflection. DRY: built-in defaults single source in
  `piper.Defaults`; provider selection single source `provider.FromEnv`;
  image paths single source in Dockerfile. SoC: `voices` = pure rules over
  data, `piper` = tool wrapper, `provider` = selection.
- **Docs to update:**
  - ARCHITECTURE §2 diagram node `Piper + voices` → `TTS provider (Piper)`.
  - §3 module list: replace `voices/`, `tts/` rows with `voices/` (neutral
    resolve), `provider/` (Engine + FromEnv), `provider/piper/`, and `support/wav/`;
    dependency-rule bullet "Only core/tts…" → `core/provider/piper`.
  - §4 sequence participant `tts (Piper)` → `provider (Piper)`.
  - §11 `voices/*.onnx` row: "extra voices for the active provider (Piper: .onnx+.json)".
  - §12 Docker: stages `piper`, `dev`, `build`, `runtime-base`, `runtime-piper`;
    tags; provider env vars; voice discovery owned by the provider.
  - §15 "Other TTS engines": rewrite as the recipe (adapter in
    `core/provider/<name>`, case in `FromEnv`, `runtime-<name>` stage or env
    only; must output PCM WAV; must revisit NFR-003/BR-001).
  - §16 new decision 65: TTS provider behind `provider.Engine`, chosen by
    `SCREENCASTER_TTS` at startup, image = base + provider layer.
  - §17.3 guard test name/location (`wav` package).
  - CODE_QUALITY: line 21 (`os/exec` for Piper), 34–35 (YAGNI bend), DRY table
    row "Voice resolution…" → `core/voices` rules + `core/provider/piper`
    defaults, 61, 71, 73 (`core/provider/piper` wraps one tool), 86, 98, 106,
    111, 148, 172 (known debt: TTS part done).
  - README: Docker section (image tags, `SCREENCASTER_TTS`), `/work/voices`
    note, dev commands (`make image-base`).

### Alternative Approaches Considered
- **Engine with 4 methods (Discover/VoiceLanguage/DefaultVoices/Synthesize)** —
  finer-grained, but bigger interface and `voices` would need a mock. Rejected
  for 2 methods returning a plain `Catalog` (user choice).
- **Default `SCREENCASTER_TTS=piper` in code** — works without env but puts
  provider + image paths back in code. Rejected: image owns its provider
  (user choice).
- **Package `core/tts` + `core/tts/piper`** — keeps depguard path; rejected
  for `core/provider` naming (user choice).
- **WAV parser inside the Piper adapter** — YAGNI-purer; rejected because
  every adapter must deliver WAV + duration (user choice).
- **Separate Dockerfiles per provider / TTS as sidecar only** — more files or
  network dependency; rejected for one Dockerfile with base + layer stages.
- **Per-main adapter struct wrapping Engine to `Synthesizer`** — unnecessary:
  `Engine` satisfies `renderer.Synthesizer` structurally.
- **Discover once at startup and cache** — would stop MCP from seeing new
  `/work/voices` files without restart; rejected.

## Implementation Steps
1. Create `core/support/wav` with `Duration`/`parse` moved from
   `core/tts/tts.go`; move `TestParseWAV_*` tests.
2. Rewrite `core/voices`: `Voice`, `Catalog`, `Resolve`, `Options` over
   `Catalog`; drop `Installed`, `Discover`, `Lang`, `builtin`. Rewrite
   `voices_test.go` with catalog literals (keep all Resolve/Options cases).
3. Create `core/provider/piper`: `New`, `Defaults`, `Catalog` (scan + lang
   rule + later-dir-wins), `Synthesize`, `Error`. Move `TestLang`,
   `TestDiscover_*` here as `Catalog` tests; add `New` errors (empty bin,
   missing bin), `Synthesize` with unknown voice, and a fake-binary test
   (shell script writing a WAV) for stderr capture.
4. Create `core/provider/provider.go`: `Engine`, `FromEnv`. Tests: piper ok
   (temp fake bin), unset, unknown, missing bin.
5. Delete `core/tts`. Update `.golangci.yml` depguard rule.
6. `core/renderer`: `Synthesizer` param rename + doc, `Deps.Voices`,
   `Plan.Voices`, `Prepare` signature, `voiceNames`. Update `render_test.go`
   fakes and expected call log (`tts en_US-ryan-high 1.wav`).
7. `core/card/card_test.go`: `TestOutro_coversBuiltInVoiceLanguages` → check
   `script.Languages(nil, nil)` defaults plus `piper.Defaults` keys (test-only
   import).
8. `mcp/server`: `Deps.Voices` type; `server_test.go` `voicesDir` helper →
   catalog builder.
9. `cli/main.go`, `mcp/main.go`: `provider.FromEnv` at startup, wire
   `TTS: eng`, `Voices` from `eng.Catalog()`; remove Piper constants. Add CLI
   test for startup failure if `run` structure allows (else manual check).
10. `tests/e2e/render_test.go`: use `provider.FromEnv(os.Getenv, ...)`.
11. Dockerfile: dev ENV, `runtime-base`, `runtime-piper`; Makefile `image`,
    `image-base`, `image-check`; compose target/tag.
12. Docs: ARCHITECTURE, CODE_QUALITY, README per "Docs to update".

## Checkpoints (Todo List)
- [x] CP1 — Neutral core types: steps 1–2; `go test -race ./core/voices/... ./core/support/wav/...` green. — both packages pass, race on
- [ ] CP2 — Piper adapter + factory: steps 3–5; `go test -race ./core/...` green, `make lint` clean (depguard sees new path), `core/tts` gone. — `go test -race ./...` in core green, golangci-lint 0 issues
- [x] CP3 — Consumers: steps 6–9; `make test vet lint` green in core, cli, mcp; Piper grep (success criterion 1) clean. — test/vet/lint green in core, cli, mcp, e2e; grep hits only piper pkg, provider.go and two test files that name `piper` on purpose (`provider_test.go`, `card_test.go`)
- [x] CP4 — Image: steps 10–11; `make dev-image image image-base image-check` pass; `docker run --rm screencaster-base screencaster render x.yaml` exits 1 with "no TTS provider"; `docker run --rm -e SCREENCASTER_TTS=foo screencaster screencaster-mcp` exits 1 with "unknown TTS provider". — images built, image-check ok; base CLI/MCP exit 1 "no TTS provider", `SCREENCASTER_TTS=foo` MCP exits 1 "unknown TTS provider"
- [ ] CP5 — End-to-end + docs: step 12; `make e2e` and `make e2e-runtime` green; `demos/cli-demo` renders unchanged; ARCHITECTURE/CODE_QUALITY/README updated.

### Risks & Mitigations
- **Dev image lacks ENV → e2e and local tests fail at startup.**
  - Mitigation: ENV in `dev` stage (step 11); unit tests inject `getenv`, never rely on process env.
- **MCP exits on startup when run from an old image / custom image without ENV** — breaking for anyone with a hand-rolled image.
  - Mitigation: clear error names the variable and available providers; README note. All shipped stages set it.
- **Voice name vs path mix-up** (renderer log, failure messages, call-log tests) changes visible output.
  - Mitigation: render log already shows names (`lang=name`); `failure.TTS` message unchanged in shape; renderer tests assert new call log.
- **Catalog/Synthesize drift** (voice file removed between Prepare and synth).
  - Mitigation: `Synthesize` looks up per call and returns `voice not installed: <name>` → becomes `failure.TTS`, abort path as today.
- **`image-check` sed breaks after move** (silent "no built-in voices found").
  - Mitigation: script already fails on empty match; CI `image` job runs it.

## Test Strategy
- **Unit:** `voices` Resolve/Options over catalog literals (all existing
  cases); `piper` Catalog (multi-dir, later wins, missing dir skipped,
  non-.onnx ignored, lang rule), `New` errors, `Synthesize` with a fake
  executable (stdout not inherited, stderr in `Error`, unknown voice);
  `wav` header parsing (moved); `provider.FromEnv` table (piper/unset/unknown/
  missing bin); renderer with fake `Synthesizer` asserting voice names;
  `mcp/server` get_options with catalog fakes; card outro coverage.
- **Integration:** MCP wiring over in-memory transport unchanged apart from
  `Deps.Voices` type.
- **E2E (Docker):** `make e2e` (dev image, real Piper via env) and
  `make e2e-runtime` (runtime-piper image) — EN and EN+PL renders, drift and
  NFR-001 checks unchanged.
- **Manual:** base image fails fast; unknown provider fails fast (CLI + MCP);
  extra voice in `/work/voices` appears in `get_options` without MCP restart;
  `docker compose run --rm screencaster render demos/...` works.

## Success Checklist
- [ ] All success criteria met (with evidence)
- [ ] `golangci-lint`, `go vet`, `go test -race` clean in core, cli, mcp, tests/e2e
- [ ] `make e2e` and `make e2e-runtime` green
- [ ] Code review approved (`screencaster-review`)
- [ ] ARCHITECTURE.md, CODE_QUALITY.md, README updated as listed
- [ ] No regressions in existing demo scripts (`demos/`, `testdata/`)

## Timeline & Estimates
- Implementation (steps 1–11): ~4 h
- Testing (unit rewrites, e2e, manual image checks): ~2 h
- Review + docs: ~1.5 h
- **Total:** ~7.5 h plus buffer

## Open Questions
- None blocking. Env var names `SCREENCASTER_PIPER_BIN` /
  `SCREENCASTER_PIPER_VOICES` chosen to share the `SCREENCASTER_` prefix;
  rename during review if preferred.
