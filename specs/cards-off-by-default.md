# Cards off by default — Implementation Plan

## Summary
GitHub issue #2: today every video gets a 3 s built-in start card and a 3 s end card unless the script says `intro: false` / `outro: false` (decision 63). Cards become opt-in: an omitted `intro`/`outro` means no card. `true` or `{}` gives the built-in card, an object customises it as today, and `false` stays valid as "none". This is a breaking default change: scripts without the keys render 6 s shorter. The release and version bump are the user's. Base: branch `skills-e2e-security` at `75111d2`, clean tree. Nothing uncommitted or unmerged is relied on.

## Success Criteria
- A video script with no `intro`/`outro` renders the recording alone. No card stills reach the assembler, `Cards.Screenshot` is not called (no browser launch for cards), and there is no `[<lang>] cards:` log line (unit test with fakes).
- `intro: true`, `intro: {}` (same for `outro`) give the built-in card with today's default text and 3 s. An object with `title`/`subtitle`/`durationMs`/`image` behaves exactly as today. `false` gives no card (`Prepare` and `Parse` unit tests).
- Image validation is unchanged: same `/intro/image` / `/outro/image` pointers and messages, checked before any browser or TTS work (FR-001 edge cases, FR-002).
- A screenshots script still rejects `intro`/`outro` in every form (`true`, `false`, object) with `intro is not allowed in a screenshots script`. Non-boolean scalars and `null` are still schema errors at `/intro`.
- The schema descriptions (which reach the MCP `render_video` description through the embedded schema), `create_demo.md`, PRD (FR-001, FR-009 and its acceptance criteria, §10.1, decision 81) and ARCHITECTURE (§4, §5, decision 81) all say the same thing. `make lint`, `make vet` and `make test` are clean.

## Scope & Constraints
- **In scope:**
  - FR-001 (`intro`/`outro` inputs) and FR-009 item 5 (cards only when set).
  - Schema, `script.Bookend` decoding and `renderer.resolveCard`.
  - Unit tests and testdata.
  - The MCP `create_demo` prompt (FR-019 text): no cards unless the developer asks.
  - Demos: `demos/portfolio-projects.yaml` gets `outro: true` so it keeps today's look; `demos/cli-demo/demo.yaml` drops its redundant `intro: false` / `outro: false`.
  - **E2E exception (user decision):** implementation makes the minimal e2e edits listed in step 10 but does not run them. CI runs the suite on the PR.
- **Out of scope:**
  - The card look and text (`domain/card`), the assembler, the recorder and the start page.
  - `internal/domain/script/example.yaml`, which stays card-less to match "only when asked".
  - README (its mention of cards is neutral), historical specs, and new e2e assertions beyond the listed edits.
  - Release, tags, version bump, image builds and running e2e.
  - Interleaved slides (PRD §10.2 stays out).
- **Hard constraints:**
  - BR-001 determinism, BR-004 no partial output, BR-010 self-contained demo.
  - FR-002: every validation runs before any browser or TTS work.
  - Existing YAML with `false` keeps working.
  - The default is decided in exactly one place (`resolveCard`).
- **Trade-offs:** this is a breaking default (shorter videos for scripts without the keys) in exchange for the user's choice of plain output. There is no compatibility shim. The repo's two demos are adjusted by hand.

## Architecture & Design

### High-Level Flow
```
YAML intro/outro ──schema: omitted | true | {} | {…} | false──▶ script.Parse ──▶ *Bookend
   omitted  → nil                  ┐
   false    → &Bookend{Off: true}  ├─▶ renderer.resolveCard ─▶ Card{Off: true}   (no card)
   true, {} → &Bookend{}           │                         ─▶ built-in card, default text, 3 s
   {…}      → &Bookend{fields}     ┘                         ─▶ customised card / image (checked here)
renderLanguage → buildCards: Off ⇒ nil Still, no Shot; no shots ⇒ no Cards.Screenshot, no "cards:" log
             → Record (StartImage only when intro != nil) → Assemble (nil stills ⇒ plain graph, lead 0)
```
Downstream already handles an off card: `buildCards` (`internal/app/renderer/render.go:453`), the recorder's empty `StartImage` (plain card colour until the first `goto` paints, as `intro: false` does today) and the assembler's no-stills graph (`internal/adapters/assembler/assembler.go:193`). Only the default moves.

### Key Changes
- **`internal/domain/script/script.schema.json`**
  - In `$defs/bookend`, `"else": { "const": false }` becomes `"else": { "type": "boolean" }`. `null`, numbers and strings stay rejected at `/intro`.
  - Descriptions:
    - `intro`: "Card shown before the recording. Omit or false for none; true or {} for the built-in card (title and description); an object to change its text, image or time."
    - `outro`: the same, with "(closing line and title)".
    - `bookend`: "Start or end card: false for none, true for the built-in card, or an object. Without image the built-in card is drawn from title and subtitle."
- **`internal/domain/script/script.go`**
  - `Bookend.UnmarshalJSON` maps `true` to a zero `Bookend` (built-in card) and `false` to `Off`. Sketch:
    ```go
    func (b *Bookend) UnmarshalJSON(data []byte) error {
    	switch string(bytes.TrimSpace(data)) {
    	case "false":
    		*b = Bookend{Off: true}
    		return nil
    	case "true":
    		*b = Bookend{} // the built-in card with default text, like {}
    		return nil
    	}
    	type plain Bookend // no methods, so no recursion
    	return json.Unmarshal(data, (*plain)(b))
    }
    ```
  - The `Bookend` doc comment changes: nil means no card (decision 81), `Off` is an explicit `false`, and `true`/`{}` decode to the zero value.
  - The `UnmarshalJSON` comment drops "the schema already rejected `true`" and says it rejected every other scalar.
  - `Off` stays. `checkType` (`script.go:247`) rejects a non-nil `Intro`/`Outro` in a screenshots script, and that covers `false`.
- **`internal/app/renderer/plan.go` `resolveCard`**
  - The first lines become `if b == nil || b.Off { return Card{Off: true}, nil }`.
  - `def.Duration = …` moves after that check. The rest is unchanged.
  - The doc comment becomes "(nil: not mentioned, which means no card, decision 81)".
  - `Prepare` is unchanged. It still builds the default text that `true`/`{}`/objects use.
- **`internal/adapters/mcpserver/create_demo.md` step 4:** replace the sentence "Every video gets a 3 s start card … or `false` for none." with:

  > Videos have no start or end card. Add `intro` or `outro` only if the developer asks for one: `true` for the built-in card (meta title and description at the start, a closing line and the title at the end), an object with `title`/`subtitle` for other text, or an `image` (PNG or JPEG, a path relative to the demo file, e.g. `assets/logo.png` for a file in `demos/assets/`, which the developer provides); `durationMs` (500–10000) changes the 3 s.

  The rest of step 4 is unchanged.
- **CLI flags, MCP tool signatures and the data model:** no change. The `render_video` description changes only through the embedded schema.
- **Dependencies:** none. No `go.mod`, Dockerfile, `Makefile` or `pins.env` change.

### Fit with Project Docs
- **PRD:**
  - Implements issue #2 as decision 81, amending decision 63's default. 63's card look, picture and time stay.
  - Touches FR-001 (inputs and edge cases: `intro: true` is no longer a schema error), FR-009 item 5 and its acceptance criteria, the §10.1 bullet (line 151, "on every video") and FR-019 (prompt text only; the PRD FR-019 section has no card sentence, so no PRD edit is needed there).
  - BR-001, BR-004 and BR-010 are untouched.
- **ARCHITECTURE.md:**
  - The changes stay in `domain/script` (parse) and `app/renderer` (decide); adapters are untouched, so the dependency rules hold.
  - Timing model: with no intro, the lead is 0, as with `intro: false` today.
  - §5 line 165 ("The video is `intro card + recording + outro card`") and the §4 sequence note (line 120, "screenshot built-in cards (one launch)") get "when set".
- **CODE_QUALITY.md:**
  - KISS: the pointer's existing three states are reused, and no new field is added.
  - DRY: the default lives only in `resolveCard`, and the MCP text comes from the embedded schema.
  - DRY table row 61: "the intro/outro defaults come from `renderer.Prepare`" becomes "the intro/outro text defaults come from `renderer.Prepare`; no card unless set (`renderer.resolveCard`)".
  - No rule is bent apart from the user-approved e2e edit exception (a CLAUDE.md workflow rule, not CODE_QUALITY).
- **Docs to update:**
  - PRD §10.1 (line 151), FR-001 (lines 216 and 218), FR-009 (line 318 and acceptance lines 323–324), decisions log: new row 81 after row 75, plus a note on row 63.
  - ARCHITECTURE §4 (line 120), §5 (line 165), decisions table: new row 81 after row 80, plus a note on row 63.
  - CODE_QUALITY DRY row 61.

### Alternative Approaches Considered
- **Option A (chosen):** nil means off, `true` decodes to a zero `Bookend`, and the schema else-branch becomes `boolean`. It is a one-line default flip in `resolveCard` plus `true` support. `false` stays `Off`, so the screenshots rejection is preserved.
- **Option B: an `On bool` field on `Bookend`/`Card`.** A zero `Card` would then mean "off". It was rejected because the field has to be kept in sync with nil/`Off`, it causes more churn in `checkType`, the wiring and the tests, and it gains nothing.
- **Option C: map `false` to nil in `UnmarshalJSON` and drop `Off`.** It was rejected because `checkType` would stop rejecting `intro: false` in screenshots scripts (breaks `invalid/screenshots-intro.yaml`). A Go `UnmarshalJSON` also cannot nil its own pointer.
- **Option D: reject `false` now that it is redundant.** The user chose to keep it: existing YAML keeps working.
- **Option E: only `{}` opts in, with no `true`.** The user chose `true` as well, because it reads better for humans and LLMs.

## Implementation Steps
1. [x] `internal/domain/script/script.schema.json`: set `$defs/bookend` `else` to `{ "type": "boolean" }`, and update the `intro`, `outro` and `bookend` descriptions as in Key Changes.
2. [x] `internal/domain/script/script.go`:
   - Add the `"true"` case to `Bookend.UnmarshalJSON` (zero `Bookend`).
   - Rewrite the `Bookend` and `UnmarshalJSON` doc comments (nil = no card, decision 81).
   - `checkType` is unchanged.
3. [x] Testdata under `testdata/scripts/`:
   - `git mv invalid/intro-true.yaml valid/cards-true.yaml`. Its content becomes a comment "# Built-in cards opted in: true and {}." plus `intro: true`, `outro: {}` and one `goto` step.
   - Add `invalid/intro-string.yaml` (`intro: "yes"`). This keeps a scalar-rejection sample, expected at `/intro`.
   - Add `invalid/screenshots-intro-true.yaml` (`type: screenshots`, `intro: true`, one `screenshot: true` step), expected at `/intro` with `intro is not allowed in a screenshots script`.
   - Check that `testdata/scripts/valid/` is enumerated in `TestParse_validSamples` (`script_test.go` around line 54), and add `valid/cards-true.yaml` there if the list is explicit.
4. [x] `internal/domain/script/script_test.go`:
   - `TestParse_decodesBookends`: add `{"valid/cards-true.yaml", &Bookend{}, &Bookend{}}`, and comment the `default-langs.yaml` row as "no key: nil, which means no card".
   - `TestParse_invalidSamples`: remove the `invalid/intro-true.yaml` row (line 194). Add the `invalid/intro-string.yaml` row (`/intro`, `""`) and the `invalid/screenshots-intro-true.yaml` row.
   - `TestParse_oneRootCausePerBrokenStep`: replace the `invalid/intro-true.yaml` row (line 245) with `invalid/intro-string.yaml`.
   - checkType table (around line 717): add a screenshots case with `Intro: &Bookend{}` (the `true` form) that is rejected at `/intro`.
5. [x] `internal/app/renderer/plan.go` `resolveCard`: nil or `Off` returns `Card{Off: true}`, the default duration is set after that check, and the doc comment is updated.
6. [x] `internal/app/renderer/plan_test.go`:
   - Rename `TestPrepare_cardsDefaultToBuiltIn` to `TestPrepare_cards` (comment: decisions 63, 81).
   - Rows:
     - Row 1: extra `metaBlock + "intro: true\noutro: true\n"`.
     - Row 2: `"intro: {}\noutro: {}\n"`. This covers `{}`.
     - Add "omitted: no cards", with extra `metaBlock` and both cards `Card{Off: true}`.
     - Keep the explicit-text and `false` rows.
   - `TestPrepare_cardImageResolvesAgainstDemoDir` (lines 409–410): the omitted outro is now off, so assert `plan.Outro == (Card{Off: true})` with the message "want no end card".
7. [x] `internal/app/renderer/render_test.go`:
   - Add `intro: true\noutro: true\n` to the shared `twoStepsEnPl` fixture (line 16) after the `meta` block, so the card call lists, the log lines, the timings (`38s`/`36s`) and the cards-failure tests keep their meaning.
   - `TestRender_customImageSkipsScreenshot` (line 435): replace on the key, not on `steps:`, to avoid a duplicate key: `strings.Replace(twoStepsEnPl, "intro: true\n", "intro:\n  image: assets/logo.png\n  durationMs: 2000\n", 1)`.
   - `TestRender_cardsOffGiveNoStills` (line 458): replace `"intro: true\noutro: true\n"` with `"intro: false\noutro: false\n"`.
   - Add `TestRender_omittedCardsGiveNoStills`: the fixture with both keys removed. Assert that `f.shots` is empty, the assembler got nil `Intro`/`Outro`, `StartImage` is empty, and no log line contains `cards:`.
   - Make sure every `strings.Replace` on the fixture still matches exactly once (check the returned string differs).
8. [x] `internal/adapters/mcpserver/create_demo.md`: replace the card sentence in step 4 with the Key Changes text. Grep `internal/adapters/mcpserver/server_test.go` for the old wording ("start card", "Every video gets"), and update any assertion.
9. [x] Demos:
   - `demos/portfolio-projects.yaml`: add `outro: true` right after the `intro:` block.
   - `demos/cli-demo/demo.yaml`: delete `intro: false` and `outro: false` (lines 3–4).
10. [x] E2E edits (user-approved exception; edit only, never run):
    - `tests/e2e/render_test.go` `renderScript` (line 31): add `intro: true` and `outro: true` after the `meta:` block, before `steps:`. The drift check keeps its 3 s intro shift (`assertDrift(…, defaultCard)`, line 179) and the `[en] cards: 2 built in` log check.
    - `tests/e2e/cards_test.go` line 98: `plain.yaml` uses `""` (tests the new default: no cards).
    - `tests/e2e/cards_test.go` line 99: `default.yaml` uses `"intro: true\noutro: true\n"`.
    - `custom.yaml` is unchanged.
    - Update the decision comment at line 73 to "Decision 63, 81: cards only when set; `true` is the built-in card, an image replaces it, and omitted or `false` is none."
11. [x] PRD `docs/PRD.md`:
    - §10.1 line 151: "An optional start card and end card per video (off unless set; built in, the developer's own picture, or custom text)."
    - FR-001 line 216: "`intro`, `outro` (optional; omitted or `false` for no card, `true` or `{}` for the built-in card, or an object `{image?, title?, subtitle?, durationMs?}`; see FR-009)."
    - FR-001 line 218: drop "`intro: true`" from the schema-error list.
    - FR-009 item 5 (line 318): "put a start card before the recording and an end card after it when the script sets `intro`/`outro` (decision 81), …". The rest is unchanged.
    - FR-009 acceptance:
      - Replace line 323 with "Given a script with no `intro` or `outro`, when rendered, then the video is the recording alone."
      - Add "Given `intro: true` and `outro: true`, when rendered, then the video lasts the recording plus 6 s and its first and last 3 s are the built-in cards."
      - Keep line 324.
    - Decisions log: append row 81 after row 75.
      - Decision: "Cards are off unless the script sets `intro`/`outro`: `true` or `{}` is the built-in card, an object customises it, and omitted or `false` is none. Amends 63's default."
      - Alternatives: "Keep default-on (63); reject `false`; only `{}` opts in."
      - Why: "User choice (issue #2). A demo is plain unless cards are asked for; `false` stays valid so existing scripts keep working."
    - Row 63: append "(default amended by 81)".
12. [x] ARCHITECTURE `docs/ARCHITECTURE.md`:
    - §4 sequence note (line 120): "screenshot built-in cards (one launch, only when one is set)".
    - §5 line 165: "The video is `[intro card] + recording + [outro card]`; a card is there only when the script sets it (Decision 81)."
    - Row 63: append "(default amended by 81)".
    - Add row 81 after row 80: "Cards are off unless set; nil `Bookend` means none, decided only in `renderer.resolveCard`; `true` decodes to the zero `Bookend`, `false` stays `Off` so screenshots scripts still reject it." Alternatives: "`On` flag; `false` as nil." Why: "One place for the default; no new field."
    - CODE_QUALITY `docs/CODE_QUALITY.md` DRY row 61: as in Fit with Project Docs.
13. [x] Stale-wording sweep: `grep -rn "every video\|Every video\|unless \`false\`\|intro: true\` and\|Omit for the built-in" docs internal demos tests README.md`. It must find nothing stale outside `specs/`. Fix any hit.
14. [x] Fix R1: `docs/ARCHITECTURE.md:334` numbering note lists 81 as also in the PRD log and 76–80 as only here.

## Checkpoints (Todo List)
- [x] CP1 — Domain parse: steps 1–4. Evidence: `go test ./internal/domain/script/...` green. — tests not run (not selected); rows and samples added
- [x] CP2 — Renderer default flip: steps 5–7. Evidence: `go test ./internal/app/renderer/...` green. — tests not run (not selected)
- [x] CP3 — Prompt, demos and e2e edits: steps 8–10. Evidence: `go test ./internal/adapters/mcpserver/...` green, and `make vet` clean (it vets with `-tags e2e ./...`, so the edited e2e files compile; nothing runs). — tests and vet not run (not selected)
- [x] CP4 — Docs and final light verification: steps 11–13. Evidence: PRD, ARCHITECTURE and CODE_QUALITY sections updated, decision 81 present in both PRD and ARCHITECTURE, the step-13 grep is clean, and `make lint`, `make vet` and `make test` are clean (only the checks the user selects; no e2e run, which is CI on the PR). — docs and grep done; lint, vet, tests not run (not selected)
- [x] CP5 — Review fixes: step 14; selected checks (lint, vet, tests) green. — lint, vet, tests PASS

### Risks & Mitigations
- **Risk:** scripts outside the repo silently lose their cards (6 s shorter, different look).
  - Mitigation: decision 81, FR-009 text and the schema description state the new default. The release note and version bump are the user's.
  - Mitigation: `false` still parses, so no script starts failing.
- **Risk:** bulk renderer test breakage, or a `strings.Replace` that silently no-ops or duplicates a YAML key.
  - Mitigation: opt the shared `twoStepsEnPl` fixture in once (step 7) and replace on the `intro: true\n` key, not `steps:`.
  - Mitigation: each test asserts its modified script differs from the fixture.
- **Risk:** `true` reaches `plain` decoding and fails as a non-validation `decode script` error.
  - Mitigation: an explicit `"true"` case in `UnmarshalJSON` (step 2), covered by `valid/cards-true.yaml` (step 4).
- **Risk:** the screenshots rejection regresses for `true`.
  - Mitigation: keep `Off` and the nil check in `checkType`. Add `invalid/screenshots-intro-true.yaml` and a checkType row.
- **Risk:** CI e2e fails because a fixture still relies on default-on (drift check shifted by 3 s, `cards: 2 built in`, `assertCards(def, true)`).
  - Mitigation: step 10 opts `renderScript` and `default.yaml` in. `make vet` compile-checks them. CI runs the suite on the PR before merge.
- **Risk:** the start of a no-card video shows the plain card colour until the first `goto` paints.
  - Mitigation: accepted. This is the same as `intro: false` today (ARCHITECTURE §5 start page), and the sync marker covers it until t0.

## Test Strategy
**Light** (during implementation; run only what the user selects):
- Unit, `domain/script`: omitted → nil; `false` → `Off`; `true` and `{}` → `&Bookend{}`; object fields as today; `"yes"` and `intro: true` with image rules still rejected as before; screenshots scripts reject `true`, `false` and objects.
- Unit, `app/renderer`:
  - `Prepare`: omitted and `false` → `Card{Off:true}`; `true` and `{}` → default text and 3 s; image paths still validated at `/intro/image` before any work.
  - `Render` with fakes: omitted cards mean no `Cards.Screenshot`, nil stills to the assembler, empty `StartImage` and no `cards:` log line. Opted-in cards keep today's call order, text and timings.
- Integration, `adapters/mcpserver`: the existing tests stay green. The tool description picks up the new schema text automatically.
- `make lint`, `make vet` (includes compiling the e2e files) and `make test`.

**Heavy** (not part of implementation):
- The Docker image build and `make image-check` are not needed: there is no Dockerfile, provider or build-input change.
- E2E: the edits in step 10 are applied but not run. CI runs `tests/e2e` on the PR.
- For `/screencaster-e2e` (optional later): "a script with no `intro`/`outro` renders a video whose first and last frames are the recorded page and whose length is under `2*defaultCard + minRecording`."

**Manual:**
- Render `demos/portfolio-projects.yaml` and check it still has its picture intro and the built-in outro.
- Render `demos/cli-demo/demo.yaml` and check it has no cards.

## Success Checklist
- [ ] All success criteria met (with evidence)
- [ ] `make lint`, `make vet` and `make test` clean (implementation)
- [ ] Heavy checks: no image build needed (no Dockerfile/provider change); e2e is not a plan item (CI runs it on the PR, which covers the step-10 edits)
- [ ] Code review approved
- [ ] Documentation updated (PRD §10.1, FR-001, FR-009, decisions 63 and 81; ARCHITECTURE §4, §5, decisions 63 and 81; CODE_QUALITY DRY row)
- [ ] No regressions in existing demo scripts (`portfolio-projects` keeps its look, `cli-demo` unchanged in output)
- [ ] User action: release notes call out the breaking default; version bump and release are the user's

## Timeline & Estimates
- Phase 1 (implementation, steps 1–10): ~2 hours
- Phase 2 (testing, light checks): ~0.5 hours
- Phase 3 (docs and review polish, steps 11–13): ~1 hour
- **Total**: ~3.5 hours (rough estimate, plus buffer)

## Open Questions
(none)

## Implementation Log
- Decision: How does a script opt into the plain built-in card?: Allow `true` and `{}`
- Decision: What happens to `false`?: Keep, means none
- Decision: Existing e2e relies on default cards; how to handle?: Exception: implementation makes the minimal e2e edits (step 10), does not run them
- Decision: Keep `demos/portfolio-projects.yaml` look?: Add `outro: true`; drop redundant `intro: false`/`outro: false` from `demos/cli-demo/demo.yaml`
- Decision: e2e fixture `renderScript`?: Opt in with `intro: true` + `outro: true`
- Decision: `cards_test.go` cases?: `plain.yaml` omits both, `default.yaml` uses `true`; `false` covered by unit tests only
- Decision: MCP `create_demo` prompt?: No cards by default; add only when the developer asks
- Decision: workspace: worktree
- Decision: checks: none (user selected no checks; none run)
- Note: worktree base is `main` at 0abf352 (spec text says `skills-e2e-security`@75111d2); main checkout was clean
- Step 1 — started
- Step 1 — done — files: internal/domain/script/script.schema.json — else-branch now boolean, intro/outro/bookend descriptions updated
- Step 2 — started
- Step 2 — done — files: internal/domain/script/script.go — `true` case in Bookend.UnmarshalJSON, doc comments rewritten (decision 81)
- Step 3 — started
- Step 3 — done — files: testdata/scripts/valid/cards-true.yaml (git mv from invalid/intro-true.yaml), testdata/scripts/invalid/intro-string.yaml, testdata/scripts/invalid/screenshots-intro-true.yaml — valid list in TestParse_validSamples is explicit, row added in step 4
- Step 4 — started
- Step 4 — done — files: internal/domain/script/script_test.go — validSamples + decodesBookends rows for cards-true, intro-string replaces intro-true (2 tables), screenshots-intro-true row, checkType "intro true" row
- Step 5 — started
- Step 5 — done — files: internal/app/renderer/plan.go — nil or Off returns Card{Off:true}; duration default set after; doc comment updated
- Step 6 — started
- Step 6 — done — files: internal/app/renderer/plan_test.go — TestPrepare_cards (true, {}, omitted rows), outro image test asserts no end card
- Step 7 — started
- Step 7 — done — files: internal/app/renderer/render_test.go — fixture opted in; custom-image test replaces on key; TestRender_omittedCardsGiveNoStills added (checks req.Log lines, not f.log); cardsOff test replaces key; each Replace guarded against no-op
- Step 8 — started
- Step 8 — done — files: internal/adapters/mcpserver/create_demo.md — card sentence replaced with the spec text; server tests have no assertion on the old wording
- Step 9 — started
- Step 9 — done — files (main checkout, git-ignored, user-approved): demos/portfolio-projects.yaml (outro: true added), demos/cli-demo/demo.yaml (intro/outro: false removed) — not in the branch diff
- Step 10 — started
- Step 10 — done — files: tests/e2e/render_test.go (renderScript only; publicScript/failingScript untouched), tests/e2e/cards_test.go — edit only, e2e not run (screencaster-e2e / CI)
- Step 11 — started
- Step 11 — done — files: docs/PRD.md — §10.1, FR-001 (2 lines), FR-009 item 5 + acceptance, decisions row 81 and note on 63
- Step 12 — started
- Step 12 — done — files: docs/ARCHITECTURE.md, docs/CODE_QUALITY.md — §4 note, §5 line, rows 63 and 81, DRY row
- Step 13 — started
- Step 13 — done — sweep found only historical decision rows (63) and the PRD glossary "3 s by default" (duration, still true); nothing stale
- Fix round — Decision: workspace: worktree
- Fix round — Decision: checks: lint, vet, tests
- Step 14 — started
- Step 14 — done — files: docs/ARCHITECTURE.md — numbering note now lists 81 as also in the PRD log and 76–80 as only here (R1)
- Handoff — mode: worktree — branch: feature/cards-off-by-default — base: 0abf352 — checks: lint, vet, tests — changed: docs/ARCHITECTURE.md, docs/CODE_QUALITY.md, docs/PRD.md, internal/adapters/mcpserver/create_demo.md, internal/app/renderer/plan.go, internal/app/renderer/plan_test.go, internal/app/renderer/render_test.go, internal/domain/script/script.go, internal/domain/script/script.schema.json, internal/domain/script/script_test.go, specs/cards-off-by-default.md, testdata/scripts/valid/cards-true.yaml, testdata/scripts/invalid/intro-true.yaml (moved), testdata/scripts/invalid/intro-string.yaml, testdata/scripts/invalid/screenshots-intro-true.yaml, tests/e2e/cards_test.go, tests/e2e/render_test.go — preexisting: none (demos/portfolio-projects.yaml and demos/cli-demo/demo.yaml edited in main checkout, git-ignored) — checkpoints: 5/5

## Review
Reviewed 2026-11-05 (re-review after R1 fix) — checks run: lint PASS, vet PASS, tests PASS (cached results)
No findings.
