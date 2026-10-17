# Caller-owned rendered output — Implementation Plan

## Summary

Rendered MP4s and screenshots can end up owned by root, so the user cannot delete them without `sudo` (GitHub issue #3). Commit 6080077 made `scripts/screencaster-docker.sh` pass `--user "$(id -u):$(id -g)"`. Five documented invocations still lack it (README CLI line, README `.mcp.json`, `compose.yaml`, `make e2e-runtime`, PRD MCP line). A probe also showed the flag alone does not work: the runtime image cannot launch the Playwright driver as a non-root uid, so the wrapper itself fails at browser launch today. One `chmod` line in the image fixes that.

## Success Criteria

- Every documented or shipped invocation (wrapper, README CLI, README MCP, `compose.yaml`, `make e2e-runtime`) writes `output/` MP4s and `screenshots/*.png` owned by the invoking uid:gid, files `0644`, dirs `0755`.
- The caller deletes that output with plain `rm -r`, no `sudo`.
- A non-root uid with no `/etc/passwd` entry (4242) completes a real render (screenshots and video) in the runtime image.
- `/opt/playwright-driver/node --version` runs as a non-root uid in the runtime image.
- `go test ./...` stays green, including `cmd/screencaster/docker_script_test.go`. No Go code changes.

## Scope & Constraints

- In scope: `Dockerfile` (runtime-base comment and one chmod), `README.md`, `compose.yaml`, `providers/piper/Dockerfile` (one comment), `Makefile`, `docs/PRD.md` (one line), `docs/ARCHITECTURE.md` (§12 paragraph, header).
- Out of scope: Go code, image `USER`, entrypoint, chown logic, `.screencaster/` internals (`jobs.db`, `render.lock`, `tmp`), existing root-owned leftovers (no detection, no auto-repair), rootless Docker, Docker Desktop on macOS (not testable here), historical `specs/`.
- Hard constraints: BR-001 determinism, NFR-003 offline, BR-008 one render at a time, demo YAML unchanged. Nothing on the render path changes.
- Permissions: keep `0644` files and `0755` dirs. The container umask is `0022`, so the host umask does not apply. Confirm only.
- Trade-offs: the image change breaks the "invocations only" choice from planning, because that choice cannot work without it (verified). The user approved it.
- Verified facts behind the plan:
  - `/opt/playwright-driver/node` is `-rwxr--r-- root:root` in `screencaster:piper`; as uid 12345, `node --version` gives `Permission denied`. `dev-base` runs `chmod -R a+rX` on the same paths; `runtime-base` does not.
  - Under `--user`, every Go write goes to `/work` or `/tmp` (`HOME=/tmp`, mode 1777). No Go code reads the uid or `HOME`.
  - bash `UID` is readonly: `UID=5 cmd` prints `UID: readonly variable`. `GID` is not.
  - compose v2.12.2 accepts `user: "${HOST_UID:?...}:${HOST_GID:?...}"`. An error message containing `$(` is itself interpolated and garbled, so keep `$` out of the `:?` text.

## Architecture & Design

### High-Level Flow

1. The caller runs `docker run --user <uid>:<gid> -v <project>:/work ... screencaster render demos/x.yaml` (or the MCP command, the compose run, or `make e2e-runtime`).
2. The image has no `USER`, so the flag sets the process uid. The process writes through the bind mount at `/work`; the host sees `<uid>:<gid>` files.
3. The uid must be able to run the driver (`/opt/playwright-driver/node`), Chromium (`/opt/ms-playwright`), Piper (`/opt/piper`, already `a+rX`), ffmpeg and the Go binaries. The new chmod covers the first two.

### Key Changes

- **`Dockerfile`, runtime-base**
  - Comment (lines 66–69): replace "Runs as root with the project mounted at /work." with "Runs as root unless started with --user; the wrapper, README, compose.yaml and make pass the caller's ids (ARCHITECTURE §12). The driver and browser are readable and executable by any uid." Keep the `--init` sentence.
  - RUN (lines 75–80): add `&& chmod -R a+rX /opt/playwright-driver /opt/ms-playwright \` right after `playwright install --with-deps chromium`, in the same RUN. A separate RUN would copy every Chromium file into a new layer.
- **`README.md`**
  - Line 12: `docker run --rm --user "$(id -u):$(id -g)" -v $(pwd):/work ghcr.io/makuchpatryk/screencaster screencaster render demos/my-demo.yaml`.
  - Line 46: "Add to the project's `.mcp.json`. Replace `<project>` with the absolute path of the folder that holds `demos/`, and `<uid>:<gid>` with the output of `id -u` and `id -g` (JSON does not run the shell, so type the numbers)."
  - Lines 52–55: add `"--user", "<uid>:<gid>",` between `"--init",` and `"-v",`.
- **`compose.yaml`**
  - Line 1: `# Run: HOST_UID=$(id -u) HOST_GID=$(id -g) docker compose run --rm screencaster render demos/my-demo.yaml`.
  - After `init: true`: comment "The caller's ids, so rendered files are not root-owned. Fails if unset." and `user: "${HOST_UID:?set HOST_UID to id -u}:${HOST_GID:?set HOST_GID to id -g}"`.
  - Why `HOST_*` and not `UID`/`GID`: bash `UID` is readonly, so the documented prefix command would fail.
- **`providers/piper/Dockerfile` line 55**: same compose run comment as above (a documented invocation that would go stale).
- **`Makefile`**
  - Lines 14–20: add `CALLER := --user $(shell id -u):$(shell id -g)` with the comment "keeps files created by the container owned by the caller (ARCHITECTURE §12)"; `DEV_RUN` and `E2E_RUN` use `$(CALLER)` (same behaviour as today).
  - `e2e-runtime` (line 71): `docker run --rm --init $(CALLER) -e SCREENCASTER_BIN=...`. Safe because the test binary is caller-owned `0755` in `.screencaster/` and the CLI tests write under `t.TempDir()` in `/tmp`.
- **`docs/PRD.md` line 382** (FR-016): `docker run -i --rm --init --user <uid>:<gid> -v <project>:/work screencaster screencaster-mcp`. This also drops the stale `--add-host` (Decision 79). The stale `baseUrl` text in FR-016/FR-017 is not touched.
- **`docs/ARCHITECTURE.md`**
  - Line 3: version `0.3` → `0.4`, date `2026-10-08` → `2026-10-17`.
  - Line 300: replace the last sentence ("The wrapper runs the container with … still defaults to root.") with: "The wrapper, the README commands, `compose.yaml` and the `make` run targets pass the caller's uid and gid (`--user`, compose `user:`), so rendered files belong to the caller and are deletable with `rm`; the image has no `USER` and still defaults to root. The runtime stage makes the Playwright driver and browser readable and executable by any uid, so a non-root uid can launch Chromium."
  - Decision 79 row (line 373): unchanged.
- **Script schema / MCP tools / data model / Go dependencies**: none.

### Fit with Project Docs

- **ARCHITECTURE.md**: §12 (Docker image) only. No dependency rule, pipeline rule or lock rule is touched. §13's manual release check ("a non-root render of a fixture demo") is now actually achievable for the Docker path. No deviation.
- **CODE_QUALITY.md**: KISS and YAGNI (no code, no chown, no `USER`, no entrypoint). DRY (`CALLER` holds the flag once in the Makefile). The chmod repeats the one `dev-base` already has (Dockerfile line 35); it is a different stage, so it cannot be shared.
- **Docs to update**: ARCHITECTURE §12 line 300 and header; PRD FR-016. No "Known debt" or "Open items" entry changes.

### Alternative Approaches Considered

- **Invocations only (no image change).** Simplest, but every non-root render fails at browser launch (verified). Rejected.
- **Chmod in the runtime stage (chosen).** One line, same as `dev-base`; fixes every `--user` caller at once.
- **Separate `RUN chmod`.** Copies every Chromium file again into a new layer. Rejected for image size.
- **Image `USER` or entrypoint that drops privileges.** The caller's uid must come from the invocation; a fixed uid can mismatch the host. Excluded earlier.
- **Go chown after publish.** Would not cover `.screencaster/`, adds code. Excluded earlier.
- **compose `--user` on the run command.** Forgetting the flag silently gives root files. Rejected for `HOST_UID`/`HOST_GID` with `:?`, which fails loudly.
- **compose `UID`/`GID` names.** `UID=$(id -u) …` fails in bash (readonly). Rejected.
- **Makefile: inline flag in `e2e-runtime` only.** Smaller diff but three copies of the same flag. Rejected for `CALLER`.

## Implementation Steps

1. [x] `Dockerfile` lines 66–69: update the runtime-base comment as above.
2. [x] `Dockerfile` lines 75–80: add `&& chmod -R a+rX /opt/playwright-driver /opt/ms-playwright \` after `playwright install --with-deps chromium`, in the same RUN.
3. [x] `README.md` line 12: add `--user "$(id -u):$(id -g)"`.
4. [x] `README.md` line 46 and lines 52–55: the `<uid>:<gid>` sentence and the `"--user", "<uid>:<gid>",` args.
5. [x] `compose.yaml`: new run comment (line 1) and the `user:` key with its comment after `init: true`.
6. [x] `providers/piper/Dockerfile` line 55: same compose run comment.
7. [x] `Makefile`: add `CALLER`; use it in `DEV_RUN`, `E2E_RUN` and the `e2e-runtime` `docker run`.
8. [x] `docs/PRD.md` line 382: new MCP command.
9. [x] `docs/ARCHITECTURE.md`: header version and date (line 3); replace the last sentence of line 300.
10. [x] `go test ./cmd/screencaster/...` (wrapper test still passes; `scripts/screencaster-docker.sh` stays unchanged).
11. [ ] Rebuild and run the manual checks CP1–CP5; record the output in the Implementation Log.

## Checkpoints (Todo List)

- [ ] CP1 — Image runs non-root: steps 1–2; `make image` green and `make image-check` ok. `docker run --rm --user 4242:4242 --entrypoint sh screencaster -c '/opt/playwright-driver/node --version'` prints a version. A screenshots demo rendered with `--user 4242:4242` exits 0; `find` shows `4242:4242`, PNG `644`, dirs `755`. Control: the same run without `--user` gives root-owned output.
- [ ] CP2 — README commands: steps 3–4; the README CLI line run literally (image retagged as `ghcr.io/makuchpatryk/screencaster:latest` locally) gives caller-owned output. The `.mcp.json` command with real ids answers `initialize`, `tools/list` and one `render_video` over stdio; its output is caller-owned.
- [ ] CP3 — compose: steps 5–6; `HOST_UID=$(id -u) HOST_GID=$(id -g) docker compose config` shows `user: <uid>:<gid>`. With `HOST_UID` unset it fails with the `set HOST_UID` message. A `docker compose run` render writes caller-owned output.
- [ ] CP4 — Make: step 7; `make e2e-runtime` green. `find . ! -user "$(id -un)"` lists nothing new compared with a listing taken before the run.
- [ ] CP5 — Docs and final: steps 8–10; PRD line 382 and ARCHITECTURE line 300 match the commands. `grep -n 'add-host' docs/PRD.md` no longer matches line 382. `make test` green. `rm -r demos/output/<name>` works without `sudo`.

### Risks & Mitigations

- Risk: stale local or published images lack the chmod (the local `screencaster:piper` is 8 days old).
  - Mitigation: CP1 starts with `make image`. The end-user fix arrives with the next release (`:latest` and the wrapper rebuilt from the new Dockerfile).
- Risk: other files in the image are unreadable or unexecutable for non-root.
  - Mitigation: `find /opt -not -perm -o+r` showed only Chromium `*.deps` metadata (unused at runtime). CP1's real render proves the rest.
- Risk: a placeholder `<uid>:<gid>` left in `.mcp.json`.
  - Mitigation: Docker fails loudly with an invalid-user error; the README sentence says to replace it.
- Risk: the project folder is not writable by the caller (e.g. cloned with `sudo`).
  - Mitigation: `.screencaster` creation fails with `permission denied`. Loud, out of scope.
- Risk: root-owned leftovers from earlier runs (`.screencaster/jobs.db*`, `demos/.screencaster/`, four MP4s in `demos/output/` in this repo) make a now-non-root MCP or CLI fail on open.
  - Mitigation: out of scope by decision. The user cleans up with one `sudo chown -R "$(id -u):$(id -g)"` on those paths before CP2.
- Risk: rootless Docker maps the uid to a subuid, so files are still not the caller's; Docker Desktop on macOS not tested.
  - Mitigation: not solved here; add a README note only if it shows up in use.

## Test Strategy

- Unit: no new tests. Existing `docker_script_test.go` keeps asserting the wrapper's `--user`.
- Integration: none.
- E2E (Docker image): `make e2e-runtime` as the caller (CP4).
- Manual (the user runs these, per the planning answer): CP1–CP3 and the `rm` check in CP5. Probe method: a host app from `python3 -m http.server 8765 --bind 0.0.0.0`, demo `goto` at `http://172.17.0.1:8765/`, probe folder writable by the test uid, deleted afterwards.

## Success Checklist

- [ ] Image renders for uid 4242 (CP1)
- [ ] README CLI and MCP produce caller-owned output (CP2)
- [ ] compose config and run work; unset `HOST_UID` fails loudly (CP3)
- [ ] `make e2e-runtime` green (CP4)
- [ ] PRD, ARCHITECTURE and the Piper comment match the commands (CP5)
- [ ] `make test` green; no Go change, no `USER`, no entrypoint, no chown
- [ ] Files `0644`, dirs `0755`
- [ ] `rm` works without `sudo` on the new output

## Timeline & Estimates

- Phase 1 (edits, steps 1–9): ~45 min
- Phase 2 (image rebuild and manual checks): ~1 h
- Phase 3 (review and polish): ~15 min
- **Total**: ~2 h

## Open Questions

- [ ] Commit granularity: the user decides when and how to commit (CLAUDE.md). Suggested one-line subjects: "Let any uid run the Playwright driver" (steps 1–2), "Pass the caller's uid in README and compose" (3–6), "Run make e2e-runtime as the caller" (7), "Align PRD and ARCHITECTURE on --user" (8–9).

## Implementation Log

- Step 1 — started
- Step 1 — done — files: Dockerfile — runtime-base comment updated (comment only, no build needed)
- Step 2 — started
- Step 2 — done — files: Dockerfile — chmod -R a+rX in the runtime-base RUN; image build deferred to CP1 (step 11)
- Step 3 — started
- Step 3 — done — files: README.md — --user added to the CLI line
- Step 4 — started
- Step 4 — done — files: README.md — uid:gid sentence and --user args in .mcp.json
- Step 5 — started
- Step 5 — done — files: compose.yaml — run comment and user: key; compose config shows 1000:1000, unset HOST_UID fails with the `set HOST_UID` message
- Step 6 — started
- Step 6 — done — files: providers/piper/Dockerfile — compose run comment
- Step 7 — started
- Step 7 — done — files: Makefile — CALLER var used by DEV_RUN, E2E_RUN, e2e-runtime; make -n shows --user 1000:1000 in all three
- Step 8 — started
- Step 8 — done — files: docs/PRD.md — FR-016 MCP command: --init, --user, no --add-host
- Step 9 — started
- Step 9 — done — files: docs/ARCHITECTURE.md — header 0.4 / 2026-10-17, §12 sentence replaced
- Step 10 — started
- Step 10 — done — go test ./cmd/screencaster/... ok (run in screencaster-dev; host has no go); wrapper script unchanged
- Step 11 — started
- Check — make test PASS (go test -race ./..., all packages ok); CP5 still needs the user's rm check
- Step 11 — stopped — done: make test PASS; compose config check ok; make -n shows --user in 3 targets. Left: make image, make image-check, uid 4242 node + render (CP1), README CLI/MCP (CP2), compose run (CP3), make e2e-runtime (CP4), rm check (CP5). User stopped the manual testing; e2e-runtime build halted with no result, rerun from scratch
