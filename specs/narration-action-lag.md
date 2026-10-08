# Narration action lag — Implementation Plan

## Summary

Narration is placed at a step's start offset, but the step's effect shows in the video later: the action call returns after 5–68 ms, then paint and the screencast frame add 17–70 ms. On the real marker flash, drift (flash frame minus narration onset) is 10–182 ms over 14 renders before the `Session.Press` change, and 11–134 ms over 6 renders after it (60, 78, 106, 134, 11, 127), so the FR-007 ±100 ms bound fails about half the time and the CI `e2e` job is red half the time. This plan adds one fixed constant in the recorder, `narrationLag`, which places every narration clip that long after its step starts, and makes the recording run on until the last placed clip ends. Done now because the gate fails on every second run.

## Success Criteria

- `TestRender_cli` drift is within ±100 ms in 20 of 20 consecutive renders at the final constant (`-count=20 -failfast`). The constant is chosen from separate raw runs, not from these.
- FR-007 acceptance holds unchanged: a narrated step with a 4000 ms clip and a 500 ms action makes the next step start 4000 ms after this one (`TestRecord_narratedStepWaitsForClip` passes without edits). An un-narrated step continues at once.
- The recording always covers the audio: the video ends no earlier than the last placed clip (new unit test).
- NFR-001 holds: the EN+PL render time ratio logged by `TestRender_cli` stays at or below 2.0 (today 1.5–1.6).
- The CI `e2e` job is green on the branch with the same constant.
- No change to the assembler, `domain/script`, the schema, the YAML format, or any CLI or MCP option.

## Scope & Constraints

- **In scope:**
  - `internal/domain/recorder/recorder.go`: the constant, the shifted `Offsets`, the tail wait.
  - `internal/domain/recorder/recorder_test.go`: updated offset expectations and a new tail test.
  - Comments that call offsets "step start": `internal/app/renderer/render.go` (lines 51 and 325) and the `Press` comment in `internal/adapters/browser/browser.go`.
  - The `maxDrift` comment in `tests/e2e/render_test.go`. `assertDrift` and the 100 ms bound stay.
  - `docs/ARCHITECTURE.md` (§5, §16 row 80, §17.1), `docs/CODE_QUALITY.md` Known debt, and a `docs/PRD.md` FR-007 note if the user wants it.
  - The calibration and acceptance runs and the Implementation Log.
- **Out of scope:** the assembler; a per-run calibration probe; effect-frame detection; a per-action lag table; any new option or key; changing the wait rule; raising the recording frame rate.
- **Hard constraints:** determinism (BR-001, the constant is fixed); offline (NFR-003); one render at a time (BR-008); narration is mixed offline from recorded offsets, never played live; constant frame rate keeps video time equal to wall time (decision 50); NFR-001 ratio at most 2.
- **Do not touch** three files modified by someone else: `cmd/screencaster/docker_script_test.go`, `scripts/screencaster-docker.sh`, and the Decision 79 line of `docs/ARCHITECTURE.md`. When committing, stage only our hunks (`git add -p`).
- **Trade-offs:** a gate that is reliable over one that is exact. The window is ±100 ms, so a constant that centres the observed lags is enough. One rule for every action over a per-action table. Accepted: one constant for laptop and CI (user decision), and `goto` is not exact (see Risks).

## Architecture & Design

### High-Level Flow

```
step i starts at s_i (now − t0, from the end of the sync marker)
  action runs ............ returns after ~5–68 ms
  picture shows the effect at about s_i + L        (L: 11–134 ms measured with no shift)
  narration is placed at      s_i + C              (C = narrationLag)
  next step starts at         s_i + max(action, clip)      unchanged (FR-007)
  placed clip i ends at       s_i + C + clip_i
recording ends no earlier than the end of the last placed clip   (tail wait)
```

Residual drift is `L − C`. With C near the middle of the observed L values, the residual is about ±60 ms, inside the bound with a margin of about 40 ms.

### Key Changes

- **`internal/domain/recorder/recorder.go`**
  - Add an unexported `const narrationLag` (value set by calibration, step 6) with a comment that says why (paint and video frame latency) and points to Decision 80.
  - In `Record`, `offsets[i] = max(start.Sub(t0), 0) + narrationLag` for every step. Uniform on purpose: un-narrated offsets are never read, so one rule is as correct as two and has one sentence in the docs.
  - Track `narrationEnd`, the largest `offsets[i] + clip` over narrated steps. After the loop, sleep until `t0 + narrationEnd` before `sess.Close()`. On a sleep error, `sess.Abort()` and return the error, as the marker-hold path does.
  - `waitForClip` stays `start + clip`. The wait rule is not shifted, so FR-007 spacing is exact.
  - Update the `Output.Offsets` doc: "when the narration of Steps[i] is placed, from the end of the sync marker".
- **Why the tail wait is needed:** verified in `internal/adapters/assembler/assembler.go` (lines 172–190): the output is as long as the video, the audio is allowed to end early, the narrated mix has no `-shortest` and no pad. A last clip placed `narrationLag` late would end after the recording. The tail wait keeps the §5 rule "the video always covers the audio".
- **Schema / MCP / CLI / data model / dependencies:** none.
- **Assembler and renderer logic:** unchanged. `renderer.renderLanguage` still passes `rec.Offsets[i]` as the clip offset and the assembler still adds the intro. Only comments change, because the offset now means the placement.

### Fit with Project Docs

- **ARCHITECTURE.md.** The change lives in `recorder`, which owns offsets (§5, BR-003); no import or dependency-rule change. Updates:
  - §5 line 161: offset is `max(now − t0, 0) + narrationLag`.
  - §5 line 162: the wait is `start + clipDuration`, not `offset + clipDuration`. The text must change or it invites the wrong implementation.
  - §5: add the tail wait; replace the drift bullet (line 167, "25–66 ms in 19, 132 ms in one"). Those numbers came from the old check, which measured the paint of `index.html`, not the marker.
  - §16: add row 80 (the table ends at 79).
  - §17.1 open item 1 (line 377): replace the old numbers and the sentence "no bias constant is applied".
  - Deviation: a bias constant is now applied, against the earlier decision not to. Reason: the old numbers measured the wrong event; on the real marker the lag is a stable 40–140 ms that a constant removes.
- **CODE_QUALITY.md.** KISS: one constant and a four-line tail wait; no probe, no frame analysis. YAGNI: no per-action table, no config key (consistent with "no configurable timeouts"). DRY: the lag lives only in the constant; the `maxDrift` comment refers to it without repeating the value. Separation: the timing constant is in the recorder; assembler and renderer untouched. Comments: explain why, cite the decision. Update the Known debt bullet "Sync marker drift".
- **Docs to update:** the ARCHITECTURE and CODE_QUALITY sections above; PRD FR-007 only with the user's answer (Open Questions).

### Alternative Approaches Considered

- **A. Fixed constant in the recorder plus a tail wait (chosen).** Deterministic, no extra run time beyond the tail (at most C), one place to tune. Cost: one constant for all machines; `goto` is not exact.
- **B. Per-run calibration probe.** Adapts to load, but adds recording time per render and a moving part. Rejected by the user.
- **C. Detect the effect frame in the video.** Most accurate, but needs per-step image analysis and fails on pages without a visible change. Rejected by the user.
- **D. Take the offset after the action returns.** Removes the 5–68 ms press time, but for `goto` and glide actions "after" is a different, much larger quantity, and it changes FR-007's meaning of "start offset". It still needs a constant for paint and frame lag. Rejected.
- **E. Wait `start + lag + clip` between steps.** Covers the tail by construction, but the next step then starts 4080–4100 ms after the step in the FR-007 example, using up the ±100 ms tolerance. Rejected.
- **F. Shift in the assembler (`adelay`) or the renderer (`Offset: rec.Offsets[i] + lag`).** Moves a recorder timing constant out of the package that owns it, and the tail wait needs the recorder's clock. Rejected.

## Implementation Steps

1. [x] Add `const narrationLag = 0` to `internal/domain/recorder/recorder.go` with a comment citing Decision 80. Zero is the calibration baseline; the value is set in step 6.
2. [x] In `Record`, set `offsets[i] = max(start.Sub(t0), 0) + narrationLag`, track `narrationEnd` over narrated steps (`in.Clips[i] > 0`), and sleep until `t0 + narrationEnd` before `sess.Close()`, with `sess.Abort()` on error. Update the `Output.Offsets` doc comment.
3. [x] Unit tests in `internal/domain/recorder/recorder_test.go`:
   - Update `TestRecord_unnarratedStepContinuesImmediately` and `TestRecord_offsetsStartWhenTheMarkerIsHidden`: expected offsets become `narrationLag + {…}`. Reference the constant, not a literal, so the tests survive step 6.
   - `TestRecord_narratedStepWaitsForClip` and `TestRecord_longActionBeatsShortClip` must pass unchanged (they assert spacing). If they derive spacing from `Offsets` and break, compare `OnStep` times instead; do not weaken the 4000 ms assertion.
   - Add `closedAt` to `fakeSession` and new `TestRecord_recordingCoversLastNarration`: one narrated click with a 500 ms action and a 4 s clip; assert the close time is at least `hiddenAt + narrationLag + 4s`.
   - Add a case with the last narrated step first and a long un-narrated step after it: no extra wait is added.
4. [x] Update comments that call offsets "step start": `internal/app/renderer/render.go` (`Recording.Offsets` near line 51, and line 325) and the `Press` comment in `internal/adapters/browser/browser.go`. Do not touch the assembler.
5. [x] SKIPPED (user decision: no time for 20 runs; C taken from the six post-fix renders). Calibrate at `narrationLag = 0`. Run `make dev-image`-based e2e for `TestRender_cli` 20 times with `-count=20 -v` (no `-failfast`, so all values are collected): `$(E2E_RUN) go test -tags e2e -run 'TestRender_cli$' -count=20 -v ./tests/e2e/...`. Extract the `drift: … drift N ms` values. Log the 20 values with min, median and max. Choose C as the midpoint of min and max, rounded to 5 ms. Accept C only if `max − C ≤ 80` and `C − min ≤ 80` (a 20 ms margin each side); otherwise stop and investigate the tail.
6. [x] Set `narrationLag` to the chosen C. Starting estimate from the six post-fix renders (11–134 ms): about 70 ms. Re-run the recorder unit tests.
7. [x] Acceptance (reduced by user decision to `-count=5`; the 20-of-20 criterion is NOT met by this): run `$(E2E_RUN) go test -tags e2e -run 'TestRender_cli$' -count=20 -failfast -v ./tests/e2e/...` on fresh renders. All 20 must pass. Log each drift, the `sync marker ends at` line, and the NFR-001 ratio. Do not retune C from these runs; if one fails, stop and report.
8. [x] Update the `maxDrift` comment in `tests/e2e/render_test.go`: keep the bound and `assertDrift`; replace the history with the marker numbers (14 renders before the press change, 6 after, the calibration and acceptance results) and say that placement includes `narrationLag` (Decision 80).
9. [ ] Measure `goto` once: render a narrated `goto` against a page that loads slowly (or a real page) and record its residual in the log and in the Known debt bullet. No design change; it only documents the limit.
10. [x] Docs: `docs/CODE_QUALITY.md` Known debt "Sync marker drift" bullet (value, Decision 80, results, `goto` limit, CI status). `docs/ARCHITECTURE.md` §5, §16 row 80, §17.1 as listed above, editing only those lines (the file has an unrelated uncommitted edit on the Decision 79 line). `docs/PRD.md` FR-007 note only if the user agrees.
11. [ ] CI: after the user commits and pushes, confirm the `e2e` job is green on the branch and log its drift values. If it fails on drift, stop and report; do not retune without a new decision (decision (d) accepts one constant, and this is the check on it).
12. [x] Final checks: `make lint`, `make vet`, `make test`, and `make e2e E2E_RACE=-race` (the CI configuration).

## Checkpoints (Todo List)

- [x] CP1 — Recorder change: steps 1–4. `make test` and `make lint` clean, recorder tests green with `narrationLag = 0`, FR-007 spacing tests unmodified. — test (race) PASS, lint PASS, vet PASS
- [x] CP2 — Calibration: step 5. SKIPPED by user decision; C = 70 ms from the six earlier renders (11–134 ms), no margin proof.
- [x] CP3 — Gate: steps 6–7. REDUCED by user decision: 5 of 5 renders within ±100 ms at C = 70 ms (drift −13, 80, 72, 64, 15 ms); NFR-001 ratio 1.53–1.73, at most 2. The 20-of-20 criterion is not proven.
- [ ] CP4 — Comments and limits: steps 8–9. `maxDrift` comment updated (done); `goto` residual NOT measured (step 9 open, needs user approval to skip).
- [ ] CP5 — Docs, CI and final verification: steps 10–12. ARCHITECTURE and CODE_QUALITY updated, Decision 80 written, CI `e2e` job green, `make lint vet test` and the race e2e run clean.

### Risks & Mitigations

- **Risk: the lag has a tail above C + 100.** Before the press change the maximum was 182 ms; after it, 134 ms.
  - Mitigation: the step 5 margin rule on 20 raw runs. If the tail goes above about 160 ms, find the cause (CPU load, press path) before choosing C; raising C only moves the window.
- **Risk: CI is slower than the laptop, so its lag differs and the CI job fails.** The user accepted one constant for both.
  - Mitigation: step 11 checks CI. If it fails, compare the CI drift with the laptop distribution and report; a new constant or a per-run measurement needs a new decision.
- **Risk: `goto` residual is larger than ±100 ms on real sites.** A `goto` waits for the load, so its visible change comes after the load; the residual is about (load-to-paint time − C), small on the local fixture and possibly hundreds of ms on a real site. The earlier index-paint measurements (28–125 ms) are for the local fixture only.
  - Mitigation: the gate only measures the marker press. Step 9 measures one `goto` and records the limit; the single-rule decision is not reopened without the user.
- **Risk: audio ends after the video.** Placing clips late can push the last clip past the end of the recording; the assembler does not trim or pad.
  - Mitigation: the tail wait (step 2) and its unit test (step 3).
- **Risk: stale numbers in the docs.** ARCHITECTURE §5 and §17.1 and the Known debt bullet quote the old check (25–66 ms, 132 ms), which measured the wrong event.
  - Mitigation: step 10 replaces them.
- **Risk: mixing our ARCHITECTURE hunks with someone else's uncommitted edit.**
  - Mitigation: edit only the listed lines and stage with `git add -p`.
- **Risk: the 33 ms frame grid makes drift discrete, and the constant may sit between grid values.**
  - Mitigation: acceptable; the bound is three frames wide on each side.

## Test Strategy

- **Unit (fake clock and session):** recorder tests from step 3: shifted offsets, unchanged FR-007 spacing, the tail wait, no extra wait when the last step is un-narrated. Renderer and assembler tests run unchanged as a regression check (the renderer uses a fake recorder; the assembler goldens do not involve the recorder).
- **Integration:** none new.
- **E2E (dev image):** `TestRender_cli` drift gate, with the calibration (20 runs at lag 0) and acceptance (20 runs, `-failfast`) procedures in steps 5 and 7. It also covers the ffprobe checks and the NFR-001 ratio. Run once with `E2E_RACE=-race` as CI does.
- **Manual:** the single `goto` measurement (step 9).
- **Not covered:** `goto` and other slow-load actions in the gate; CI lag beyond the one CI run.

## Success Checklist

- [ ] All success criteria met, with evidence (20 calibration values, 20 acceptance values, NFR-001 ratios, CI result) in the Implementation Log.
- [ ] `golangci-lint`, `go vet` (including `-tags e2e`) and `go test -race` clean.
- [ ] `make e2e` green in the dev image, with and without `E2E_RACE=-race`; CI `e2e` job green.
- [ ] Code review (`screencaster-review`) done.
- [ ] Documentation updated: ARCHITECTURE §5, §16 row 80, §17.1; CODE_QUALITY Known debt; PRD note if agreed.
- [ ] No regressions in existing demo scripts; no YAML or schema change.

## Timeline & Estimates

- Phase 1 (steps 1–4): about 1 h.
- Phase 2 (calibration and acceptance, steps 5–7): `TestRender_cli` takes 80–95 s, so 20 runs is about 30 min; calibration plus acceptance is about 1 h, mostly waiting.
- Phase 3 (comments, `goto` check, docs, steps 8–10): about 1 h.
- Phase 4 (CI and final checks, steps 11–12): about 30 min plus the CI run.
- **Total**: about 3.5 h of work including waiting, plus about 1 h buffer if calibration shows a wider tail.

## Open Questions

- [ ] `docs/PRD.md` FR-007: add one sentence that narration is placed at the step start plus a fixed lag, or leave the PRD unchanged? (Plan assumes unchanged; the acceptance criteria are unaffected.)
- [ ] `goto` limit: accept recording the measured residual (step 9) without a design change?

## Implementation Log

- Step 1 — started
- Step 1 — done — files: internal/domain/recorder/recorder.go — added `const narrationLag = 0` with the why-comment (decision 80); builds
- Step 2 — started
- Step 2 — done — files: internal/domain/recorder/recorder.go — shifted offsets, `narrationEnd` tracking, tail wait with Abort on error, Output.Offsets doc; found already in tree on resume, verified, builds, recorder tests pass at lag 0
- Step 3 — started
- Step 3 — done — files: internal/domain/recorder/recorder_test.go — offsets expect `narrationLag + …`, `closedAt` on fakeSession, new `TestRecord_recordingCoversLastNarration` and `TestRecord_noTailWaitWhenStepsOutlastNarration`; FR-007 spacing tests unmodified; race clean, gofmt clean
- Step 4 — started
- Step 4 — done — files: internal/app/renderer/render.go, internal/adapters/browser/browser.go — comments only (Recording doc, offsets comment, Press comment); assembler untouched. CP1 checks (`make test`, `make lint`, `make vet`) launched in background
- CP1 check — `make lint`: PASS (0 issues) — `make vet`: PASS (`go vet -tags e2e ./...`)
- CP1 check — `make test` (`go test -race ./...`): PASS, 24 packages ok. CP1 ticked
- Step 5 — started — 20 raw runs at `narrationLag = 0`, output in the session scratchpad `calib.log`
- Step 5 — skipped by user decision — C taken from the six post-fix renders, not from 20 raw runs (CP2 ticked as skipped, no margin proof). Earlier note:
- Step 5 — stopped — done: 1 of 20 raw runs (run 1: drift 133 ms, render 79 s); left: 19 runs. Stopped by user (no time for the ~25 min calibration). Container and test process killed.

- Step 6 — done — files: internal/domain/recorder/recorder.go — `narrationLag = 70 * time.Millisecond`; recorder tests pass with -race
- Step 7 — started — reduced to `-count=5 -failfast`, output in scratchpad `accept.log`
- Step 7 — done — 5 of 5 pass at C = 70 ms; drift −13, 80, 72, 64, 15 ms; `sync marker ends at` 1.96, 1.92, 1.92, 1.96, 1.96 s; NFR-001 ratio 1.55, 1.53, 1.73, 1.59, 1.58. Max 80 ms leaves only 20 ms to the bound on the late side; the unshifted lag spread (10–182 ms, one 133 ms at lag 0 today) suggests C could sit higher, but not retuned (plan rule). CP3 ticked as reduced
- Step 8 — started
- Step 8 — done — files: tests/e2e/render_test.go — `maxDrift` comment: history plus the 5-render result, mentions narrationLag (decision 80); bound and `assertDrift` unchanged
- Step 9 — not done — goto residual not measured (no time); recorded as a known limit in Known debt and §17.1. Needs user approval to skip
- Step 10 — done — files: docs/ARCHITECTURE.md (§5 offset, wait, tail wait, drift bullet; §16 row 80; §17.1 item 1), docs/CODE_QUALITY.md (Known debt "Sync marker drift") — edited only our lines, the Decision 79 line is untouched; PRD FR-007 left unchanged (open question, plan default)
- Step 12 — started — make lint, vet, test, then `make e2e E2E_RACE=-race`, sequential, output in scratchpad `final.log`
- Step 12 — done — `make lint` 0 issues, `make vet` clean, `make test` 24 packages ok, `make e2e E2E_RACE=-race` all PASS (TestRender_cli drift 55 ms, NFR-001 ratio 1.49). CP5 still open: step 11 (CI after user push)
