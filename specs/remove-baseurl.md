# Remove baseUrl, absolute goto, shorter docker command — Implementation Plan

## Summary

A demo's `baseUrl` is removed. Every `goto` must be an absolute http(s) URL. A relative `goto` fails validation. A demo that still has `baseUrl` gets one hint error and nothing else. The Docker command loses `--shm-size` and `--add-host`: Playwright already passes `--disable-dev-shm-usage`, and a host app on Linux is reached by its bridge IP written into the demo. Done now because the README docker example is the first thing users copy, and it fails today.

## Success Criteria

- `script.schema.json`, `script.Script` and `script.Parse` contain no `baseUrl` field. A test asserts the schema has no `baseUrl` property and it is not in `required`.
- A script with a top-level `baseUrl` fails with exactly one error: `/baseUrl`, `baseUrl was removed: put the app's address into each goto URL, e.g. goto: http://172.17.0.1:3000/projects`.
- `goto: /projects` fails at `/steps/<i>/goto` with `goto must be an absolute http or https URL: /projects`. `goto: https://x/y` passes.
- A script without `baseUrl` parses.
- `scripts/screencaster-docker.sh`, `compose.yaml`, `Makefile` and README contain no `--shm-size` and no `--add-host`.
- On Linux, `docker run --rm -v $(pwd):/work ghcr.io/makuchpatryk/screencaster screencaster render demos/<demo>.yaml` renders (a) a public-site demo and (b) a demo whose goto is `http://172.17.0.1:<port>/` with the app bound to 0.0.0.0. No flags beyond `--rm -v`.
- `make vet`, `make test`, `make lint`, `make e2e`, `make e2e-runtime` pass.

## Scope & Constraints

- In scope: schema, `script` Parse and types, executor, the plumbing that carries a base URL (shooter, recorder, explorer, renderer, wire), MCP descriptions and `create_demo.md`, testdata and e2e fixtures, the two local demos, docker wrapper, compose, Makefile, README, ARCHITECTURE, CODE_QUALITY.
- Out of scope: any browser code change (Playwright already disables `/dev/shm` use, see Architecture), host-name mapping code, native-run guard, deprecation shim, auto-migration, PRD and `specs/` history (see Open Questions).
- Hard constraints: BR-001 (no change to render output), NFR-003 (no new network calls; the demo's own goto URLs are the only ones), BR-010 (rule text changes, the rule is kept: goto URLs resolved by Go's `AbsoluteHTTP`).
- Trade-off: demos hardcode the address. Linux host apps use the Docker bridge IP (`172.17.0.1` by default), macOS/Windows use `host.docker.internal`. Simpler code and a shorter command, at the cost of a per-OS address in the demo.
- Breaking by user decision: old demos fail until their `baseUrl` moves into the gotos.

## Architecture & Design

### High-Level Flow

1. `script.Parse(data)`: YAML to JSON. If the top-level object has a `baseUrl` key, return the hint error only (early return). Otherwise schema validation. Then Go rules: every `goto` must pass `AbsoluteHTTP`; `checkType` runs. All errors reported together as `failure.ValidationErrors`.
2. `executor.Dispatch`: `case script.Goto` calls `page.Goto(a.URL)` directly. No resolution.
3. Chromium launches with Playwright's defaults, which include `--disable-dev-shm-usage`. The browser context has no base URL.
4. Host app on Linux: the demo's goto host is the bridge IP; the container reaches it through its default route. No DNS, no `--add-host`.

### Key Changes

- **`internal/domain/script/script.schema.json`**: remove `baseUrl` from `required` (line 6) and its property (lines 18-22). `goto` description (line 203): "Absolute http(s) URL of the page to open, e.g. https://example.com/projects". Top-level `additionalProperties: false` stays, so the schema alone rejects `baseUrl`; the Go hint explains it.
- **`internal/domain/script/script.go`**:
  - Delete `Script.BaseURL` (line 43).
  - Const `baseURLRemovedHint` with the text above.
  - `Parse`: after `YAMLToJSON`, unmarshal into `map[string]json.RawMessage`; if `baseUrl` is present return `failure.ValidationErrors{{Pointer: "/baseUrl", Message: baseURLRemovedHint}}`.
  - Replace the baseUrl `AbsoluteHTTP` block (lines ~197-203) with `gotoErrors(s)` returning one error per bad goto at `/steps/<i>/goto`, merged with `checkType(s)`.
  - `ParseSteps` (lines ~263-272): drop `BaseURL` from the placeholder; the comment about `baseUrl` satisfying required fields goes.
  - `AbsoluteHTTP` comment (line 161) no longer names baseUrl.
- **`internal/domain/script/step.go`** line 27: `Goto` comment "absolute http(s) URL (BR-010)".
- **`internal/domain/script/example.yaml`, `example-screenshots.yaml`**: remove `baseUrl`; gotos absolute.
- **`internal/domain/executor/executor.go`**: `New(p Page, m Mode) *Executor`; delete `base`, `resolve`, the `net/url` import; `Goto` passes `a.URL`. Comments naming base resolution (package and `New`) updated.
- **Callers of `executor.New`** (drop the error branch): `internal/domain/shooter/shooter.go:64`, `internal/domain/recorder/recorder.go:104`, `internal/app/explorer/explorer.go:93`.
- **Remove `BaseURL` fields and assignments**: `shooter.go` (33, 42, 58), `recorder.go` (42, 80, 100), `explorer.go` (47, 58, 84), `internal/app/renderer/render.go` (42, 117, 244, 311), `internal/app/wire/adapters.go` (34, 52), `internal/app/wire/app.go` (89, 105, 114).
- **`internal/adapters/browser/browser.go`**: delete `Options.BaseURL` (line 48) and the context `BaseURL` block (lines 162-163). Launch unchanged (Playwright default args).
- **`internal/adapters/mcpserver/server.go`**:
  - `render_video` rule (line ~99): every goto is an absolute http(s) URL, e.g. `http://172.17.0.1:3000/projects`; relative paths are rejected.
  - `explore_page` description (lines ~72-73): `url` is the absolute page; a goto in `actions` must be absolute too. Remove "resolves against `url`".
  - `explorer.Input` construction (lines ~320-324): no `BaseURL`; the comment about the base goes.
- **`internal/adapters/mcpserver/create_demo.md`** lines 13, 15, 16: ask for the app's address; write absolute gotos; `baseUrl` leaves the list of keys.
- **`scripts/screencaster-docker.sh`**: `exec docker run --rm --init -v "$PWD:/work" -w /work --entrypoint screencaster ghcr.io/makuchpatryk/screencaster:@VERSION@ "$@"`. No `--shm-size`, no `--add-host`. Comment updated.
- **`cmd/screencaster/docker_script_test.go`**: drop the "chromium shared memory" and "reaches the host app" cases; add a case asserting `--shm-size` and `--add-host` are absent. `--init`, `-v`, `-w`, entrypoint, version cases stay.
- **`compose.yaml`**: remove `shm_size` (lines 10-11) and `extra_hosts` with their comments. Keep `init: true`, image, volume.
- **`Makefile`**: remove `--shm-size=1g` from `E2E_RUN` (line 19) and the `e2e-runtime` run (line 68).
- **Local demos (gitignored, not in the diff)**: `demos/portfolio-projects.yaml` drops `baseUrl`, `goto: /` becomes `goto: https://makuchpatryk.com/`. `demos/cli-demo/demo.yaml` drops `baseUrl`; `goto: /` becomes `http://172.17.0.1:7681/`; the absolute second goto's host becomes `172.17.0.1` too.
- **`testdata/scripts/**`** (about 45 files): remove the `baseUrl:` line. Each relative goto becomes `<old baseUrl without trailing slash><path>` (so `/` becomes `<base>/`). Files:
  - `invalid/missing-baseurl.yaml`: delete (no longer invalid; covered by `valid/no-baseurl.yaml`).
  - `invalid/baseurl-relative.yaml` → `invalid/baseurl-removed.yaml`, keeps its content; expects the hint at `/baseUrl`.
  - New `invalid/goto-relative.yaml` with `goto: /projects`; expects `/steps/0/goto`.
  - New `valid/no-baseurl.yaml` (the former missing-baseurl content with absolute gotos).
  - `valid/all-target-fields.yaml`, `cards.yaml`, `cards-text.yaml`: gotos use the old base path (`https://example.com/app/...`).
- **Go tests**:
  - `internal/domain/script/script_test.go`: lines 121-122 (BaseURL assertion removed), 188-189 (missing-baseurl row → no-baseurl valid row; hint row added), 241 (`baseurl-relative` → `baseurl-removed` at `/baseUrl`), 289 and 335 (inline `baseUrl` removed). New rows: relative goto rejected at `/steps/0/goto`; absolute goto passes; schema has no `baseUrl` property or required entry.
  - `internal/domain/executor/executor_test.go` (45-61): "relative against baseUrl" becomes "goto passes the URL through unchanged"; `executor.New` call updated.
  - `shooter_test.go` (77, 94-98), `recorder_test.go` (233-238), `internal/app/renderer/render_test.go` (17, 183-203), `shots_test.go` (21, 183), `plan_test.go` (17, 24, 33, 95, 147-156, 221 keeps cookie `domain`, 324-330): remove base URL fixtures and assertions; plan_test 147-156 becomes the baseUrl-removed case.
  - `internal/adapters/mcpserver/server_test.go`: line 31 fixture; line 194 expected description phrase; 511-519 `TestExplorePage_urlIsTheBase` reworked to assert the explorer gets `URL` only. Line 420 (`get_options` has no `baseUrl`) kept. Also `shots_test.go` 17, 150.
  - `cmd/screencaster/render_test.go:151`: stray `screencaster.yaml` with `baseUrl` stays; it tests the ignored-config warning.
  - `tests/e2e/`: `render_test.go` (32, 67, 94-100, 262), `shots_test.go` (23, 40, 52, 120-126), `cards_test.go` (22), `explore_test.go` (20, 47, 65, 88, 119), `record_test.go` (120, 158), `helpers_test.go` (51): scripts use absolute gotos built from the fixture base URL; `BaseURL` fields removed from launch options. `storageState` domain `127.0.0.1` stays.
- **Docs**:
  - `README.md`: minimal docker command (line 22-23); host-app demo example uses `http://172.17.0.1:3000/...`; note: Linux uses the bridge IP (`ip route` inside the container shows it; the bridge default is `172.17.0.1`), macOS/Windows use `http://host.docker.internal:3000/...`; the host app must listen on 0.0.0.0; `--init` documented as optional for the raw command; line 57-59 compose/docker bullets rewritten; line 67-70 demo example loses `baseUrl`; line 90 public-site demo is `name` and `steps`; line 98 screenshots example; line 180 MCP docker args lose `--add-host`; one line: every goto is absolute, a relative path is rejected, `baseUrl` is a hard error with a hint.
  - `docs/ARCHITECTURE.md`: header version 0.2 → 0.3 and date; diagram line 44 (`Chromium -- host.docker.internal --> App` → bridge IP note); §4 rule 1 (line 132) drops `baseUrl`; §7 line 217 URL resolution sentence; §8 line 225 (explore url is the base) reworded; §11 line 278 (`demos/*.yaml` no longer carries `baseUrl`); §12 lines 300 and 303 (no `--add-host`, no `--shm-size`); §14 line 323 (NFR-003: no outbound traffic except absolute goto URLs); decisions table: mark 27, 58, 59 as amended/superseded; add **78** (baseUrl removed; every goto absolute; a relative goto is rejected; a baseUrl demo gets one hint) and **79** (no `--shm-size`/`--add-host` in the Docker wrapper or compose; Playwright sets `--disable-dev-shm-usage`; Linux host apps use the bridge IP in the demo, macOS/Windows `host.docker.internal`). Amends 76's flags list.
  - `docs/CODE_QUALITY.md`: line 55 (URL resolution now only in `domain/script` rule and goto; executor passes URL through); line 103 (`baseUrl` mention removed).
- No change: `internal/adapters/browser/browser.go` args, `Dockerfile`.

### Fit with Project Docs

- **ARCHITECTURE §1, BR-001, NFR-003**: output unchanged; no new network paths.
- **§4 rule 1 (validate first)**: still validates before any browser or TTS.
- **CODE_QUALITY KISS/YAGNI**: no DNS code, no resolver flag, no native guard, no shim. Removes code paths rather than adding them.
- **Separation of concerns**: nothing new reads the environment or `/proc`.
- **DRY**: `AbsoluteHTTP` is the one URL rule for goto and explore's `url`.

### Alternatives Considered

- **App maps `host.docker.internal` to the gateway in Go** (`/proc/net/route` + Chromium `--host-resolver-rules`). Works with the minimal command on Linux. Rejected: new code, a DNS lookup on every launch, and a native-run question. User chose the simpler option.
- **Keep `--add-host` for host-app demos only.** No code, but the README then needs a Linux-only flag. Rejected in favor of the bridge IP.
- **Keep `baseUrl` with a deprecation warning.** Rejected by user (hard error).
- **Hint plus schema errors** (decision-70 pattern). Rejected by user: hint only.
- **`--network host` on Linux.** Changes isolation; rejected by PRD decision 27.

## Implementation Steps

Schema and validation
1. [x] Edit `script.schema.json`: remove `baseUrl` from `required` and its property; `goto` description per Key Changes.
2. [x] Edit `script.go`: delete `Script.BaseURL`; add `baseURLRemovedHint`; in `Parse`, check the raw top-level map for `baseUrl` and return the hint error only.
3. [x] In `Parse`, replace the baseUrl `AbsoluteHTTP` block with `gotoErrors(s)`, merged with `checkType(s)`.
4. [x] In `ParseSteps`, drop `BaseURL` from the placeholder; update the `AbsoluteHTTP` comment.
5. [x] Edit `step.go` `Goto` comment; `example.yaml` and `example-screenshots.yaml`: remove `baseUrl`, absolute gotos.

Executor and plumbing
6. [x] `executor.go`: `New(p, m)` without error or base; `Goto` passes `a.URL`; delete `resolve`.
7. [x] Update `executor.New` callers in `shooter.go`, `recorder.go`, `explorer.go`.
8. [x] Remove `BaseURL` from `shooter.go`, `recorder.go`, `explorer.go`, `renderer/render.go`, `wire/adapters.go`, `wire/app.go`.
9. [x] `browser.go`: delete `Options.BaseURL` and the context `BaseURL` block.

MCP
10. [x] `server.go`: `render_video` rule, `explore_page` description, explorer input.
11. [x] `create_demo.md` lines 13, 15, 16.

Fixtures and local demos
12. [x] `testdata/scripts/**`: remove `baseUrl`, absolute gotos; delete `missing-baseurl.yaml`; rename `baseurl-relative.yaml` → `baseurl-removed.yaml`; add `valid/no-baseurl.yaml` and `invalid/goto-relative.yaml`.
13. [x] Local demos `demos/portfolio-projects.yaml` and `demos/cli-demo/demo.yaml` (gitignored, updated on disk only).

Go tests
14. [x] Update `script_test.go` (rows and assertions listed above) and add the relative-goto, absolute-goto and schema tests.
15. [x] Update `executor_test.go`, `shooter_test.go`, `recorder_test.go`, `renderer/*_test.go`, `mcpserver/*_test.go`.
16. [x] Update `tests/e2e/*` to absolute gotos and no `BaseURL` fields.

Docker and docs
17. [x] `scripts/screencaster-docker.sh` and `docker_script_test.go` (absent flags asserted).
18. [x] `compose.yaml` (remove `shm_size`, `extra_hosts`); `Makefile` (remove `--shm-size=1g` twice).
19. [x] `README.md` per Key Changes.
20. [x] `docs/ARCHITECTURE.md`: header, diagram note, §4, §7, §8, §11, §12, §14, decisions 27/58/59 marked, rows 78 and 79.
21. [x] `docs/CODE_QUALITY.md` lines 55 and 103.
22. [x] Verify: `grep -rn "baseUrl\|BaseURL\|shm-size\|add-host" --exclude-dir=.git --exclude-dir=specs .` → only the intended hits (`render_test.go:151`, the absent-flag assertions in `docker_script_test.go`, the hint text, decision rows, and the PRD if left).

## Checkpoints

- [x] CP1 — Script domain: steps 1-5; `go test -race ./internal/domain/script/...` green (hint, relative goto, absolute goto, no-baseUrl, schema tests). `grep baseUrl internal/domain/script/` only in the hint text.
- [x] CP2 — Plumbing: steps 6-9; `go vet -tags e2e ./...` clean; `grep -rn BaseURL internal cmd` no hits outside `render_test.go` comments.
- [x] CP3 — Tests and MCP: steps 10-16; `make test` green; `make lint` green.
- [ ] CP4 — Docker and docs: steps 17-21; `docker_script_test` green; `make e2e` and `make e2e-runtime` green with no shm flag. — PARTIAL: steps 17-21 done, docker_script_test green; `make e2e` / `make e2e-runtime` SKIPPED at user request, not run
- [ ] CP5 — Live Linux check: minimal `docker run` renders `demos/portfolio-projects.yaml` (public site) and a host-app demo at `http://172.17.0.1:<port>/` with the app on 0.0.0.0; ffprobe both MP4s; capture the errors for a relative goto and for a `baseUrl` script.
- [x] CP6 — Final grep (step 22) clean; docs match behaviour.

## Risks & Mitigations

- **Bridge IP differs.** Default `docker0` is `172.17.0.1`; custom networks, rootless Docker or a changed daemon config use another. Mitigation: README says how to check (`docker run --rm --entrypoint sh ghcr.io/makuchpatryk/screencaster -c 'ip route'`); a wrong IP fails as a normal navigation error.
- **macOS/Windows and the Linux IP.** `172.17.0.1` is not reachable from Docker Desktop, so a Linux demo does not run on a Mac. Mitigation: README gives the `host.docker.internal` form for Docker Desktop; demos are per machine.
- **Firewall drops docker0 traffic** (ufw, nftables). Mitigation: README note; CP5 checks with the app bound.
- **App bound to 127.0.0.1** is unreachable from the bridge. Mitigation: documented (host app must listen on 0.0.0.0).
- **Playwright default flag.** `--disable-dev-shm-usage` comes from Playwright's `chromiumSwitches` list. A Playwright bump that drops it brings back the crash. Mitigation: CP4 e2e runs without the shm flag, so the bump is caught in CI; decision 79 names the dependency.
- **Hard break for existing demos.** Mitigation: hint names the fix; the in-repo demos and testdata are migrated.
- **`explore_page` relative actions** now fail validation. Mitigation: tool description, `create_demo.md`, and explore e2e all say absolute.

## Test Strategy

- Unit (no network, no Chromium): script rules and schema; executor pass-through via `fakePage`; MCP description text; docker script template.
- E2E (`make e2e`, `make e2e-runtime`): renders, shots, cards and explore against the httptest fixture with absolute gotos, no shm flag.
- Live (CP5, manual, Linux): minimal command, public site and bridge-IP host app; ffprobe; both error messages.
- Gates: `make vet`, `make lint`, `make test`.

## Success Checklist

- [ ] Schema, `Script`, `Parse` have no `baseUrl`; schema test passes.
- [ ] Relative goto rejected at `/steps/<i>/goto`; absolute accepted.
- [ ] `baseUrl` demo gets only the hint at `/baseUrl`.
- [ ] No base URL in executor, shooter, recorder, explorer, renderer, wire, browser.
- [ ] No `--shm-size` or `--add-host` in script, compose, Makefile, README.
- [ ] MCP descriptions, `create_demo.md`, README, ARCHITECTURE (rows 78, 79, version), CODE_QUALITY updated.
- [ ] `make vet`, `make test`, `make lint`, `make e2e`, `make e2e-runtime` pass.
- [ ] Minimal `docker run` renders a public-site demo and a bridge-IP host-app demo on Linux.

## Open Questions

- [ ] **PRD.md** (14 `baseUrl` mentions, rule text in BR-010, FR-001, FR-005, decisions 15/27/58/59): update live rule text and add decisions 78/79 so the PRD matches ARCHITECTURE? Default in this plan: yes. `specs/*.md` stay as dated history.
- [ ] **`--add-host` in the wrapper and compose**: plan removes it everywhere. On Docker Desktop `host.docker.internal` resolves without it; on Linux demos use the bridge IP. Confirm removal.
- [ ] **`demos/cli-demo/demo.rc`** types the YAML on screen. Leave the recorded video as is (default) or re-record?
- [ ] **IPv4 bridge address** only. Plan assumes no IPv6 demo addresses.

## Implementation Log

(screencaster-implement appends one line per step event here, so work can resume in a new session.)
- Step 1 — started
- Step 1 — done — files: internal/domain/script/script.schema.json — removed baseUrl from required and properties, goto description absolute
- Step 2 — started
- Step 2 — done — files: internal/domain/script/script.go — dropped Script.BaseURL, added baseURLRemovedHint and baseURLRemoved (early return in Parse, before schema validation)
- Step 3 — started
- Step 3 — done — files: internal/domain/script/script.go — gotoErrors(s) merged with checkType(s)
- Step 4 — started
- Step 4 — done — files: internal/domain/script/script.go — ParseSteps placeholder has name and steps only; AbsoluteHTTP comment
- Step 5 — started
- Step 5 — done — files: internal/domain/script/step.go, example.yaml, example-screenshots.yaml — Goto comment; baseUrl removed, gotos http://host.docker.internal:3000/projects (cookie domain unchanged). Package builds; tests not yet updated (step 14); `go` is not on the host, run via the dev image (make targets)
- Step 6 — started
- Step 6 — done — files: internal/domain/executor/executor.go — New(p, m) returns *Executor, resolve and net/url gone, Goto passes a.URL
- Step 7 — started
- Step 7 — done — files: internal/domain/shooter/shooter.go, internal/domain/recorder/recorder.go, internal/app/explorer/explorer.go — dropped the error branch of executor.New
- Step 8 — started
- Step 8 — done — files: shooter.go, recorder.go, explorer.go, renderer/render.go, wire/adapters.go, wire/app.go — BaseURL fields and assignments removed; gofmt realigned structs. Pre-existing: renderer/render_test.go is not gofmt-clean (blank line at 133), left alone
- Step 9 — started
- Step 9 — done — files: internal/adapters/browser/browser.go — Options.BaseURL and context BaseURL block deleted. Non-test code builds except mcpserver (step 10)
- Step 10 — started
- Step 10 — done — files: internal/adapters/mcpserver/server.go — render_video rule, explore_page description, explorer.Input without BaseURL. `go build ./...` passes
- Step 11 — started
- Step 11 — done — files: internal/adapters/mcpserver/create_demo.md — asks for the app's address (bridge IP on Linux, host.docker.internal on Docker Desktop), absolute gotos, no baseUrl key
- Step 12 — started
- Step 12 — done — files: testdata/scripts/** (45 files), baseurl-relative.yaml renamed to baseurl-removed.yaml, missing-baseurl.yaml deleted, new valid/no-baseurl.yaml and invalid/goto-relative.yaml — baseUrl line removed, relative gotos made absolute from the old base
- Step 13 — started
- Step 13 — done — files: demos/portfolio-projects.yaml, demos/cli-demo/demo.yaml (gitignored) — baseUrl dropped, gotos absolute (public site; 172.17.0.1:7681 and :8000). demo.rc still types a baseUrl line on screen (open question, left as is)
- Step 14 — started
- Step 14 — done — files: internal/domain/script/script_test.go — rows for baseurl-removed, goto-relative, no-baseurl; hint-only test, every-relative-goto test, schema-has-no-baseUrl test; example gotos updated. `go test -race ./internal/domain/script/...` ok
- CP1 — ticked: script tests race clean; grep baseUrl in script package only in the hint code
- Step 15 — started
- Step 15 — done — files: executor_test.go, shooter_test.go, recorder_test.go, explorer_test.go, wire/adapters_test.go, renderer/{plan,render,shots}_test.go, mcpserver/{server,shots}_test.go — BaseURL fixtures removed, gotos absolute, plan_test rows: relative goto + baseUrl hint. Deviation: TestExplorePage_urlIsTheBase became TestExplorePage_relativeGotoInActionsIsRejected (LaunchOptions has no URL to assert). Also explorer_test and wire/adapters_test (not named in plan) needed it. `go test -race ./internal/...` ok, render_test.go gofmt'd
- Step 16 — started
- Step 16 — done — files: tests/e2e/{render,shots,cards,record,explore,helpers}_test.go — templates drop baseUrl and use %[1]s/path gotos (state is %[2]s), Go steps use base + "/path", BaseURL fields removed. `go vet -tags e2e ./...` clean; e2e itself runs at CP4
- CP2 — ticked: `go vet -tags e2e ./...` clean, no `BaseURL` identifier left in internal, cmd, tests (only the baseURLRemoved hint helper)
- Step 17 — started
- Step 17 — done — files: scripts/screencaster-docker.sh, cmd/screencaster/docker_script_test.go — flags dropped, comment reworded (no flag names, so the absent-flag test and the final grep stay clean); two flag cases replaced by TestDockerScript_hasNoShmSizeAndNoAddHost. `go test -race ./cmd/...` ok
- Step 18 — started
- Step 18 — done — files: compose.yaml, Makefile — shm_size, extra_hosts and both --shm-size=1g removed
- Step 19 — started
- Step 19 — done — files: README.md — minimal docker command, bridge-IP note (ip route, 0.0.0.0, firewall, Docker Desktop form), compose/docker bullets, demo and screenshots examples absolute, hard-error sentence, MCP args and prompt text. The user's uncommitted removal of the `docker pull` line is kept
- Step 20 — started
- Step 20 — done — files: docs/ARCHITECTURE.md — v0.3 / 2026-10-08, diagram label, §4 rule 1, §7, §8, §11, §12 (two places), §14, decisions 58/59 amended, rows 78 and 79, header range 76–79. Decision 27 is a PRD decision, not in this table
- Step 21 — started
- Step 21 — done — files: docs/CODE_QUALITY.md — DRY row split (executor semantics; AbsoluteHTTP as the URL rule), baseUrl dropped from the paths bullet
- Step 22 — started
- Step 22 — done — grep (excluding .git, specs, demos): remaining hits are the hint code and its tests, `baseurl-removed.yaml`, `cmd/screencaster/render_test.go:151`, the absent-flag test, README line 92 (hard-error sentence), create_demo.md ("there is no baseUrl"), ARCHITECTURE decision rows/§12, and docs/PRD.md (not in the plan's steps; open question)
- CP3 — ticked: make vet PASS, make lint PASS (0 issues), make test PASS (24 ok packages, 0 FAIL)
- Step 22 check — grep clean (see step 22 note)
- CP4 — e2e and e2e-runtime stopped before finishing at user request ("continue without this test"); box left unticked. Pending: CP5 live Linux check (needs `make image` first)
