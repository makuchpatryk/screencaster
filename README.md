# screencaster

Turn a natural-language description into a narrated demo video (MP4) with re-renderable YAML scripts.

## Quick start

1. **Clone and build** (Go 1.25; `go.work` is committed):
   ```bash
   go build -o screencaster ./cli
   go build -o screencaster-mcp ./mcp
   ```

2. **Docker** (recommended; one image holds both binaries, Chromium, Piper, both voices and ffmpeg):
   ```bash
   make image        # or: docker build -t screencaster .
   docker run --rm --init --add-host=host.docker.internal:host-gateway \
     -v $(pwd):/work screencaster screencaster render demos/my-demo.yaml
   ```
   - `--init` forwards SIGTERM, so a stopped render cleans up.
   - `--add-host` lets `baseUrl: http://host.docker.internal:3000` reach an app on the host (Linux).
   - The container runs as root: files in `output/` belong to root.
   - The MCP server (`screencaster-mcp`, arrives with M5) runs from the same image with `docker run -i`.

3. **Configure** your project (`screencaster.yaml`):
   ```yaml
   baseUrl: http://host.docker.internal:3000
   storageState: auth/storageState.json
   outputDir: output
   ```

4. **Write a demo** (`demos/my-demo.yaml`):
   ```yaml
   name: my-demo
   languages: [en, pl]
   meta:
     title: How to create a project
     audience: release-notes
   steps:
     - action: goto
       url: /projects
     - action: click
       selector: role=button[name="New project"]
       narration:
         en: Click the New project button.
         pl: Kliknij przycisk Nowy projekt.
     - action: fill
       selector: input[name="name"]
       value: My Project
   ```

5. **Render**:
   - CLI: `screencaster render demos/my-demo.yaml`
   - MCP (in Claude Code): describe what you want, Claude writes the YAML and renders

## Docs

- **[PRD](docs/PRD.md)** — what it does, requirements, business logic
- **[ARCHITECTURE](docs/ARCHITECTURE.md)** — how it is built, pipeline, concurrency, data model
- **[CODE_QUALITY](docs/CODE_QUALITY.md)** — rules for writing and reviewing code

## Project layout

```
core/          shared library (no MCP, no SQLite); script.schema.json lives in core/script
cli/           screencaster render ... CLI
mcp/           screencaster-mcp MCP server
tests/e2e/     end-to-end tests (own module, local only)
testdata/      fixture HTML app and sample scripts
Dockerfile
Makefile
```

## Development

The host needs only Docker. Go, golangci-lint and (from M2) Chromium, Piper and ffmpeg live in the dev image; the source is mounted, not copied.

```bash
make dev-image   # build the dev image once, rebuild when the Dockerfile changes
make test        # go test -race in core, cli, mcp, tests/e2e
make vet         # go vet in every module
make lint        # golangci-lint (incl. import-boundary rules) in every module
```

```bash
make image         # build the runtime image
make image-check   # built-in voices from core/voices are present in it
make e2e           # real Chromium, Piper, ffmpeg in the dev image
make e2e-runtime   # the CLI tests against the runtime image
```

E2E runs locally only, not in CI. Run it on an idle machine: the recording lead-in test is timing-sensitive.

## Scripts

- `demos/*.yaml` — stored demo scripts (created via chat or CLI)
- `output/*.mp4` — rendered videos, never overwritten (BR-006, FR-010)
- `auth/storageState.json` — logged-in session state for the target app (add to `.gitignore`)

## Key constraints

- One render at a time (BR-008, FR-014)
- 30 s timeout per step (BR-004, FR-008)
- 1920×1080 30 fps H.264 MP4 (FR-004, FR-009)
- Deterministic: no LLM at render time (BR-001)
- Offline TTS via Piper (§9)

## Notes

- Add extra Piper voices to `/work/voices/*.onnx` (FR-018)
- Add `.screencaster/` to the `.gitignore` of the project you render (render lock, job DB, temp files)
- The MCP server runs inside Docker. Configure in Claude Code with:
  ```json
  {
    "name": "screencaster",
    "command": "docker",
    "args": ["run", "-i", "--rm", "--init", "--add-host=host.docker.internal:host-gateway", "-v", "<project>:/work", "screencaster", "screencaster-mcp"]
  }
  ```

## Author

Built by one developer. Not production software, no support.
