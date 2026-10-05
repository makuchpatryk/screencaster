# Single-file demo — Implementation Plan

## Summary

Today a demo needs three files: the per-project `screencaster.yaml` (baseUrl, storageState, outputDir, voices), the storageState JSON and `demos/<name>.yaml`. This change makes each demo **one explicit, self-contained YAML file**. The demo script carries `baseUrl` (required), `storageState` (optional) and `outputDir` (optional). `screencaster.yaml` and the `core/config` package are removed. The goal is that a public-site demo is one file and one command, with no hidden project state for the developer or for Claude Code to know about.

## Success Criteria

- `screencaster render demos/x.yaml` renders a public site with **only** that file in the work dir: no `screencaster.yaml`, no storageState file.
- A demo with `storageState: auth/s.json` starts logged in. If the file is missing, validation fails before any browser or Piper starts, with `/storageState: storageState not found: /work/auth/s.json`.
- MCP `render_video`, `explore_page` and `get_options` work in a work dir without `screencaster.yaml`. `explore_page` takes an absolute `url` plus an optional `storageState`. `get_options` has no `baseUrl` field.
- If a `screencaster.yaml` is present, the CLI prints `screencaster.yaml is ignored; move its fields into the demo script` to stderr on every render, and the MCP server logs the same line once at startup. Nothing reads the file.
- No source file imports `core/config` (the package is deleted). `make test`, `make vet`, `make lint` and `make e2e` are green.

## Scope & Constraints

- **In scope:**
  - schema fields `baseUrl`, `storageState` and `outputDir`
  - deleting `core/config`
  - removing the project-level voice defaults from `core/voices`
  - the renderer `Plan`
  - the CLI and MCP handlers, the `create_demo` prompt and the `render_video` description
  - testdata, the unit and e2e tests
  - README, PRD, ARCHITECTURE and CODE_QUALITY
- **Out of scope:**
  - a migration tool
  - any fallback to `screencaster.yaml`
  - a CLI `--base-url` override
  - resolving paths relative to the demo file
  - env-var voice defaults
  - backward compatibility for old demo YAML (pre-release, `demos/` is empty)
- **Hard constraints:**
  - BR-001: determinism, no LLM at render time
  - NFR-003: offline
  - BR-008 and ADR-45: one render at a time, with the render lock unchanged
  - FR-002 and the "fail early" rule: every check runs before the browser or TTS starts
  - Strict decoding: the schema keeps `additionalProperties: false`
  - The MCP binary's stdout stays protocol-only
- **Trade-offs:**
  - **Explicitness over environment-agnosticism.** Today BR-010 keeps scripts free of the target host, so one script can run against dev, staging or prod by changing a single config file. After this change, retargeting a demo means editing its `baseUrl`. The user chose this. BR-010 is rewritten (see Fit with Project Docs).
  - **Fewer packages over DRY for the storageState check.** `core/config` is deleted (user choice). Resolving and stat-ing `storageState` against the work dir therefore appears in two places, `renderer.Prepare` and the `explore_page` handler, and is recorded as known debt.
  - **Stricter baseUrl check over one source of truth.** The schema requires a non-empty string, and `script.Parse` also checks it is an absolute http(s) URL, as `config.Load` did (user choice). The URL rule lives in Go and the shape lives in the schema.

## Architecture & Design

### High-Level Flow

```
CLI:  render demos/x.yaml
  run(): if <work>/screencaster.yaml exists -> stderr warning
  renderer.Render -> Prepare(req, installed)
        read script -> script.Parse            (schema + baseUrl absolute http(s))
        langs := script.Languages(override, s.Languages)
        errs  := script.Validate(s, langs)
               + voices.Resolve(langs, s.Voices, installed)
               + storageState check            (resolve vs WorkDir, must exist)
        outputDir := s.OutputDir or "output", resolved vs WorkDir
        -> Plan{Script, Languages, Voices, StorageState, OutputDir}
  recorder.Input{BaseURL: plan.Script.BaseURL, StorageState: plan.StorageState}
  publish(plan.OutputDir, ...)

MCP:  startup: if <work>/screencaster.yaml exists -> slog.Warn (stderr), once
  render_video  -> renderer.Prepare (unchanged call)
  explore_page{url (absolute), storageState?, actions?}
        script.AbsoluteHTTP(url) else tool error
        storageState resolved vs WorkDir, must exist
        explorer.Explore{BaseURL: url, URL: url, StorageState, Actions}
  get_options   -> voices.Options(installed)   (no baseUrl, no config)
```

Using `url` as the explorer's `BaseURL` means a relative `goto` inside `actions` resolves against the page being explored. That follows the same URL-reference rules as the executor (BR-010) and needs no change in `core/explorer` or `core/executor`.

### Key Changes

- **`core/script/script.schema.json`:**
  - `required` becomes `["name", "baseUrl", "steps"]`.
  - New properties:
    ```json
    "baseUrl":      { "description": "Absolute http(s) URL of the app. Relative goto URLs resolve against it.", "type": "string", "minLength": 1 },
    "storageState": { "description": "Playwright storageState JSON, relative to the working directory. Omit for a fresh, logged-out session.", "type": "string", "minLength": 1 },
    "outputDir":    { "description": "Where MP4s are written, relative to the working directory. Default output.", "type": "string", "minLength": 1 }
    ```
  - The `voices` description changes to "Falls back to the built-in en/pl voices."
- **`core/script/script.go`:**
  - `Script` gains `BaseURL string \`json:"baseUrl"\``, `StorageState string \`json:"storageState"\`` and `OutputDir string \`json:"outputDir"\``.
  - New exported `func AbsoluteHTTP(raw string) bool`, moved from `config.Load`: `url.Parse` succeeds, the scheme is http or https, and the host is non-empty.
  - `Parse` calls it after the schema passes. On failure it returns `failure.ValidationErrors{{Pointer: "/baseUrl", Message: "baseUrl must be an absolute http or https URL: " + s.BaseURL}}`.
  - Defaults stay out of `Parse`, which keeps returning the file's content.
- **`core/script/example.yaml`:** add `baseUrl: http://host.docker.internal:3000`. A test already keeps the example valid.
- **`core/voices/voices.go`:**
  - `Resolve(langs, scriptVoices, inst)` and `Options(inst)` drop the `cfgVoices` parameter. Leaving an always-empty map argument would be dead input (YAGNI).
  - Order becomes: script voice, then built-in, then the error `no voice for language: <lang>`.
- **`core/renderer/renderer.go`:**
  - `Plan` drops `Cfg config.Config` and gains `StorageState string` (absolute, or empty) and `OutputDir string` (absolute).
  - `Prepare` drops `config.Load`. It resolves `storageState` against `req.WorkDir` and stats it, adding `failure.ValidationError{Pointer: "/storageState", Message: "storageState not found: <abs>"}` to the same `errs` list as the script and voice errors, so every problem is reported at once.
  - It resolves `outputDir` (default `output`). A private `const defaultOutputDir = "output"` moves here from `core/config`.
  - `Render` builds `runDir` from `req.WorkDir`. `renderLanguage` uses `plan.Script.BaseURL` and `plan.StorageState`, and `publish` uses `plan.OutputDir`.
- **`core/config/`:** delete `config.go` and `config_test.go`.
- **`cli/render.go`:**
  - `run` checks `filepath.Join(workDir, "screencaster.yaml")` before executing the command and, if it exists, writes the warning to `stderr`.
  - The check lives in `run`, not `main`, so `render_test.go` can assert it through the injected writer.
- **`mcp/main.go`:** after the logger is set up, stat `<work>/screencaster.yaml` and call `slog.Warn` once.
- **`mcp/server/server.go`:**
  - Delete the `config` import and the "Config is loaded per call" comment on `Deps`.
  - `exploreIn` becomes:
    ```go
    type exploreIn struct {
        URL          string        `json:"url" jsonschema:"absolute http(s) URL of the page to open"`
        StorageState string        `json:"storageState,omitempty" jsonschema:"Playwright storageState JSON relative to the project directory; omit for a logged-out session"`
        Actions      []script.Step `json:"actions,omitempty" jsonschema:"script steps to replay before the snapshot (narration is ignored)"`
    }
    ```
  - `explorePage` returns the tool error `url must be an absolute http or https URL: <url>` when `!script.AbsoluteHTTP(in.URL)`. It resolves and stats `StorageState` (`storageState not found: <abs>`), then calls `Explore` with `BaseURL: in.URL`.
  - `optionsOut` drops `BaseURL`. `options` drops `config.Load`.
  - The tool descriptions change. `explore_page` says "`url` must be absolute; pass the demo's baseUrl joined with the path, and its storageState if the app needs a login". `get_options` drops "the app's baseUrl". In the `render_video` rules, "URLs are paths relative to the script's `baseUrl`" and "authentication comes from the script's optional `storageState`".
- **`mcp/server/create_demo.md`:**
  - Step 1 no longer mentions baseUrl.
  - Step 2 also asks for the app's base URL and, if a login is needed, the storageState path.
  - Step 3 calls `explore_page` with absolute URLs and that storageState.
  - Step 4 writes `baseUrl` and `storageState` (if any) into the YAML.
- **Dependencies / Dockerfile:** none. `core/config` lives inside the `core` module, so `go.mod`, `go.work` and `.golangci.yml` don't change (verified: depguard has no `config` rule).
- **Data model (SQLite):** none. Jobs store the script path, and the worker re-runs `Prepare` against the file.

### Fit with Project Docs

- **ARCHITECTURE.md:**
  - §3: remove the `config/` line.
  - §4: the sequence participant `script+config+voices` becomes `script+voices`, and rule 1 drops "Config,".
  - §7: "URL resolution against `baseUrl`" now refers to the script's.
  - §8: "storageState, baseURL" come from the tool input.
  - §9: the Validation row's "Produced by" becomes "script, voices, renderer (paths)".
  - §11: remove the `screencaster.yaml` line, and `auth/…` becomes "path from the demo's `storageState`; optional".
  - §16: new decisions 58–60 (below).
  - Dependency rules are unchanged. `cli` and `mcp` still reach `core` only, `core/script` stays I/O-free (a URL parse is not I/O), and the stat lives in `renderer`, which already does file I/O.
- **CODE_QUALITY.md:**
  - The Separation table row `core/config, core/script` becomes `core/script`.
  - Hard rule "Paths, `baseUrl` and `outputDir` come from `core/config`" becomes "come from the demo script via `renderer.Plan` (the `explore_page` handler gets them from its input)".
  - In Interface segregation, the `resolveVoices(...)` example loses `cfgVoices`.
  - In Working agreements, the example message `config not found: …` is replaced by `storageState not found: …`.
  - Known debt gains a bullet: "storageState is resolved and checked in `renderer.Prepare` and in the `explore_page` handler; extract on the third use."
  - KISS and YAGNI are served: one package and one config source fewer.
- **PRD.md:**
  - BR-010: URLs are relative to the script's `baseUrl`. Auth comes only from the script's optional storageState. No login steps. The environment-agnostic rationale is replaced by "one self-contained file per demo".
  - FR-001: rewritten as "Demo target settings". It covers the fields, validation and errors above. AC1 becomes "a script without `baseUrl` fails schema validation (`/baseUrl`) before launching a browser". AC2 (built-in voices) is kept.
  - FR-002: list `baseUrl`, `storageState` and `outputDir`.
  - BR-011: script voice, then built-in.
  - FR-011: drop "config" from validation.
  - FR-017: new input `{url, storageState?, actions?}`, error states.
  - FR-018: output without `baseUrl`. Remove the "config missing" edge case.
  - §11 data list: drop `screencaster.yaml`.
  - The UF error states that mention config missing.
  - Glossary "storageState … Provided by config": change to "by the demo script".
  - Decisions log: add 58–60. Mark decision 15 as superseded by 58.
- **New decisions (ARCHITECTURE §16 and PRD log):**
  - 58: Each demo is one self-contained YAML. `baseUrl` is required, `storageState` and `outputDir` are optional, and `screencaster.yaml` is removed (a warning is shown if one is present). Supersedes decision 15.
  - 59: `explore_page` takes an absolute `url` plus an optional `storageState`. The url is also the base for relative gotos in `actions`.
  - 60: No project-level voice defaults. Voice resolution is script, then built-in.

### Alternative Approaches Considered

- **Keep `screencaster.yaml` as an optional fallback.**
  - Pro: environment-agnostic scripts still work.
  - Con: two sources of truth, and the demo is not self-contained.
  - Rejected by the user.
- **Shrink `core/config` to path helpers** (Track A).
  - Pro: the storageState resolution exists once, shared by the renderer and `explore_page`.
  - Con: a package kept alive for about 20 lines.
  - Not chosen: the user picked deletion, and the duplication is recorded as debt.
- **baseUrl rule as a schema `pattern` only** (Track A).
  - Pro: a single source of truth.
  - Con: a regex is looser than `url.Parse`, and the error text is generic.
  - Not chosen: the user picked a Go check that keeps today's message.
- **Resolve paths relative to the demo file.**
  - Pro: portable folders.
  - Con: changes the established `/work` rule and surprises Docker users.
  - Rejected by the user.
- **Per-call MCP warning** (Track B).
  - Pro: hard to miss.
  - Con: noisy log.
  - Not chosen: the user picked a single warning at startup.

## Implementation Steps

1. `core/script/script.schema.json`: add `baseUrl` to `required` and add the three properties. Update the `voices` description.
2. `core/script/script.go`: add the `Script` fields, `AbsoluteHTTP`, and the baseUrl check in `Parse`. Add `baseUrl` to `core/script/example.yaml`.
3. Testdata:
   - Add `baseUrl: http://host.docker.internal:3000` to every `testdata/scripts/valid/*.yaml` and `testdata/scripts/invalid/*.yaml`, so each invalid sample still fails only on its own defect.
   - New invalid samples: `missing-baseurl.yaml` and `baseurl-relative.yaml` (`baseUrl: /app`).
   - New valid sample: `all-target-fields.yaml` with `storageState` and `outputDir`.
4. `core/script/script_test.go`: table cases for the new samples. `AbsoluteHTTP` cases: http, https, `ftp://x`, `/path`, `http://`, an empty string.
5. `core/voices/voices.go` and its test: drop `cfgVoices` from `Resolve` and `Options`. Delete the "config beats built-in" cases and keep "script beats built-in".
6. `core/renderer/renderer.go`: new `Plan` fields, `Prepare` without config (storageState check collected into `errs`, outputDir default and resolve), `Render`/`renderLanguage`/`publish` reading from `Plan` and `req.WorkDir`. Remove the import.
7. `core/renderer/renderer_test.go`:
   - The project helper stops writing `screencaster.yaml`, and its scripts carry `baseUrl`.
   - Replace the "config not found" cases with:
     - `TestPrepare_storageStateOptional`
     - `TestPrepare_storageStateMissingIsValidationError` (pointer `/storageState`, reported together with a voice error)
     - `TestPrepare_outputDirDefaultsToOutput`
     - `TestPrepare_outputDirRelativeToWorkDir`
     - `TestPrepare_ignoresScreencasterYAML` (a stray file with a different baseUrl does not change the plan)
8. Delete `core/config/`. `go build ./...` in `core` must succeed with no references left.
9. `cli/render.go` and its test:
   - Add the warning in `run`.
   - Replace the `config not found` table case with `TestRun_warnsAboutScreencasterYAML` (stderr has the line, the render still runs) and a no-warning case.
10. `mcp/main.go`: the startup warning via `slog.Warn`.
11. `mcp/server/server.go`: the `exploreIn`, `explorePage`, `optionsOut` and `options` changes, the tool descriptions and the `Deps` comment. Update `create_demo.md`.
12. `mcp/server/server_test.go`:
    - The env setup stops writing `screencaster.yaml`, and the test scripts carry `baseUrl`.
    - Replace the two "config not found" cases with:
      - explore with a relative url gives a tool error
      - explore with a missing storageState gives `storageState not found: …`
      - explore with no storageState passes `""` to the fake explorer
      - explore passes `BaseURL == URL`
    - `get_options` JSON has no `baseUrl` key.
13. `tests/e2e/render_test.go`:
    - `project()` stops writing `screencaster.yaml`.
    - `renderScript` and `failingScript` become format templates that get `baseUrl: <fixture URL>` and `storageState: auth/storageState.json`.
    - New `TestRender_publicSiteNoStorageState`: a one-step `goto` + `wait` script with no storageState and no other files in the work dir; the MP4 passes ffprobe.
    - `explore_test.go`: check the explorer `Input` (BaseURL is now the URL). Its callers already pass absolute values, so it should need only a recheck.
14. Docs: README (Quick start steps 3 and 4 merged into one demo file, MCP section, tool list), then PRD, ARCHITECTURE and CODE_QUALITY as listed in Fit with Project Docs.

## Checkpoints (Todo List)

- [x] CP1 — Script format: steps 1–4. `go test ./script/...` in `core` green; the example and all testdata samples behave as expected. — race clean; `ValidateSteps` also needed a placeholder `baseUrl`
- [x] CP2 — Voices + renderer, config deleted: steps 5–8. `go test -race ./...` in `core` green; `grep -rn core/config --include=*.go .` is empty. — race clean, grep empty
- [x] CP3 — Entry points: steps 9–12. `go test -race ./...` in `cli` and `mcp` green; `make vet` and `make lint` clean. — test, vet, lint all pass in every module
- [x] CP4 — E2E: step 13. `make e2e` green (drift ≤ 150 ms, ffprobe, explore selector reused) and `make e2e-runtime` green. — e2e: 11 tests pass, drift 36 ms, ratio 1.85; e2e-runtime: drift −58 ms, ratio 1.83
- [x] CP5 — Docs + final check: step 14. README, PRD, ARCHITECTURE and CODE_QUALITY updated (decisions 58–60, known debt bullet); `grep -rn "screencaster.yaml" docs README.md mcp core cli` matches only the warning, its tests and the decision text; full `make test vet lint` green. — grep also hits README warning note and PRD FR-001/UF-002 text; test, vet, lint green

### Risks & Mitigations

- **Risk:** an invalid testdata sample starts failing on a missing `baseUrl` instead of its own defect, so the test passes for the wrong reason.
  - Mitigation: step 3 adds `baseUrl` to every sample. The script tests assert the exact pointer and message, not just "has errors".
- **Risk:** the MCP startup warning is written to stdout and corrupts the stdio session.
  - Mitigation: use `slog.Warn` after the logger is pointed at stderr. The existing MCP wiring test over the in-memory transport would surface a stray frame. Review checklist item 8.
- **Risk:** Claude, through `create_demo`, keeps sending relative `url`s to `explore_page` from habit or stale prompt text.
  - Mitigation: the tool error names the rule. The prompt and the tool description both say "absolute".
  - Fallback: a later decision could accept a path plus `baseUrl` as tool input. Not built now (YAGNI).
- **Risk:** a job queued before the upgrade points at an old-format script and fails in the worker.
  - Mitigation: jobs never resume across restarts (BR-009 marks them `interrupted`). A new job is validated synchronously by `Prepare` in `render_video`, so no job row is created for an old-format script.
- **Risk:** retargeting environments now needs edits to every demo, which loses BR-010's original intent.
  - Mitigation: accepted by the user and recorded in decision 58. Open question below on a future `--base-url` override.

## Test Strategy

- **Unit tests:**
  - `core/script`: schema and `AbsoluteHTTP`, plus the new testdata.
  - `core/voices`: resolution without config.
  - `core/renderer`: `Prepare` paths, defaults, collected errors, stray-config immunity, with the fake `Recorder`, `Synthesizer` and `Assembler` checking that `recorder.Input` gets the script's `BaseURL` and the resolved `StorageState`.
  - `cli`: the warning line and exit codes.
- **Integration:**
  - `mcp/server` over the in-memory transport: `explore_page` input validation and storageState handling with the fake explorer, the `get_options` shape, and `render_video` without `screencaster.yaml`.
- **E2E (dev image and runtime image):**
  - The existing render, drift, NFR-001 and explore-selector tests, now using the single-file format.
  - New public-site render with no storageState.
- **Manual testing:**
  - Run `docker run … screencaster render demos/wiki.yaml` in an empty folder holding just the demo file (from the README example) against a public site.
  - Run the `/mcp__screencaster__create_demo` flow in Claude Code once: the prompt asks for the base URL, the explore calls use absolute URLs, and the YAML it writes contains `baseUrl`.
  - Put an old `screencaster.yaml` into the folder and check that the warning appears on the CLI and in the MCP stderr log.

## Success Checklist

- [ ] All success criteria met (with evidence)
- [ ] `golangci-lint`, `go vet`, `go test -race` clean in `core`, `cli`, `mcp`, `tests/e2e`
- [ ] E2E green in the dev image and the runtime image
- [ ] Code review approved (`screencaster-review`)
- [ ] README, PRD, ARCHITECTURE, CODE_QUALITY updated (decisions 58–60, BR-010, FR-001, FR-017, FR-018, known debt)
- [ ] `testdata/scripts/*` and `core/script/example.yaml` valid in the new format

## Timeline & Estimates

- Phase 1 (implementation, steps 1–2, 5–6, 8–11): ~4 h
- Phase 2 (tests and testdata, steps 3–4, 7, 12–13 plus the e2e runs): ~3 h
- Phase 3 (docs, review, polish, step 14): ~2 h
- **Total:** ~9 h, plus buffer

## Open Questions

- [ ] Do we need a CLI `--base-url` override later, to render one demo against staging and prod without editing it? Not planned (YAGNI); revisit if retargeting becomes common.
