# screencaster

Write a YAML script, run one command, get a narrated MP4 of a website. Offline, re-renderable, no LLM at render time.

![screencaster rendering a demo from the terminal, then playing the video](docs/demo.gif)

*This demo was made with screencaster itself.*

## Install

Pick a version on the [Releases](https://github.com/makuchpatryk/screencaster/releases) page; `screencaster --version` reports it (`dev` for a local build).

**Docker, nothing else to install.** The image holds both binaries, Chromium, the Piper TTS provider with both voices, and ffmpeg. Each release has a `screencaster-docker` script that runs it for you, with the flags below and the project folder mounted at `/work`:

```bash
./screencaster-docker render demos/my-demo.yaml
```

Or run the image yourself:

```bash
docker pull ghcr.io/makuchpatryk/screencaster:latest      # or :X.Y.Z
docker run --rm --init --shm-size=1g --add-host=host.docker.internal:host-gateway \
  -v $(pwd):/work ghcr.io/makuchpatryk/screencaster screencaster render demos/my-demo.yaml
```

The image is linux/amd64 only (Piper is pinned to its x86_64 build), so arm64 hosts run it under emulation. `compose.yaml` and the MCP example below use the local name `screencaster`; after a pull, `docker tag ghcr.io/makuchpatryk/screencaster:latest screencaster` makes it so (compose wants `screencaster:piper`).

**Native binaries, one command.** Each release has `screencaster_X.Y.Z_linux_<arch>.tar.gz` with `screencaster`, `screencaster-mcp` and `screencaster-docker`. The binaries bring no tools; `setup` fetches them. It is the only command that uses the network; `render` stays offline.

```bash
tar xzf screencaster_X.Y.Z_linux_amd64.tar.gz
sudo ./screencaster setup            # downloads and installs everything, once
./screencaster setup --check         # one line per piece; exit 0 only when all are ok; needs no root
./screencaster render demos/my-demo.yaml
```

- **What `setup` installs**, into `/opt/screencaster` (override with `SCREENCASTER_HOME`, for `setup` and for every later run): Piper and the two built-in voices (versions and sha256 pinned in the binary), the Playwright driver and Chromium, and ffmpeg with `ffprobe`. Afterwards any user can render, with no `SCREENCASTER_*` variable set.
- **Download values live in a `.env`-style file, not in Go:** [`internal/app/setup/pins.env`](internal/app/setup/pins.env) (Piper version, sha256 and URL base; voices revision, URL base and the voice files with their sha256). It is embedded in the binary, so a bare `setup` needs no config. To update without rebuilding, copy the keys you want to change into your own file and run `sudo ./screencaster setup --pins-file my-pins.env` (repeatable, later files win), or set the same keys as environment variables (they win over files; `SCREENCASTER_VOICE_FILES` takes `name=path=sha256` entries joined by `;`). A new `SCREENCASTER_PIPER_VERSION` needs its `SCREENCASTER_PIPER_SHA256` in the same place. Every download is still sha256-checked; a bad value or unknown key fails before any work. `sudo` drops your environment by default: use `sudo env VAR=value ./screencaster setup` or `sudo -E`.
- **Needs root** (`sudo`), because it writes `/opt/screencaster` and runs `apt-get`. `--check` needs no root.
- **Rerunning is safe.** Each piece is verified and skipped when good, repaired when missing or bad; a failed run resumes at the piece that failed.
- **Hosts it contacts:** `github.com` (Piper), `huggingface.co` (voices), `nodejs.org` and `registry.npmjs.org` (the driver) and the Playwright CDN (Chromium). Behind a proxy or mirror, the error names the piece and the URL; the `PLAYWRIGHT_NODEJS_PATH`, `NODE_MIRROR` and `PLAYWRIGHT_GO_NPM_REGISTRY` variables of playwright-go apply.
- **Debian and Ubuntu (apt) only for the automatic parts:** ffmpeg and Chromium's system libraries come from `apt-get`. On another distro `setup` still downloads the rest, then names what is left (`ffmpeg` and `ffprobe` on `PATH`; Chromium's libraries are not checked, install what Chromium needs for your distro). Not verified on other distros.
- **linux amd64 only:** `setup` refuses other architectures before doing anything (Piper is pinned to x86_64). The arm64 tarball is built but its binaries cannot run `setup`; use Docker there.

## Quick start

1. **Clone and build** (Go 1.25, one module):
   ```bash
   go build ./cmd/...      # screencaster and screencaster-mcp
   ```

2. **Docker** (recommended; one image holds both binaries, Chromium, the Piper TTS provider with both voices, and ffmpeg):
   ```bash
   make image              # builds the base, then providers/piper (tags screencaster:piper and screencaster)
   docker compose run --rm screencaster render demos/my-demo.yaml
   ```
   - `compose.yaml` sets `--init` (forwards SIGTERM, so a stopped render cleans up), `--shm-size=1g`, the `host.docker.internal` host mapping and the `.:/work` mount.
   - The host mapping lets `baseUrl: http://host.docker.internal:3000` reach an app on the host (Linux).
   - Plain Docker works too: `docker run --rm --init --shm-size=1g --add-host=host.docker.internal:host-gateway -v $(pwd):/work screencaster screencaster render demos/my-demo.yaml`.
   - The container runs as root: the rendered files belong to root.
   - The TTS provider comes from the image's environment: the Piper image sets `SCREENCASTER_TTS=piper` plus `SCREENCASTER_PIPER_BIN` and `SCREENCASTER_PIPER_VOICES`. An unset `SCREENCASTER_TTS` means `piper` at the `setup` install dir (`/opt/screencaster/piper`); both binaries exit at startup when the name is unknown or the Piper binary is not there, so a hand-built image must set the three variables. `make image-base` builds the same image without any provider; each provider is a folder `providers/<name>/` with its own `Dockerfile`, built on it by `make image PROVIDER=<name>` (default `piper`).
   - The MCP server (`screencaster-mcp`) runs from the same image with `docker run -i`, see [Claude Code (MCP)](#claude-code-mcp).

3. **Write a demo** (`demos/my-demo.yaml`). One file holds everything: the target app, the optional login and the steps. Every path in a demo (`outputDir`, an intro or outro `image`) is relative to the demo file's folder and must stay inside the working directory (`/work` in Docker):
   ```yaml
   name: my-demo
   baseUrl: http://host.docker.internal:3000   # required, absolute http(s) URL
   storageState:                               # optional; omit for a public site
     cookies:
       - {name: session, value: <value>, domain: host.docker.internal, path: /}
   outputDir: output                           # optional, default output (next to the demo)
   languages: [en, pl]
   meta:
     title: How to create a project
     description: Create a project from the projects page.
     audience: release-notes
   intro:                                      # optional, default: a built-in card
     image: assets/logo.png                    # your own picture instead (PNG or JPEG)
   outro: false                                # no end card
   steps:
     - goto: /projects
     - click: role=button[name="New project"]
       narration:
         en: Click the New project button.
         pl: Kliknij przycisk Nowy projekt.
     - fill: { selector: 'input[name="name"]', value: My Project }
   ```
   `narration` is optional on every step; a step without it is silent. A step that has it needs an entry for every selected language, and an empty string (`en: ""`) keeps the step silent in that language.

   `storageState` has the shape of Playwright's `context.storageState()` (`cookies`, `origins` with `localStorage`); paste an exported file here as it is (JSON is valid YAML). It holds live session secrets, so keep a demo that has one out of git. A demo for a public site is just `name`, `baseUrl` and `steps`: no other file is needed. A `screencaster.yaml` from an earlier version is ignored (with a warning); move its fields into the demo.

   **Start and end cards.** Every video starts with a 3 s card (the `meta` title and description) and ends with a 3 s card (a closing line in the video's language and the title), so it looks finished without an editing step. `intro` and `outro` change a card: `image` shows your picture full-frame (scaled to fit on a dark background; give it instead of `title` and `subtitle`), `title` and `subtitle` change the text, `durationMs` the time (500 to 10000), and `false` drops the card. The video is 6 s longer than the recording; use `intro: false` and `outro: false` for the plain recording. A demo for `demos/my-demo.yaml` with a logo keeps it in `demos/assets/logo.png`.

   **Screenshots.** Set `type: screenshots` to get PNGs of the site instead of a video. The steps are the same, and each `screenshot` step captures one PNG: `screenshot: true` for the viewport, or `screenshot: { fullPage: true }`, `{ selector: "#panel" }` or `{ clip: { x: 0, y: 0, width: 800, height: 450 } }`. Add `annotate` to draw a marker around one element first: `box`, `arrow`, `label` (text) and `dim` (darkens the rest). There is no narration, `languages`, `voices`, `intro` or `outro`, and no TTS or ffmpeg runs, so it takes seconds:
   ```yaml
   name: projects-screens
   type: screenshots
   baseUrl: http://host.docker.internal:3000
   steps:
     - goto: /projects
     - screenshot: true                         # 01.png, the viewport
     - screenshot: { fullPage: true }           # 02.png
     - click: role=button[name="New project"]
     - screenshot:                              # 03.png, with markers
         annotate: { selector: 'input[name="name"]', box: true, arrow: true, label: Name your project }
   ```
   The PNGs go to `<outputDir>/<name>/screenshots/01.png`, `02.png`, ... in step order, `demos/output/projects-screens/screenshots/` here. A rerun overwrites them and removes numbered shots it no longer makes; other files in that folder are never touched. This is the one exception to "never overwritten" (stable paths for docs that embed the images).

4. **Render**:
   - CLI: `screencaster render demos/my-demo.yaml` (`--lang en,pl` overrides the script's `languages`); it prints what it does on stderr (a start summary, one line per phase and language with its time, an end summary) and the video paths on stdout
   - MCP (in Claude Code): describe what you want, Claude writes the YAML and renders

## Docs

- **[PRD](docs/PRD.md)** — what it does, requirements, business logic
- **[ARCHITECTURE](docs/ARCHITECTURE.md)** — how it is built, pipeline, concurrency, data model
- **[CODE_QUALITY](docs/CODE_QUALITY.md)** — rules for writing and reviewing code

## Project layout

```
cmd/           the two binaries: screencaster (CLI) and screencaster-mcp
internal/      domain/ (pure rules; script.schema.json in domain/script), app/ (use cases), adapters/ (tools)
tests/e2e/     end-to-end tests (//go:build e2e)
testdata/      fixture HTML app and sample scripts
providers/     one folder per TTS provider: its Dockerfile layer on the base image
Dockerfile     the provider-free base image
Makefile
```

## Development

The host needs only Docker. Go, golangci-lint, Chromium, Piper and ffmpeg live in the dev image; the source is mounted, not copied.

```bash
make dev-image   # build the dev image once, rebuild when a Dockerfile changes (PROVIDER=piper by default)
make test        # go test -race ./... (one module)
make vet         # go vet -tags e2e ./...
make lint        # golangci-lint (incl. import-boundary rules)
```

```bash
make image         # build the base and the provider's runtime image (screencaster:piper, screencaster)
make image-base    # the provider-free base (screencaster-base)
make image-check   # the provider's own check: for Piper, the built-in voices are in the image
make e2e           # real Chromium, Piper, ffmpeg in the dev image
make e2e-runtime   # the CLI tests against the runtime image
```

E2E runs in CI on every push and PR (`e2e` job) and locally with `make e2e`. Run it locally on an idle machine: the drift check is timing-sensitive.

## Scripts

- `demos/*.yaml` — stored demo scripts (created via chat or CLI)
- `demos/output/*.mp4` — rendered videos, in `output/` next to the demo that made them, never overwritten (BR-006, FR-010)
- `demos/output/<name>/screenshots/NN.png` — the shots of a `type: screenshots` script, overwritten on rerun (FR-020)

## Key constraints

- One render at a time (BR-008, FR-014)
- 30 s timeout per step (BR-004, FR-008)
- 1920×1080 30 fps H.264 MP4 (FR-004, FR-009)
- Deterministic script: the same YAML and voices give the same steps, timing and narration placement; no LLM at render time (BR-001). Pixels and audio bytes may differ between runs (browser rendering, encoder, TTS build).
- Offline TTS via Piper (§9)

## Notes

- Add extra voices for the active TTS provider to `/work/voices` (Piper: `*.onnx` plus `*.onnx.json`, FR-018). Voice names are the provider's own, so switching provider may need `voices:` edits in a script
- Add `.screencaster/` to the `.gitignore` of the project you render (render lock, job DB, temp files)

## Claude Code (MCP)

`screencaster-mcp` runs inside the image and talks to Claude Code over stdio. Add it to the project's `.mcp.json`, with `<project>` the absolute path of the repo that holds `demos/`:

```json
{
  "mcpServers": {
    "screencaster": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "--init", "--add-host=host.docker.internal:host-gateway",
               "-v", "<project>:/work", "screencaster", "screencaster-mcp"]
    }
  }
}
```

- Tools: `render_video` (validates, queues a job, returns `jobId` and `position`), `take_screenshots` (the same for a `type: screenshots` script; `get_render_status` then lists the PNG paths), `get_render_status`, `explore_page` (takes an absolute `url` and an optional inline `storageState`; returns the accessibility tree with a ready-to-use selector on every interactive element) and `get_options` (installed languages and voices, audiences, existing demos).
- Prompt: `/mcp__screencaster__create_demo [description]` asks for languages, voices, audience, title, base URL and login (if any) in one message, explores the app, writes `demos/<name>.yaml` and renders it.
- Jobs run one at a time, oldest first, and are kept in `.screencaster/jobs.db`. Jobs still queued or running when the server stops are marked `failed` with `interrupted` at the next start; they do not resume.
- A CLI render and a queued job never run together: both take `.screencaster/render.lock`, and a job waits for it while still `queued`.
- `explore_page` does not wait for the queue and may overlap a running render, which can make the video's pacing jitter.
- Logs go to stderr; stdout carries protocol frames only.

## Author

Built by one developer. Not production software, no support.
